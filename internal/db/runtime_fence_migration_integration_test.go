package db

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSessionRuntimeFenceMigrationPostgresPath(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	schema := "runtime_fence_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := tx.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL search_path TO "+quotedSchema+", public"); err != nil {
		t.Fatalf("set search path: %v", err)
	}
	applyPreTeamBaseline(t, ctx, tx)
	if _, err := tx.Exec(ctx, readEmbeddedMigration(t, "postgres/legacy/v1/migrations/0113_session_runtime_fencing_token.down.sql")); err != nil {
		t.Fatalf("create pre-0113 schema: %v", err)
	}

	assertColumnExists(t, ctx, tx, schema, "bot_sessions", "runtime_fencing_token", false)
	assertColumnExists(t, ctx, tx, schema, "tool_approval_requests", "runtime_fencing_token", false)
	assertColumnExists(t, ctx, tx, schema, "user_input_requests", "runtime_fencing_token", false)

	userID := uuid.NewString()
	botID := uuid.NewString()
	sessionID := uuid.NewString()
	if _, err := tx.Exec(ctx, "INSERT INTO users (id) VALUES ($1)", userID); err != nil {
		t.Fatalf("insert pre-0113 user: %v", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO bots (id, owner_user_id, type, name) VALUES ($1, $2, 'personal', $3)", botID, userID, "runtime-fence-migration"); err != nil {
		t.Fatalf("insert pre-0113 bot: %v", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO bot_sessions (id, bot_id) VALUES ($1, $2)", sessionID, botID); err != nil {
		t.Fatalf("insert pre-0113 session: %v", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO tool_approval_requests (
  bot_id, session_id, tool_call_id, tool_name, operation, tool_input, short_id
) VALUES ($1, $2, 'tool-call', 'exec', 'exec', '{}'::jsonb, 1)
`, botID, sessionID); err != nil {
		t.Fatalf("insert pre-0113 tool approval: %v", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO user_input_requests (
  bot_id, session_id, tool_call_id, short_id, input_json
) VALUES ($1, $2, 'input-call', 1, '{}'::jsonb)
`, botID, sessionID); err != nil {
		t.Fatalf("insert pre-0113 user input: %v", err)
	}

	up := readEmbeddedMigration(t, "postgres/legacy/v1/migrations/0113_session_runtime_fencing_token.up.sql")
	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("apply 0113 up: %v", err)
	}
	assertColumnExists(t, ctx, tx, schema, "bot_sessions", "runtime_fencing_token", true)
	assertColumnExists(t, ctx, tx, schema, "tool_approval_requests", "runtime_fencing_token", true)
	assertColumnExists(t, ctx, tx, schema, "user_input_requests", "runtime_fencing_token", true)

	var sessionToken int64
	if err := tx.QueryRow(ctx, "SELECT runtime_fencing_token FROM bot_sessions WHERE id = $1", sessionID).Scan(&sessionToken); err != nil {
		t.Fatalf("read migrated session fence: %v", err)
	}
	if sessionToken != 0 {
		t.Fatalf("migrated session fence = %d, want 0", sessionToken)
	}
	for _, table := range []string{"tool_approval_requests", "user_input_requests"} {
		var token *int64
		if err := tx.QueryRow(ctx, "SELECT runtime_fencing_token FROM "+pgx.Identifier{table}.Sanitize()+" WHERE session_id = $1", sessionID).Scan(&token); err != nil {
			t.Fatalf("read migrated %s fence: %v", table, err)
		}
		if token != nil {
			t.Fatalf("migrated %s fence = %d, want NULL", table, *token)
		}
	}

	var firstToken int64
	if err := tx.QueryRow(ctx, "SELECT nextval('session_runtime_fencing_token_seq')").Scan(&firstToken); err != nil {
		t.Fatalf("allocate initial migrated runtime fence: %v", err)
	}
	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("reapply 0113 up: %v", err)
	}
	var nextToken int64
	if err := tx.QueryRow(ctx, "SELECT nextval('session_runtime_fencing_token_seq')").Scan(&nextToken); err != nil {
		t.Fatalf("allocate migrated runtime fence: %v", err)
	}
	if nextToken <= firstToken {
		t.Fatalf("next runtime fence after reapply = %d, want greater than %d", nextToken, firstToken)
	}
}
