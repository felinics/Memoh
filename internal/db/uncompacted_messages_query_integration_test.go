package db

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// TestListUncompactedMessagesReclaimEligibility exercises the real SQL
// eligibility predicate against PostgreSQL: usable summaries and fresh pending
// leases stay excluded, while stale or failed claims are reclaimable.
func TestListUncompactedMessagesReclaimEligibility(t *testing.T) {
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

	schema := "uncompacted_query_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := tx.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL search_path TO "+quotedSchema+", public"); err != nil {
		t.Fatalf("set search path: %v", err)
	}
	baseline := readEmbeddedPreTeamInit(t)
	if _, err := tx.Exec(ctx, baseline); err != nil {
		t.Fatalf("apply 0001 baseline: %v", err)
	}
	bindTeamQueryFixture(t, ctx, tx)
	if _, err := tx.Exec(ctx, `
ALTER TABLE users ADD COLUMN team_id UUID NOT NULL DEFAULT public.memoh_current_team_id();
ALTER TABLE bots ADD COLUMN team_id UUID NOT NULL DEFAULT public.memoh_current_team_id();
ALTER TABLE bot_sessions ADD COLUMN team_id UUID NOT NULL DEFAULT public.memoh_current_team_id();
ALTER TABLE bot_channel_routes ADD COLUMN team_id UUID NOT NULL DEFAULT public.memoh_current_team_id();
ALTER TABLE channel_identities ADD COLUMN team_id UUID NOT NULL DEFAULT public.memoh_current_team_id();
ALTER TABLE bot_history_message_compacts ADD COLUMN team_id UUID NOT NULL DEFAULT public.memoh_current_team_id();
ALTER TABLE bot_history_messages ADD COLUMN team_id UUID NOT NULL DEFAULT public.memoh_current_team_id();

DROP VIEW bot_visible_history_messages;
CREATE VIEW bot_visible_history_messages AS
SELECT
  m.team_id,
  m.turn_id,
  m.turn_position,
  m.turn_message_seq,
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id,
  m.source_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.usage,
  m.compact_id,
  m.session_mode,
  m.runtime_type,
  m.model_id,
  m.event_id,
  m.display_text,
  m.created_at
FROM bot_history_messages m
WHERE m.turn_visible = true
  AND m.turn_id IS NOT NULL
  AND m.turn_position IS NOT NULL
  AND m.turn_message_seq IS NOT NULL;
`); err != nil {
		t.Fatalf("add team query fixture schema: %v", err)
	}

	userID := uuid.NewString()
	botID := uuid.NewString()
	sessionID := uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO users (id) VALUES ($1)`, userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO bots (id, owner_user_id, type, name) VALUES ($1, $2, 'personal', 'reclaim-test')`, botID, userID); err != nil {
		t.Fatalf("insert bot: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO bot_sessions (id, bot_id) VALUES ($1, $2)`, sessionID, botID); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	logs := map[string]string{
		"usable":       uuid.NewString(),
		"error":        uuid.NewString(),
		"pendingFresh": uuid.NewString(),
		"pendingStale": uuid.NewString(),
		"poison":       uuid.NewString(),
		"whitespace":   uuid.NewString(),
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO bot_history_message_compacts (id, bot_id, session_id, status, summary, started_at) VALUES
  ($1, $7, $8, 'ok', 'a usable summary', now()),
  ($2, $7, $8, 'error', '', now()),
  ($3, $7, $8, 'pending', '', now()),
  ($4, $7, $8, 'pending', '', now() - INTERVAL '16 minutes'),
  ($5, $7, $8, 'ok', '', now()),
  ($6, $7, $8, 'ok', E'  \n\t', now())
`, logs["usable"], logs["error"], logs["pendingFresh"], logs["pendingStale"], logs["poison"], logs["whitespace"], botID, sessionID); err != nil {
		t.Fatalf("insert compact logs: %v", err)
	}

	type fixture struct {
		name      string
		compactID string
		metadata  string
		eligible  bool
	}
	fixtures := []fixture{
		{name: "plain", eligible: true},
		{name: "covered by usable summary", compactID: logs["usable"], eligible: false},
		{name: "log failed", compactID: logs["error"], eligible: true},
		{name: "fresh pending lease", compactID: logs["pendingFresh"], eligible: false},
		{name: "stale pending lease", compactID: logs["pendingStale"], eligible: true},
		{name: "legacy ok with empty summary", compactID: logs["poison"], eligible: true},
		{name: "legacy ok with whitespace-only summary", compactID: logs["whitespace"], eligible: true},
		{name: "passive sync", metadata: `{"trigger_mode":"passive_sync"}`, eligible: false},
	}
	wantEligible := make(map[string]string)
	for i, f := range fixtures {
		id := uuid.NewString()
		metadata := f.metadata
		if metadata == "" {
			metadata = "{}"
		}
		var compactID any
		if f.compactID != "" {
			compactID = f.compactID
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO bot_history_messages
  (id, bot_id, session_id, role, content, metadata, compact_id, turn_visible, turn_id, turn_position, turn_message_seq, created_at)
VALUES
  ($1, $2, $3, 'user', '[{"type":"text","text":"m"}]', $4, $5, true, $6, $7, 0, now() + make_interval(secs => $8))
`, id, botID, sessionID, metadata, compactID, uuid.NewString(), i, i); err != nil {
			t.Fatalf("insert message %q: %v", f.name, err)
		}
		if f.eligible {
			wantEligible[id] = f.name
		}
	}

	var sessionUUID pgtype.UUID
	if err := sessionUUID.Scan(sessionID); err != nil {
		t.Fatalf("scan session uuid: %v", err)
	}
	rows, err := sqlc.New(tx).ListUncompactedMessagesBySession(ctx, sessionUUID)
	if err != nil {
		t.Fatalf("ListUncompactedMessagesBySession: %v", err)
	}

	got := make(map[string]bool)
	for i, row := range rows {
		id := uuid.UUID(row.ID.Bytes).String()
		got[id] = true
		if i > 0 && row.CreatedAt.Time.Before(rows[i-1].CreatedAt.Time) {
			t.Fatalf("rows not ordered by created_at: %v after %v", row.CreatedAt.Time, rows[i-1].CreatedAt.Time)
		}
	}
	for id, name := range wantEligible {
		if !got[id] {
			t.Errorf("row %q missing from candidate set", name)
		}
	}
	if len(got) != len(wantEligible) {
		t.Errorf("candidate set size = %d, want %d (an excluded row leaked in)", len(got), len(wantEligible))
	}
}
