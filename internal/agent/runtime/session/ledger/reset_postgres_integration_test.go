package ledger_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/internal/agent/runtime/session/ledger"
	dbpkg "github.com/memohai/memoh/internal/db"
	"github.com/memohai/memoh/internal/db/dbtest"
	dbsqlc "github.com/memohai/memoh/internal/db/postgres/sqlc"
	"github.com/memohai/memoh/internal/runtimefence"
)

var (
	ledgerResetMigrationOnce sync.Once
	ledgerResetMigrationErr  error
)

func TestPostgresLedgerResetLeaseLifecycleAndMutex(t *testing.T) {
	ctx := context.Background()
	pool := openLedgerResetPostgres(t, ctx)
	botID, sessionID := createLedgerResetFixture(t, ctx, pool)
	secondSessionID := createLedgerResetSession(t, ctx, pool, botID)
	store := resetStoreForTest(t, pool)

	botToken := uuid.NewString()
	botLease, applied, err := store.AcquireReset(ctx, ledger.ResetLease{Scope: ledger.ResetScopeBot, BotID: botID, Token: botToken}, time.Minute)
	if err != nil || !applied {
		t.Fatalf("acquire bot lease = (%v, %v)", applied, err)
	}
	if botLease.ExpiresAt.IsZero() || !botLease.ExpiresAt.After(time.Now()) {
		t.Fatalf("bot lease expiry = %v", botLease.ExpiresAt)
	}

	// The bot lease blocks both a competing bot lease and any session lease.
	if _, applied, err := store.AcquireReset(ctx, ledger.ResetLease{Scope: ledger.ResetScopeBot, BotID: botID, Token: uuid.NewString()}, time.Minute); err != nil || applied {
		t.Fatalf("competing bot acquire = (%v, %v), want blocked", applied, err)
	}
	if _, applied, err := store.AcquireReset(ctx, ledger.ResetLease{
		Scope: ledger.ResetScopeSession, BotID: botID, SessionID: sessionID, Token: uuid.NewString(),
	}, time.Minute); err != nil || applied {
		t.Fatalf("session acquire under bot lease = (%v, %v), want blocked", applied, err)
	}
	if effective, blocked, err := store.EffectiveReset(ctx, botID, ""); err != nil || !blocked || effective.Token != botToken {
		t.Fatalf("bot-scope effective = (%#v, %v, %v)", effective, blocked, err)
	}
	if effective, blocked, err := store.EffectiveReset(ctx, botID, sessionID); err != nil || !blocked || effective.Token != botToken {
		t.Fatalf("session-scope effective under bot lease = (%#v, %v, %v)", effective, blocked, err)
	}

	// Renewal and release are token CAS operations.
	wrongToken := botLease
	wrongToken.Token = uuid.NewString()
	if _, ok, err := store.RenewReset(ctx, wrongToken, time.Minute); err != nil || ok {
		t.Fatalf("renew with wrong token = (%v, %v), want miss", ok, err)
	}
	renewed, ok, err := store.RenewReset(ctx, botLease, 2*time.Minute)
	if err != nil || !ok || !renewed.ExpiresAt.After(botLease.ExpiresAt) {
		t.Fatalf("renew own lease = (%#v, %v, %v)", renewed, ok, err)
	}
	if ok, err := store.ReleaseReset(ctx, wrongToken); err != nil || ok {
		t.Fatalf("release with wrong token = (%v, %v), want miss", ok, err)
	}
	if ok, err := store.ReleaseReset(ctx, renewed); err != nil || !ok {
		t.Fatalf("release own lease = (%v, %v)", ok, err)
	}
	if _, blocked, err := store.EffectiveReset(ctx, botID, ""); err != nil || blocked {
		t.Fatalf("effective after release = (%v, %v), want free", blocked, err)
	}

	// A session lease blocks the bot scope, but sibling sessions coexist.
	sessionToken := uuid.NewString()
	sessionLease, applied, err := store.AcquireReset(ctx, ledger.ResetLease{
		Scope: ledger.ResetScopeSession, BotID: botID, SessionID: sessionID, Token: sessionToken,
	}, time.Minute)
	if err != nil || !applied {
		t.Fatalf("acquire session lease = (%v, %v)", applied, err)
	}
	if _, applied, err := store.AcquireReset(ctx, ledger.ResetLease{Scope: ledger.ResetScopeBot, BotID: botID, Token: uuid.NewString()}, time.Minute); err != nil || applied {
		t.Fatalf("bot acquire under session lease = (%v, %v), want blocked", applied, err)
	}
	siblingLease, applied, err := store.AcquireReset(ctx, ledger.ResetLease{
		Scope: ledger.ResetScopeSession, BotID: botID, SessionID: secondSessionID, Token: uuid.NewString(),
	}, time.Minute)
	if err != nil || !applied {
		t.Fatalf("sibling session acquire = (%v, %v), want applied", applied, err)
	}
	if effective, blocked, err := store.EffectiveReset(ctx, botID, ""); err != nil || !blocked || effective.Scope != ledger.ResetScopeSession {
		t.Fatalf("bot-scope effective under session leases = (%#v, %v, %v)", effective, blocked, err)
	}
	if effective, blocked, err := store.EffectiveReset(ctx, botID, sessionID); err != nil || !blocked || effective.Token != sessionToken {
		t.Fatalf("own-session effective = (%#v, %v, %v)", effective, blocked, err)
	}
	if ok, err := store.ReleaseReset(ctx, sessionLease); err != nil || !ok {
		t.Fatalf("release session lease = (%v, %v)", ok, err)
	}
	if ok, err := store.ReleaseReset(ctx, siblingLease); err != nil || !ok {
		t.Fatalf("release sibling lease = (%v, %v)", ok, err)
	}
}

func TestPostgresLedgerResetExpiryAllowsTakeover(t *testing.T) {
	ctx := context.Background()
	pool := openLedgerResetPostgres(t, ctx)
	botID, _ := createLedgerResetFixture(t, ctx, pool)
	store := resetStoreForTest(t, pool)

	stale, applied, err := store.AcquireReset(ctx, ledger.ResetLease{Scope: ledger.ResetScopeBot, BotID: botID, Token: uuid.NewString()}, 100*time.Millisecond)
	if err != nil || !applied {
		t.Fatalf("acquire short lease = (%v, %v)", applied, err)
	}
	time.Sleep(150 * time.Millisecond)
	if _, blocked, err := store.EffectiveReset(ctx, botID, ""); err != nil || blocked {
		t.Fatalf("expired lease still effective = (%v, %v)", blocked, err)
	}
	successor, applied, err := store.AcquireReset(ctx, ledger.ResetLease{Scope: ledger.ResetScopeBot, BotID: botID, Token: uuid.NewString()}, time.Minute)
	if err != nil || !applied {
		t.Fatalf("takeover after expiry = (%v, %v)", applied, err)
	}
	// The stale owner's token no longer renews or releases the scope.
	if _, ok, err := store.RenewReset(ctx, stale, time.Minute); err != nil || ok {
		t.Fatalf("stale owner renew = (%v, %v), want miss", ok, err)
	}
	if ok, err := store.ReleaseReset(ctx, stale); err != nil || ok {
		t.Fatalf("stale owner release = (%v, %v), want miss", ok, err)
	}
	if effective, blocked, err := store.EffectiveReset(ctx, botID, ""); err != nil || !blocked || effective.Token != successor.Token {
		t.Fatalf("successor effective = (%#v, %v, %v)", effective, blocked, err)
	}
	if ok, err := store.ReleaseReset(ctx, successor); err != nil || !ok {
		t.Fatalf("release successor lease = (%v, %v)", ok, err)
	}
}

func TestPostgresLedgerResetScopeNotFoundFailsFast(t *testing.T) {
	ctx := context.Background()
	pool := openLedgerResetPostgres(t, ctx)
	botID, sessionID := createLedgerResetFixture(t, ctx, pool)
	store := resetStoreForTest(t, pool)

	if _, _, err := store.AcquireReset(ctx, ledger.ResetLease{
		Scope: ledger.ResetScopeBot, BotID: uuid.NewString(), Token: uuid.NewString(),
	}, time.Minute); !errors.Is(err, ledger.ErrResetScopeNotFound) {
		t.Fatalf("acquire on missing bot = %v, want ErrResetScopeNotFound", err)
	}
	if _, _, err := store.AcquireReset(ctx, ledger.ResetLease{
		Scope: ledger.ResetScopeSession, BotID: botID, SessionID: uuid.NewString(), Token: uuid.NewString(),
	}, time.Minute); !errors.Is(err, ledger.ErrResetScopeNotFound) {
		t.Fatalf("acquire on missing session = %v, want ErrResetScopeNotFound", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE bot_sessions SET deleted_at = now() WHERE id = $1", sessionID); err != nil {
		t.Fatalf("soft delete session: %v", err)
	}
	if _, _, err := store.AcquireReset(ctx, ledger.ResetLease{
		Scope: ledger.ResetScopeSession, BotID: botID, SessionID: sessionID, Token: uuid.NewString(),
	}, time.Minute); !errors.Is(err, ledger.ErrResetScopeNotFound) {
		t.Fatalf("acquire on soft-deleted session = %v, want ErrResetScopeNotFound", err)
	}
}

func TestPostgresLedgerFenceAndFinalizeOrphanRequiresValidLease(t *testing.T) {
	ctx := context.Background()
	pool := openLedgerResetPostgres(t, ctx)
	botID, sessionID := createLedgerResetFixture(t, ctx, pool)
	store := resetStoreForTest(t, pool)
	orphanStore, ok := store.(ledger.OrphanResetStore)
	if !ok {
		t.Fatal("postgres ledger does not expose OrphanResetStore")
	}

	queries := dbsqlc.New(pool)
	fencingToken, err := queries.NextSessionRuntimeFenceToken(ctx)
	if err != nil {
		t.Fatalf("allocate fencing token: %v", err)
	}
	runID := uuid.NewString()
	if _, err := pool.Exec(ctx, `
		INSERT INTO session_runs (
			run_id, bot_id, session_id, invocation_id, turn_id, turn_position,
			state, input_json, input_fingerprint, fencing_token
		) VALUES ($1, $2, $3, $4, $5, 1, 'running', '{}'::jsonb, $6, $7)
	`, runID, botID, sessionID, uuid.NewString(), uuid.NewString(), "ledger-reset-input", fencingToken); err != nil {
		t.Fatalf("create orphan run: %v", err)
	}
	orphan := ledger.Run{RunID: runID, BotID: botID, SessionID: sessionID, FencingToken: fencingToken}

	// A lease that was never acquired must not be able to finalize anything.
	if _, _, err := orphanStore.FenceAndFinalizeOrphan(ctx, ledger.ResetLease{
		Scope: ledger.ResetScopeSession, BotID: botID, SessionID: sessionID, Token: uuid.NewString(),
	}, orphan); !errors.Is(err, runtimefence.ErrResetLeaseLost) {
		t.Fatalf("finalize without a lease = %v, want ErrResetLeaseLost", err)
	}

	lease, applied, err := store.AcquireReset(ctx, ledger.ResetLease{
		Scope: ledger.ResetScopeSession, BotID: botID, SessionID: sessionID, Token: uuid.NewString(),
	}, time.Minute)
	if err != nil || !applied {
		t.Fatalf("acquire reset lease = (%v, %v)", applied, err)
	}
	finalized, applied, err := orphanStore.FenceAndFinalizeOrphan(ctx, lease, orphan)
	if err != nil || !applied {
		t.Fatalf("finalize orphan run = (%v, %v)", applied, err)
	}
	if finalized.State != ledger.StateAborted {
		t.Fatalf("finalized state = %q, want aborted", finalized.State)
	}
	var state string
	if err := pool.QueryRow(ctx, "SELECT state FROM session_runs WHERE run_id = $1", runID).Scan(&state); err != nil {
		t.Fatalf("load finalized run: %v", err)
	}
	if state != string(ledger.StateAborted) {
		t.Fatalf("durable run state = %q, want aborted", state)
	}
	// A second pass finds no active run and reports applied=false, not an error.
	if _, applied, err := orphanStore.FenceAndFinalizeOrphan(ctx, lease, orphan); err != nil || applied {
		t.Fatalf("finalize already-final run = (%v, %v)", applied, err)
	}
	if ok, err := store.ReleaseReset(ctx, lease); err != nil || !ok {
		t.Fatalf("release reset lease = (%v, %v)", ok, err)
	}
}

func resetStoreForTest(t *testing.T, pool *pgxpool.Pool) ledger.ResetStore {
	t.Helper()
	store, ok := ledger.NewPostgres(dbsqlc.New(pool), pool).(ledger.ResetStore)
	if !ok {
		t.Fatal("postgres ledger does not expose ResetStore")
	}
	return store
}

func openLedgerResetPostgres(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if os.Getenv("MEMOH_TEST_POSTGRES_REQUIRED") == "1" {
			t.Fatal("ledger reset PostgreSQL test is required, but TEST_POSTGRES_DSN is not set")
		}
		t.Skip("set TEST_POSTGRES_DSN to run ledger reset PostgreSQL integration")
	}
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatalf("open ledger reset PostgreSQL pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if os.Getenv("TEST_POSTGRES_BOOTSTRAP_SCHEMA") == "1" {
		ledgerResetMigrationOnce.Do(func() {
			ledgerResetMigrationErr = dbtest.MigratePostgresUp(dsn)
		})
		if ledgerResetMigrationErr != nil {
			t.Fatalf("migrate ledger reset PostgreSQL database: %v", ledgerResetMigrationErr)
		}
	}
	return pool
}

func createLedgerResetFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (string, string) {
	t.Helper()
	userID := uuid.New()
	botID := uuid.New()
	name := "ledger-reset-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		WITH created_user AS (
			INSERT INTO users (id, username, is_active)
			VALUES ($1, $2, true)
			RETURNING id
		)
		INSERT INTO team_members (user_id, role)
		SELECT id, 'admin' FROM created_user
	`, userID, name); err != nil {
		t.Fatalf("create ledger reset user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO bots (id, owner_user_id, name) VALUES ($1, $2, $3)
	`, botID, userID, name); err != nil {
		t.Fatalf("create ledger reset bot: %v", err)
	}
	cleanupCtx := context.WithoutCancel(ctx)
	t.Cleanup(func() {
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM bots WHERE id = $1", botID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM users WHERE id = $1", userID)
	})
	return botID.String(), createLedgerResetSession(t, ctx, pool, botID.String())
}

func createLedgerResetSession(t *testing.T, ctx context.Context, pool *pgxpool.Pool, botID string) string {
	t.Helper()
	sessionID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO bot_sessions (id, bot_id, channel_type, runtime_type)
		VALUES ($1, $2, 'local', 'acp_agent')
	`, sessionID, botID); err != nil {
		t.Fatalf("create ledger reset session: %v", err)
	}
	return sessionID.String()
}
