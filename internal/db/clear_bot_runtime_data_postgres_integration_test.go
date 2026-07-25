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

const clearBotRuntimeDataSQL = `
WITH target_sessions AS MATERIALIZED (
  SELECT session.id
  FROM bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.bot_id = $1
  ORDER BY session.id
  FOR UPDATE
),
target_compaction_artifacts AS MATERIALIZED (
  SELECT compact.id
  FROM bot_history_message_compacts compact
  WHERE compact.team_id = public.memoh_current_team_id()
    AND compact.bot_id = $1
    AND (SELECT count(*) FROM target_sessions) >= 0
  ORDER BY compact.id
  FOR UPDATE
),
deleted_compaction_artifacts AS (
  DELETE FROM bot_history_message_compacts compact
  USING target_compaction_artifacts target
  WHERE compact.team_id = public.memoh_current_team_id()
    AND compact.id = target.id
  RETURNING compact.id
),
target_messages AS MATERIALIZED (
  SELECT message.id
  FROM bot_history_messages message
  WHERE message.team_id = public.memoh_current_team_id()
    AND message.bot_id = $1
    AND (SELECT count(*) FROM target_sessions) >= 0
    AND (SELECT count(*) FROM deleted_compaction_artifacts) >= 0
  ORDER BY message.id
  FOR UPDATE
),
deleted_messages AS (
  DELETE FROM bot_history_messages message
  USING target_messages target
  WHERE message.team_id = public.memoh_current_team_id()
    AND message.id = target.id
  RETURNING message.id
),
deleted_sessions AS (
  DELETE FROM bot_sessions session
  USING target_sessions target
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = target.id
    AND (SELECT count(*) FROM deleted_messages) >= 0
  RETURNING session.id
)
DELETE FROM bot_channel_routes route
WHERE route.team_id = public.memoh_current_team_id()
  AND route.bot_id = $1
  AND (SELECT count(*) FROM deleted_sessions) >= 0
`

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

	if _, err := tx.Exec(ctx, clearBotRuntimeDataSQL, targetBotID); err != nil {
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
