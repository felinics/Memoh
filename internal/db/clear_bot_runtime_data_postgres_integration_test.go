package db

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func TestClearBotRuntimeDataClearsCompactionArtifactsPostgresPath(t *testing.T) {
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
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	schema := "clear_bot_runtime_compaction_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := tx.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL search_path TO "+quotedSchema); err != nil {
		t.Fatalf("set search path: %v", err)
	}
	bindTeamQueryFixture(t, ctx, tx)
	if _, err := tx.Exec(ctx, `
CREATE TABLE bot_channel_routes (
  id UUID PRIMARY KEY,
  bot_id UUID NOT NULL,
  team_id UUID NOT NULL DEFAULT public.memoh_current_team_id()
);
CREATE TABLE bot_sessions (
  id UUID PRIMARY KEY,
  bot_id UUID NOT NULL,
  team_id UUID NOT NULL DEFAULT public.memoh_current_team_id(),
  route_id UUID REFERENCES bot_channel_routes(id) ON DELETE SET NULL
);
CREATE TABLE bot_history_message_compacts (
  id UUID PRIMARY KEY,
  bot_id UUID NOT NULL,
  team_id UUID NOT NULL DEFAULT public.memoh_current_team_id(),
  session_id UUID REFERENCES bot_sessions(id) ON DELETE CASCADE
);
CREATE TABLE bot_history_messages (
  id UUID PRIMARY KEY,
  bot_id UUID NOT NULL,
  team_id UUID NOT NULL DEFAULT public.memoh_current_team_id(),
  session_id UUID REFERENCES bot_sessions(id) ON DELETE SET NULL,
  compact_id UUID REFERENCES bot_history_message_compacts(id) ON DELETE SET NULL
);
`); err != nil {
		t.Fatalf("create clear-bot-runtime schema: %v", err)
	}

	targetBotID := "00000000-0000-0000-0000-00000000b301"
	foreignBotID := "00000000-0000-0000-0000-00000000b302"
	if _, err := tx.Exec(ctx, `
INSERT INTO bot_channel_routes (id, bot_id) VALUES
  ('00000000-0000-0000-0000-00000000d301', '00000000-0000-0000-0000-00000000b301'),
  ('00000000-0000-0000-0000-00000000d302', '00000000-0000-0000-0000-00000000b302');
INSERT INTO bot_sessions (id, bot_id, route_id) VALUES
  ('00000000-0000-0000-0000-00000000e301', '00000000-0000-0000-0000-00000000b301', '00000000-0000-0000-0000-00000000d301'),
  ('00000000-0000-0000-0000-00000000e302', '00000000-0000-0000-0000-00000000b302', '00000000-0000-0000-0000-00000000d302');
INSERT INTO bot_history_message_compacts (id, bot_id, session_id) VALUES
  ('00000000-0000-0000-0000-00000000c301', '00000000-0000-0000-0000-00000000b301', '00000000-0000-0000-0000-00000000e301'),
  ('00000000-0000-0000-0000-00000000c302', '00000000-0000-0000-0000-00000000b302', '00000000-0000-0000-0000-00000000e302');
INSERT INTO bot_history_messages (id, bot_id, session_id, compact_id) VALUES
  ('00000000-0000-0000-0000-00000000a301', '00000000-0000-0000-0000-00000000b301', '00000000-0000-0000-0000-00000000e301', '00000000-0000-0000-0000-00000000c301'),
  ('00000000-0000-0000-0000-00000000a302', '00000000-0000-0000-0000-00000000b302', '00000000-0000-0000-0000-00000000e302', '00000000-0000-0000-0000-00000000c302');
`); err != nil {
		t.Fatalf("insert clear-bot-runtime fixtures: %v", err)
	}

	parsedTargetBotID, err := ParseUUID(targetBotID)
	if err != nil {
		t.Fatalf("parse target bot id: %v", err)
	}
	if err := sqlc.New(tx).ClearBotRuntimeData(ctx, parsedTargetBotID); err != nil {
		t.Fatalf("clear bot runtime data: %v", err)
	}
	for _, table := range []string{
		"bot_history_message_compacts",
		"bot_history_messages",
		"bot_sessions",
		"bot_channel_routes",
	} {
		assertBotRowCount(t, ctx, tx, table, targetBotID, 0)
		assertBotRowCount(t, ctx, tx, table, foreignBotID, 1)
	}
}

func assertBotRowCount(t *testing.T, ctx context.Context, tx pgx.Tx, table, botID string, want int) {
	t.Helper()
	var got int
	query := "SELECT count(*) FROM " + pgx.Identifier{table}.Sanitize() + " WHERE bot_id = $1"
	if err := tx.QueryRow(ctx, query, botID).Scan(&got); err != nil {
		t.Fatalf("count %s rows: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s rows for bot %s = %d, want %d", table, botID, got, want)
	}
}
