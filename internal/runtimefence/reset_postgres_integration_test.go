package runtimefence_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/runtimefence"
)

func TestPostgresPublishBotRuntimeConfigTwoPhase(t *testing.T) {
	ctx := context.Background()
	pool := openRuntimeFencePostgres(t, ctx)
	botID, _ := createRuntimeFenceFixtures(t, ctx, pool)
	store := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))
	token := grantBotResetLease(t, ctx, pool, botID)
	epochBefore, expiresBefore := loadBotResetState(t, ctx, pool, botID)

	fencedCtx := runtimefence.WithResetContext(ctx, runtimefence.ResetFence{
		Scope: "bot", BotID: botID, Token: token, LeaseTTL: time.Minute,
	})
	publishFailure := errors.New("workspace write failed")
	var epochDuringPublish int64
	publishErr, guardErr := runtimefence.PublishBotRuntimeConfig(fencedCtx, store, botID, time.Minute, func(context.Context) error {
		// Phase A must already be committed when the external write starts, so
		// a crash mid-write leaves every old-epoch process permanently stale.
		epochDuringPublish, _ = loadBotResetState(t, ctx, pool, botID)
		return publishFailure
	})
	if guardErr != nil {
		t.Fatalf("PublishBotRuntimeConfig() guard error = %v", guardErr)
	}
	if !errors.Is(publishErr, publishFailure) {
		t.Fatalf("PublishBotRuntimeConfig() publish error = %v, want the callback failure", publishErr)
	}
	if epochDuringPublish != epochBefore+1 {
		t.Fatalf("epoch during publish = %d, want the committed phase-A bump %d", epochDuringPublish, epochBefore+1)
	}
	epochAfter, expiresAfter := loadBotResetState(t, ctx, pool, botID)
	if epochAfter != epochBefore+1 {
		t.Fatalf("epoch after publish = %d, want exactly one bump", epochAfter)
	}
	// The phase-B transaction must have refreshed the lease even though the
	// publish callback failed.
	if !expiresAfter.After(expiresBefore) {
		t.Fatalf("lease expiry %v was not refreshed past %v", expiresAfter, expiresBefore)
	}

	// A fence with a stale token must not publish or bump anything.
	staleCtx := runtimefence.WithResetContext(ctx, runtimefence.ResetFence{
		Scope: "bot", BotID: botID, Token: uuid.NewString(), LeaseTTL: time.Minute,
	})
	published := false
	publishErr, guardErr = runtimefence.PublishBotRuntimeConfig(staleCtx, store, botID, time.Minute, func(context.Context) error {
		published = true
		return nil
	})
	if publishErr != nil || !errors.Is(guardErr, runtimefence.ErrResetLeaseLost) {
		t.Fatalf("stale-token publish = (%v, %v), want ErrResetLeaseLost guard", publishErr, guardErr)
	}
	if published {
		t.Fatal("stale token still ran the publish callback")
	}
	// Phase A validates the token before bumping, so the epoch is unchanged.
	if epochFinal, _ := loadBotResetState(t, ctx, pool, botID); epochFinal != epochAfter {
		t.Fatalf("epoch after stale publish = %d, want unchanged %d", epochFinal, epochAfter)
	}
}

func TestPostgresInResetTransactionValidatesScopeAndRefreshes(t *testing.T) {
	ctx := context.Background()
	pool := openRuntimeFencePostgres(t, ctx)
	botID, sessionID := createRuntimeFenceFixtures(t, ctx, pool)
	store := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))
	token := grantSessionResetLease(t, ctx, pool, botID, sessionID)
	expiresBefore := loadSessionResetExpiry(t, ctx, pool, sessionID)

	fencedCtx := runtimefence.WithResetContext(ctx, runtimefence.ResetFence{
		Scope: "session", BotID: botID, SessionID: sessionID, Token: token, LeaseTTL: time.Minute,
	})
	ran := false
	if err := runtimefence.InResetTransaction(fencedCtx, store, botID, sessionID, func(dbstore.Queries) error {
		ran = true
		return nil
	}); err != nil || !ran {
		t.Fatalf("fenced transaction = (%v, ran=%v)", err, ran)
	}
	if expiresAfter := loadSessionResetExpiry(t, ctx, pool, sessionID); !expiresAfter.After(expiresBefore) {
		t.Fatalf("lease expiry %v was not refreshed in-transaction past %v", expiresAfter, expiresBefore)
	}

	// A session lease must not authorize a callback that omits the session
	// scope: that would let a session reset perform bot-wide mutations.
	if err := runtimefence.InResetTransaction(fencedCtx, store, botID, "", func(dbstore.Queries) error {
		t.Fatal("bot-wide callback ran under a session lease")
		return nil
	}); !errors.Is(err, runtimefence.ErrResetLeaseLost) {
		t.Fatalf("missing session scope = %v, want ErrResetLeaseLost", err)
	}
	if err := runtimefence.InResetTransaction(fencedCtx, store, botID, uuid.NewString(), func(dbstore.Queries) error {
		t.Fatal("callback ran for a different session")
		return nil
	}); !errors.Is(err, runtimefence.ErrResetLeaseLost) {
		t.Fatalf("foreign session scope = %v, want ErrResetLeaseLost", err)
	}

	// Once a successor holds the row, the old token fails closed mid-mutation.
	if _, err := pool.Exec(ctx, `
		UPDATE bot_sessions
		SET runtime_reset_token = $1, runtime_reset_expires_at = now() + interval '1 minute'
		WHERE id = $2
	`, uuid.New(), sessionID); err != nil {
		t.Fatalf("hand lease to successor: %v", err)
	}
	if err := runtimefence.InResetTransaction(fencedCtx, store, botID, sessionID, func(dbstore.Queries) error {
		t.Fatal("callback ran after the lease was taken over")
		return nil
	}); !errors.Is(err, runtimefence.ErrResetLeaseLost) {
		t.Fatalf("taken-over lease = %v, want ErrResetLeaseLost", err)
	}

	// Without a fence the transaction helper is an ordinary pass-through.
	ran = false
	if err := runtimefence.InResetTransaction(ctx, store, botID, sessionID, func(dbstore.Queries) error {
		ran = true
		return nil
	}); err != nil || !ran {
		t.Fatalf("unfenced pass-through = (%v, ran=%v)", err, ran)
	}
}

func grantBotResetLease(t *testing.T, ctx context.Context, pool *pgxpool.Pool, botID string) string {
	t.Helper()
	token := uuid.New()
	if _, err := pool.Exec(ctx, `
		UPDATE bots
		SET runtime_reset_token = $1, runtime_reset_expires_at = now() + interval '1 minute'
		WHERE id = $2
	`, token, botID); err != nil {
		t.Fatalf("grant bot reset lease: %v", err)
	}
	return token.String()
}

func grantSessionResetLease(t *testing.T, ctx context.Context, pool *pgxpool.Pool, botID, sessionID string) string {
	t.Helper()
	token := uuid.New()
	if _, err := pool.Exec(ctx, `
		UPDATE bot_sessions
		SET runtime_reset_token = $1, runtime_reset_expires_at = now() + interval '1 minute'
		WHERE id = $2 AND bot_id = $3
	`, token, sessionID, botID); err != nil {
		t.Fatalf("grant session reset lease: %v", err)
	}
	return token.String()
}

func loadBotResetState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, botID string) (int64, time.Time) {
	t.Helper()
	var epoch int64
	var expires time.Time
	if err := pool.QueryRow(ctx, `
		SELECT runtime_config_epoch, runtime_reset_expires_at FROM bots WHERE id = $1
	`, botID).Scan(&epoch, &expires); err != nil {
		t.Fatalf("load bot reset state: %v", err)
	}
	return epoch, expires
}

func loadSessionResetExpiry(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sessionID string) time.Time {
	t.Helper()
	var expires time.Time
	if err := pool.QueryRow(ctx, `
		SELECT runtime_reset_expires_at FROM bot_sessions WHERE id = $1
	`, sessionID).Scan(&expires); err != nil {
		t.Fatalf("load session reset expiry: %v", err)
	}
	return expires
}
