package runtimefence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type transactionRunner interface {
	InTx(ctx context.Context, fn func(dbstore.Queries) error) error
}

type transactionCapability interface {
	SupportsTransactions() bool
}

type persistenceFenceLocker interface {
	LockSessionRuntimeFence(ctx context.Context, arg sqlc.LockSessionRuntimeFenceParams) (int64, error)
}

type sessionWriteParentLocker interface {
	LockBotForSessionWrite(ctx context.Context, id pgtype.UUID) (pgtype.UUID, error)
}

type persistenceFenceActivationLocker interface {
	LockSessionRuntimeFenceForActivation(ctx context.Context, arg sqlc.LockSessionRuntimeFenceForActivationParams) (int64, error)
}

type persistenceFenceActivator interface {
	ActivateSessionRuntimeFence(ctx context.Context, arg sqlc.ActivateSessionRuntimeFenceParams) (int64, error)
}

type preservedToolApprovalClaimer interface {
	ClaimToolApprovalRequestForRuntime(ctx context.Context, arg sqlc.ClaimToolApprovalRequestForRuntimeParams) (sqlc.ToolApprovalRequest, error)
}

type preservedUserInputClaimer interface {
	ClaimUserInputRequestForRuntime(ctx context.Context, arg sqlc.ClaimUserInputRequestForRuntimeParams) (sqlc.UserInputRequest, error)
}

type pendingToolApprovalSuperseder interface {
	SupersedePendingToolApprovalsBySession(ctx context.Context, arg sqlc.SupersedePendingToolApprovalsBySessionParams) ([]sqlc.ToolApprovalRequest, error)
}

type pendingUserInputSuperseder interface {
	SupersedePendingUserInputsBySession(ctx context.Context, arg sqlc.SupersedePendingUserInputsBySessionParams) ([]sqlc.UserInputRequest, error)
}

type waitingDecisionRunReclaimer interface {
	ReclaimWaitingDecisionSessionRun(ctx context.Context, arg sqlc.ReclaimWaitingDecisionSessionRunParams) (sqlc.SessionRun, error)
}

// Activate is the persistence ownership cutover. Redis may already reserve the
// successor as admitting, but a writer holding the previous token still
// linearizes before this transaction if it acquired the session lock first.
// Once activation commits, the previous token can never write again. Cleanup
// uses later statements in the same transaction so it sees rows committed by a
// writer that activation had to wait for.
func Activate(ctx context.Context, queries dbstore.Queries, fence Fence) error {
	return ActivateWithOptions(ctx, queries, fence, ActivationOptions{})
}

func ActivateWithOptions(ctx context.Context, queries dbstore.Queries, fence Fence, options ActivationOptions) error {
	if !fence.Valid() {
		return errors.New("runtime persistence fence is invalid")
	}
	txer, ok := queries.(transactionRunner)
	if !ok {
		return ErrTransactionsUnsupported
	}
	capability, ok := queries.(transactionCapability)
	if !ok || !capability.SupportsTransactions() {
		return ErrTransactionsUnsupported
	}
	pgBotID, err := dbpkg.ParseUUID(fence.BotID)
	if err != nil {
		return fmt.Errorf("invalid runtime fence bot id: %w", err)
	}
	pgSessionID, err := dbpkg.ParseUUID(fence.SessionID)
	if err != nil {
		return fmt.Errorf("invalid runtime fence session id: %w", err)
	}
	var preserveToolApprovalID, preserveUserInputID pgtype.UUID
	if preserved := options.PreserveDecision; preserved != nil {
		preserveID, parseErr := dbpkg.ParseUUID(preserved.ID)
		if parseErr != nil {
			return fmt.Errorf("invalid preserved runtime decision id: %w", parseErr)
		}
		switch strings.TrimSpace(preserved.Kind) {
		case DecisionToolApproval:
			preserveToolApprovalID = preserveID
		case DecisionUserInput:
			preserveUserInputID = preserveID
		default:
			return fmt.Errorf("unsupported preserved runtime decision kind %q", preserved.Kind)
		}
	}
	var reclaimRunID pgtype.UUID
	if reclaim := options.ReclaimWaitingDecision; reclaim != nil {
		if options.PreserveDecision == nil {
			return errors.New("waiting-decision reclaim requires a preserved decision")
		}
		if reclaim.PreviousToken <= 0 || fence.Token <= reclaim.PreviousToken {
			return errors.New("waiting-decision reclaim fencing tokens are invalid")
		}
		if strings.TrimSpace(reclaim.OwnerID) == "" || strings.TrimSpace(reclaim.LiveGeneration) == "" {
			return errors.New("waiting-decision reclaim owner and live generation are required")
		}
		reclaimRunID, err = dbpkg.ParseUUID(reclaim.RunID)
		if err != nil {
			return fmt.Errorf("invalid reclaimed runtime run id: %w", err)
		}
	}
	inputResult, err := json.Marshal(map[string]any{
		"status":      "canceled",
		"reason":      "runtime_superseded",
		"instruction": "The request was canceled because a newer runtime run took ownership.",
	})
	if err != nil {
		return fmt.Errorf("encode superseded runtime input result: %w", err)
	}

	err = txer.InTx(ctx, func(txQueries dbstore.Queries) error {
		if err := LockBotForSessionWrite(ctx, txQueries, fence.BotID); err != nil {
			return err
		}
		locker, ok := txQueries.(persistenceFenceActivationLocker)
		if !ok {
			return errors.New("persistence store does not support runtime fence activation locking")
		}
		activator, ok := txQueries.(persistenceFenceActivator)
		if !ok {
			return errors.New("persistence store does not support runtime fence activation")
		}
		current, err := locker.LockSessionRuntimeFenceForActivation(ctx, sqlc.LockSessionRuntimeFenceForActivationParams{
			SessionID: pgSessionID,
			BotID:     pgBotID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrStale
		}
		if err != nil {
			return fmt.Errorf("lock runtime fence for activation: %w", err)
		}
		switch {
		case current > fence.Token:
			return ErrStale
		case current == fence.Token:
			return nil
		}
		if preserved := options.PreserveDecision; preserved != nil {
			if err := claimPreservedDecision(ctx, txQueries, *preserved, preserveToolApprovalID, preserveUserInputID, pgBotID, pgSessionID, fence.Token); err != nil {
				return err
			}
		}
		if reclaim := options.ReclaimWaitingDecision; reclaim != nil {
			reclaimer, ok := txQueries.(waitingDecisionRunReclaimer)
			if !ok {
				return errors.New("persistence store does not support waiting-decision run reclaim")
			}
			_, err := reclaimer.ReclaimWaitingDecisionSessionRun(ctx, sqlc.ReclaimWaitingDecisionSessionRunParams{
				OwnerID:              pgtype.Text{String: strings.TrimSpace(reclaim.OwnerID), Valid: true},
				NewFencingToken:      fence.Token,
				LiveGeneration:       pgtype.Text{String: strings.TrimSpace(reclaim.LiveGeneration), Valid: true},
				RunID:                reclaimRunID,
				PreviousFencingToken: reclaim.PreviousToken,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrStale
			}
			if err != nil {
				return fmt.Errorf("reclaim waiting-decision run: %w", err)
			}
		}
		activated, err := activator.ActivateSessionRuntimeFence(ctx, sqlc.ActivateSessionRuntimeFenceParams{
			SessionID:           pgSessionID,
			BotID:               pgBotID,
			RuntimeFencingToken: fence.Token,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrStale
		}
		if err != nil {
			return fmt.Errorf("advance runtime persistence fence: %w", err)
		}
		if activated != fence.Token {
			return ErrStale
		}
		toolSuperseder, ok := txQueries.(pendingToolApprovalSuperseder)
		if !ok {
			return errors.New("persistence store does not support superseding tool approvals")
		}
		if _, err := toolSuperseder.SupersedePendingToolApprovalsBySession(ctx, sqlc.SupersedePendingToolApprovalsBySessionParams{
			Reason:     "tool approval cancelled: superseded by a newer runtime run",
			BotID:      pgBotID,
			SessionID:  pgSessionID,
			PreserveID: preserveToolApprovalID,
		}); err != nil {
			return fmt.Errorf("cancel superseded tool approvals: %w", err)
		}
		userInputSuperseder, ok := txQueries.(pendingUserInputSuperseder)
		if !ok {
			return errors.New("persistence store does not support superseding user inputs")
		}
		if _, err := userInputSuperseder.SupersedePendingUserInputsBySession(ctx, sqlc.SupersedePendingUserInputsBySessionParams{
			ResultJson: inputResult,
			BotID:      pgBotID,
			SessionID:  pgSessionID,
			PreserveID: preserveUserInputID,
		}); err != nil {
			return fmt.Errorf("cancel superseded user inputs: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("activate runtime persistence fence: %w", err)
	}
	return nil
}

func claimPreservedDecision(
	ctx context.Context,
	queries dbstore.Queries,
	preserved PreservedDecision,
	toolApprovalID, userInputID, botID, sessionID pgtype.UUID,
	token int64,
) error {
	claimToken := pgtype.Int8{Int64: token, Valid: true}
	switch strings.TrimSpace(preserved.Kind) {
	case DecisionToolApproval:
		claimer, ok := queries.(preservedToolApprovalClaimer)
		if !ok {
			return errors.New("persistence store does not support runtime tool approval claims")
		}
		_, err := claimer.ClaimToolApprovalRequestForRuntime(ctx, sqlc.ClaimToolApprovalRequestForRuntimeParams{
			RuntimeFencingToken: claimToken,
			ID:                  toolApprovalID,
			BotID:               botID,
			SessionID:           sessionID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPreservedDecisionUnavailable
		}
		if err != nil {
			return fmt.Errorf("claim preserved tool approval: %w", err)
		}
	case DecisionUserInput:
		claimer, ok := queries.(preservedUserInputClaimer)
		if !ok {
			return errors.New("persistence store does not support runtime user input claims")
		}
		_, err := claimer.ClaimUserInputRequestForRuntime(ctx, sqlc.ClaimUserInputRequestForRuntimeParams{
			RuntimeFencingToken: claimToken,
			ID:                  userInputID,
			BotID:               botID,
			SessionID:           sessionID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPreservedDecisionUnavailable
		}
		if err != nil {
			return fmt.Errorf("claim preserved user input: %w", err)
		}
	default:
		return fmt.Errorf("unsupported preserved runtime decision kind %q", preserved.Kind)
	}
	return nil
}

// InTransaction locks and validates the durable token in the same real
// PostgreSQL transaction as fn. Redis admission reserves control ownership;
// token activation is the linearization point for persistence ownership.
func InTransaction(
	ctx context.Context,
	queries dbstore.Queries,
	botID string,
	sessionID string,
	fn func(dbstore.Queries) error,
) error {
	if fn == nil {
		return errors.New("runtime-fenced transaction callback is required")
	}
	fence, ok := FromContext(ctx)
	if !ok {
		return errors.New("runtime persistence fence is missing")
	}
	if strings.TrimSpace(botID) == "" {
		botID = fence.BotID
	}
	if strings.TrimSpace(sessionID) == "" {
		sessionID = fence.SessionID
	}
	if err := ValidateScope(ctx, botID, sessionID); err != nil {
		return err
	}
	txer, ok := queries.(transactionRunner)
	if !ok {
		return ErrTransactionsUnsupported
	}
	capability, ok := queries.(transactionCapability)
	if !ok || !capability.SupportsTransactions() {
		return ErrTransactionsUnsupported
	}
	return txer.InTx(ctx, func(txQueries dbstore.Queries) error {
		if err := LockBotForSessionWrite(ctx, txQueries, botID); err != nil {
			return err
		}
		if err := Lock(ctx, txQueries, botID, sessionID); err != nil {
			return err
		}
		return fn(txQueries)
	})
}

// LockBotForSessionWrite establishes parent-before-child lock ordering before
// a transaction locks a session and writes rows that reference its bot.
func LockBotForSessionWrite(ctx context.Context, queries dbstore.Queries, botID string) error {
	locker, ok := queries.(sessionWriteParentLocker)
	if !ok {
		return errors.New("persistence store does not support bot parent locking")
	}
	pgBotID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		return fmt.Errorf("invalid runtime fence bot id: %w", err)
	}
	if _, err := locker.LockBotForSessionWrite(ctx, pgBotID); errors.Is(err, pgx.ErrNoRows) {
		return ErrStale
	} else if err != nil {
		return fmt.Errorf("lock runtime persistence bot: %w", err)
	}
	return nil
}

// Lock validates the current fence and serializes writers on the session row
// until the caller's transaction ends. A successor cannot activate its token
// until that write commits, and an older token cannot write after activation.
func Lock(ctx context.Context, queries dbstore.Queries, botID, sessionID string) error {
	fence, ok := FromContext(ctx)
	if !ok {
		return errors.New("runtime persistence fence is missing")
	}
	if err := ValidateScope(ctx, botID, sessionID); err != nil {
		return err
	}
	locker, ok := queries.(persistenceFenceLocker)
	if !ok {
		return errors.New("persistence store does not support runtime fence locking")
	}
	pgBotID, err := dbpkg.ParseUUID(fence.BotID)
	if err != nil {
		return fmt.Errorf("invalid runtime fence bot id: %w", err)
	}
	pgSessionID, err := dbpkg.ParseUUID(fence.SessionID)
	if err != nil {
		return fmt.Errorf("invalid runtime fence session id: %w", err)
	}
	_, err = locker.LockSessionRuntimeFence(ctx, sqlc.LockSessionRuntimeFenceParams{
		SessionID:           pgSessionID,
		BotID:               pgBotID,
		RuntimeFencingToken: fence.Token,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrStale
	}
	if err != nil {
		return fmt.Errorf("lock runtime persistence fence: %w", err)
	}
	return nil
}
