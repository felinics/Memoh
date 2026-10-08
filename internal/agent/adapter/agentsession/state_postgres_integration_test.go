package agentsession

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/dbtest"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/runtimefence"
)

func TestPostgresHistoryClearRemovesPublicationAndFencesOldOwner(t *testing.T) {
	ctx := context.Background()
	pool := openACPStatePostgresPool(t, ctx)
	botID, sessionID := createACPStatePostgresFixture(t, ctx, pool)
	queries := dbsqlc.New(pool)
	storeQueries := postgresstore.NewQueriesWithPool(pool, queries)
	store := NewRuntimeStateStore(storeQueries)
	runID, turnID, _ := createACPStateRun(t, ctx, queries, pool, botID, sessionID)
	persistACPStateWatermark(t, ctx, pool, botID, sessionID, runID, turnID)
	head, found, err := store.Head(ctx, botID, sessionID)
	if err != nil || !found || head.RunID != runID {
		t.Fatalf("head = %+v, %v, %v", head, found, err)
	}
	if err := storeQueries.ClearHistoryBySession(ctx, mustACPStateUUID(t, sessionID)); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Head(ctx, botID, sessionID); err != nil || found {
		t.Fatalf("cleared head = %v, %v", found, err)
	}
	moved, err := storeQueries.UpsertAgentSessionPublication(ctx, dbsqlc.UpsertAgentSessionPublicationParams{
		BotID: mustACPStateUUID(t, botID), SessionID: mustACPStateUUID(t, sessionID), RunID: mustACPStateUUID(t, runID),
	})
	if err != nil || moved != 0 {
		t.Fatalf("stale owner published after history clear: %d, %v", moved, err)
	}
}

func openACPStatePostgresPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if os.Getenv("MEMOH_TEST_POSTGRES_REQUIRED") == "1" {
			t.Fatal("ACP state PostgreSQL test is required, but TEST_POSTGRES_DSN is not set")
		}
		t.Skip("set TEST_POSTGRES_DSN to run ACP state PostgreSQL integration")
	}
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatalf("open ACP state PostgreSQL pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if os.Getenv("TEST_POSTGRES_BOOTSTRAP_SCHEMA") == "1" {
		if err := dbtest.MigratePostgresUp(dsn); err != nil {
			t.Fatalf("migrate ACP state PostgreSQL database: %v", err)
		}
	}
	return pool
}

func createACPStatePostgresFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (string, string) {
	t.Helper()
	userID := uuid.New()
	botID := uuid.New()
	sessionID := uuid.New()
	name := "acp-state-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		WITH created_user AS (
			INSERT INTO users (id, username, is_active)
			VALUES ($1, $2, true)
			RETURNING id
		)
		INSERT INTO team_members (user_id, role)
		SELECT id, 'admin' FROM created_user
	`, userID, name); err != nil {
		t.Fatalf("create ACP state user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO bots (id, owner_user_id, name) VALUES ($1, $2, $3)
	`, botID, userID, name); err != nil {
		t.Fatalf("create ACP state bot: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO bot_sessions (id, bot_id, channel_type, runtime_type)
		VALUES ($1, $2, 'local', 'acp_agent')
	`, sessionID, botID); err != nil {
		t.Fatalf("create ACP state session: %v", err)
	}
	cleanupCtx := context.WithoutCancel(ctx)
	t.Cleanup(func() {
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM bots WHERE id = $1", botID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM users WHERE id = $1", userID)
	})
	return botID.String(), sessionID.String()
}

func createACPStateRun(
	t *testing.T,
	ctx context.Context,
	queries *dbsqlc.Queries,
	pool *pgxpool.Pool,
	botID, sessionID string,
) (runID string, turnID string, fence runtimefence.Fence) {
	t.Helper()
	token, err := queries.NextSessionRuntimeFenceToken(ctx)
	if err != nil {
		t.Fatalf("allocate ACP state fence: %v", err)
	}
	runID = uuid.NewString()
	turnID = uuid.NewString()
	if _, err := pool.Exec(ctx, `
		INSERT INTO session_runs (
			run_id, bot_id, session_id, invocation_id, turn_id, turn_position,
			state, input_json, input_fingerprint, owner_id, fencing_token,
			owner_since, live_generation
		) VALUES ($1, $2, $3, $4, $5, 1, 'running', '{}'::jsonb, $6, $7, $8, now(), $9)
	`, runID, botID, sessionID, uuid.NewString(), turnID, "acp-state-input", "owner", token, "generation"); err != nil {
		t.Fatalf("create ACP state run: %v", err)
	}
	fence = runtimefence.Fence{BotID: botID, SessionID: sessionID, Token: token}
	if err := runtimefence.Activate(ctx, postgresstore.NewQueriesWithPool(pool, queries), fence); err != nil {
		t.Fatalf("activate ACP state run: %v", err)
	}
	return runID, turnID, fence
}

func persistACPStateWatermark(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	botID, sessionID, runID, turnID string,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO bot_history_messages (
			bot_id, session_id, role, content, metadata, session_mode, runtime_type,
			turn_id, turn_position, turn_message_seq, turn_visible, run_id
		) VALUES (
			$1, $2, 'assistant', '{"role":"assistant","content":"ok"}'::jsonb,
			'{"agent_turn_outcome":"succeeded"}'::jsonb,
			'chat', 'acp_agent', $3, 1, 1, true, $4
		)
	`, botID, sessionID, turnID, runID); err != nil {
		t.Fatalf("persist ACP canonical round: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_session_publications (session_id, run_id)
		VALUES ($1, $2)
		ON CONFLICT (team_id, session_id) DO UPDATE SET
			run_id = EXCLUDED.run_id,
			updated_at = now()
	`, sessionID, runID); err != nil {
		t.Fatalf("persist ACP canonical publication head: %v", err)
	}
}

func mustACPStateUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	parsed, err := dbpkg.ParseUUID(value)
	if err != nil {
		t.Fatalf("parse ACP state UUID %q: %v", value, err)
	}
	return parsed
}
