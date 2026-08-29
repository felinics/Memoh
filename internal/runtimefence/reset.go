package runtimefence

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

const (
	defaultBotRuntimePublishTimeout = 2 * time.Minute
	botRuntimePublishFinalizeBudget = 15 * time.Second
	defaultResetRefreshTTL          = 30 * time.Second
)

// PublishBotRuntimeConfig serializes a bot-scoped external runtime-config
// write against reset successors and stale ACP processes.
//
// The epoch invalidation is intentionally committed in a short first
// transaction before any external side effect. A second transaction then
// holds the bot parent lock while publish runs. This two-phase order makes a
// partial workspace write safe even if the process crashes or the long
// transaction later rolls back: every process bound to the old epoch was
// already made permanently stale. The second transaction refreshes the exact
// reset token before committing, even when publish reports an error.
//
// publishErr is the external callback result. guardErr means the reset/epoch
// boundary itself did not complete and must not be downgraded to a warning.
func PublishBotRuntimeConfig(
	ctx context.Context,
	queries dbstore.Queries,
	botID string,
	publishTimeout time.Duration,
	publish func(context.Context) error,
) (publishErr error, guardErr error) {
	if publish == nil {
		return nil, errors.New("runtime config publish callback is required")
	}
	fence, ok := ResetFromContext(ctx)
	botID = strings.TrimSpace(botID)
	if !ok || fence.Scope != "bot" || botID == "" || fence.BotID != botID {
		return nil, ErrResetLeaseLost
	}
	if cause := context.Cause(ctx); cause != nil {
		return nil, NormalizeResetError(ctx, cause)
	}
	if publishTimeout <= 0 {
		publishTimeout = defaultBotRuntimePublishTimeout
	}
	botUUID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		return nil, fmt.Errorf("invalid runtime config bot id: %w", err)
	}
	resetToken, err := dbpkg.ParseUUID(fence.Token)
	if err != nil {
		return nil, fmt.Errorf("invalid runtime config reset token: %w", err)
	}

	// Phase A is deliberately short and independently committed. Do not move
	// this bump into the long external-I/O transaction: a crash after a partial
	// file write would roll that transaction back and revive old runtimes.
	if err := InResetTransaction(ctx, queries, botID, "", func(txQueries dbstore.Queries) error {
		_, bumpErr := txQueries.BumpBotRuntimeConfigEpoch(ctx, botUUID)
		return bumpErr
	}); err != nil {
		return nil, err
	}
	if cause := context.Cause(ctx); cause != nil {
		// The committed epoch bump is harmless without an external publication.
		return nil, NormalizeResetError(ctx, cause)
	}

	txer, ok := queries.(transactionRunner)
	if !ok {
		return nil, ErrTransactionsUnsupported
	}
	capability, ok := queries.(transactionCapability)
	if !ok || !capability.SupportsTransactions() {
		return nil, ErrTransactionsUnsupported
	}

	// The reset owner context may be canceled when its renewer blocks behind
	// this transaction's own bot row lock. Detach only after the fresh phase-B
	// token check; the exact DB token and parent lock then exclude successors.
	totalTimeout := publishTimeout + botRuntimePublishFinalizeBudget
	if totalTimeout < publishTimeout { // duration overflow
		totalTimeout = publishTimeout
	}
	txBase := WithResetContext(context.WithoutCancel(ctx), fence)
	txCtx, cancelTx := context.WithTimeout(txBase, totalTimeout)
	defer cancelTx()

	guardErr = txer.InTx(txCtx, func(txQueries dbstore.Queries) error {
		if _, lockErr := txQueries.LockBotForRuntimeReset(txCtx, botUUID); lockErr != nil {
			if errors.Is(lockErr, pgx.ErrNoRows) {
				return ErrResetLeaseLost
			}
			return fmt.Errorf("lock runtime config bot: %w", lockErr)
		}
		if validateErr := ValidateResetLocked(txCtx, txQueries, botID, ""); validateErr != nil {
			return validateErr
		}

		publishCtx, cancelPublish := context.WithTimeout(txCtx, publishTimeout)
		publishErr = publish(publishCtx)
		cancelPublish()

		leaseTTL := fence.LeaseTTL
		if leaseTTL <= 0 {
			leaseTTL = defaultResetRefreshTTL
		}
		leaseMilliseconds := leaseTTL.Milliseconds()
		if leaseMilliseconds < 1 {
			leaseMilliseconds = 1
		}
		if _, refreshErr := txQueries.RefreshLockedBotRuntimeReset(txCtx, sqlc.RefreshLockedBotRuntimeResetParams{
			LeaseMilliseconds: leaseMilliseconds,
			BotID:             botUUID,
			ResetToken:        resetToken,
		}); refreshErr != nil {
			if errors.Is(refreshErr, pgx.ErrNoRows) {
				return ErrResetLeaseLost
			}
			return fmt.Errorf("refresh runtime config reset: %w", refreshErr)
		}
		// Delay the callback error so the refresh transaction commits. The
		// already-committed phase-A epoch protects partial external writes.
		return nil
	})
	if guardErr != nil {
		return publishErr, guardErr
	}
	return publishErr, nil
}

// InResetTransaction validates a tokenized reset lease under the bot parent
// lock and runs fn in that same real PostgreSQL transaction. Contexts without a
// reset fence retain the ordinary direct path for non-reset callers.
func InResetTransaction(
	ctx context.Context,
	queries dbstore.Queries,
	botID string,
	sessionID string,
	fn func(dbstore.Queries) error,
) error {
	if fn == nil {
		return errors.New("history reset transaction callback is required")
	}
	fence, ok := ResetFromContext(ctx)
	if !ok {
		return fn(queries)
	}
	if errors.Is(context.Cause(ctx), ErrResetLeaseLost) {
		return ErrResetLeaseLost
	}
	botID = strings.TrimSpace(botID)
	sessionID = strings.TrimSpace(sessionID)
	if botID == "" {
		botID = fence.BotID
	}
	if botID != fence.BotID {
		return ErrResetLeaseLost
	}
	if fence.Scope == "session" {
		// A session-scoped lease must never authorize a callback whose caller
		// accidentally omitted the child scope. Requiring the explicit id keeps
		// bot-wide mutations fail closed.
		if sessionID == "" || sessionID != fence.SessionID {
			return ErrResetLeaseLost
		}
	}
	txer, ok := queries.(transactionRunner)
	if !ok {
		return ErrTransactionsUnsupported
	}
	capability, ok := queries.(transactionCapability)
	if !ok || !capability.SupportsTransactions() {
		return ErrTransactionsUnsupported
	}
	pgBotID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		return fmt.Errorf("invalid history reset bot id: %w", err)
	}
	err = txer.InTx(ctx, func(txQueries dbstore.Queries) error {
		if _, err := txQueries.LockBotForRuntimeReset(ctx, pgBotID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrResetLeaseLost
			}
			return fmt.Errorf("lock history reset bot: %w", err)
		}
		if err := ValidateResetLocked(ctx, txQueries, botID, sessionID); err != nil {
			return err
		}
		// Extend the lease while holding the parent lock. The out-of-band
		// renewer can be starved behind this same lock for the duration of a
		// long mutation (a large history restore, for example); refreshing
		// here keeps the lease alive for as long as fenced work is actually
		// progressing, without loosening the token CAS.
		if err := refreshResetLocked(ctx, txQueries, fence, botID, sessionID); err != nil {
			return err
		}
		return fn(txQueries)
	})
	return NormalizeResetError(ctx, err)
}

// refreshResetLocked extends the validated lease's expiry using the fence's
// own TTL. The caller must already hold the bot parent lock and have validated
// the token in this transaction.
func refreshResetLocked(ctx context.Context, queries dbstore.Queries, fence ResetFence, botID, sessionID string) error {
	leaseTTL := fence.LeaseTTL
	if leaseTTL <= 0 {
		leaseTTL = defaultResetRefreshTTL
	}
	leaseMilliseconds := leaseTTL.Milliseconds()
	if leaseMilliseconds < 1 {
		leaseMilliseconds = 1
	}
	pgBotID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		return fmt.Errorf("invalid history reset bot id: %w", err)
	}
	pgToken, err := dbpkg.ParseUUID(fence.Token)
	if err != nil {
		return fmt.Errorf("invalid history reset token: %w", err)
	}
	switch fence.Scope {
	case "bot":
		_, err = queries.RefreshLockedBotRuntimeReset(ctx, sqlc.RefreshLockedBotRuntimeResetParams{
			LeaseMilliseconds: leaseMilliseconds, BotID: pgBotID, ResetToken: pgToken,
		})
	case "session":
		pgSessionID, parseErr := dbpkg.ParseUUID(sessionID)
		if parseErr != nil {
			return fmt.Errorf("invalid history reset session id: %w", parseErr)
		}
		_, err = queries.RefreshLockedBotSessionRuntimeReset(ctx, sqlc.RefreshLockedBotSessionRuntimeResetParams{
			LeaseMilliseconds: leaseMilliseconds, SessionID: pgSessionID, BotID: pgBotID, ResetToken: pgToken,
		})
	default:
		return ErrResetLeaseLost
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrResetLeaseLost
	}
	if err != nil {
		return fmt.Errorf("refresh history reset lease: %w", err)
	}
	return nil
}

// ResetLeaseFailure reports whether a fenced mutation failed because its
// reset lease was lost, folding in the cause carried by the owner context
// (the renewer cancels the context, so the direct error may only say
// "context canceled"). It returns the error to surface, or nil when the
// failure is unrelated to the lease. Handlers use this instead of repeating
// the context.Cause / errors.Is dance at every call site.
func ResetLeaseFailure(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if cause := context.Cause(ctx); errors.Is(cause, ErrResetLeaseLost) {
		return errors.Join(cause, err)
	}
	if errors.Is(err, ErrResetLeaseLost) {
		return err
	}
	return nil
}

// NormalizeResetError preserves the stable lease-loss identity when context
// cancellation wins a race with a database call and the driver reports only
// context.Canceled or context.DeadlineExceeded.
func NormalizeResetError(ctx context.Context, err error) error {
	if errors.Is(context.Cause(ctx), ErrResetLeaseLost) {
		if err == nil || errors.Is(err, ErrResetLeaseLost) {
			return ErrResetLeaseLost
		}
		return errors.Join(ErrResetLeaseLost, err)
	}
	return err
}

// ValidateResetLocked checks the reset token using a fresh statement while the
// caller's transaction already holds the bot parent lock.
func ValidateResetLocked(ctx context.Context, queries dbstore.Queries, botID, sessionID string) error {
	fence, ok := ResetFromContext(ctx)
	if !ok {
		return nil
	}
	if errors.Is(context.Cause(ctx), ErrResetLeaseLost) {
		return ErrResetLeaseLost
	}
	botID = strings.TrimSpace(botID)
	sessionID = strings.TrimSpace(sessionID)
	if botID == "" {
		botID = fence.BotID
	}
	if botID != fence.BotID {
		return ErrResetLeaseLost
	}
	if fence.Scope == "session" {
		if sessionID == "" || sessionID != fence.SessionID {
			return ErrResetLeaseLost
		}
	}
	pgBotID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		return fmt.Errorf("invalid history reset bot id: %w", err)
	}
	pgToken, err := dbpkg.ParseUUID(fence.Token)
	if err != nil {
		return fmt.Errorf("invalid history reset token: %w", err)
	}
	switch fence.Scope {
	case "bot":
		if _, err := queries.ValidateLockedBotRuntimeReset(ctx, sqlc.ValidateLockedBotRuntimeResetParams{
			BotID: pgBotID, ResetToken: pgToken,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrResetLeaseLost
			}
			return fmt.Errorf("validate bot history reset: %w", err)
		}
	case "session":
		pgSessionID, err := dbpkg.ParseUUID(sessionID)
		if err != nil {
			return fmt.Errorf("invalid history reset session id: %w", err)
		}
		if _, err := queries.ValidateLockedBotSessionRuntimeReset(ctx, sqlc.ValidateLockedBotSessionRuntimeResetParams{
			BotID: pgBotID, SessionID: pgSessionID, ResetToken: pgToken,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrResetLeaseLost
			}
			return fmt.Errorf("validate session history reset: %w", err)
		}
	default:
		return ErrResetLeaseLost
	}
	return nil
}
