package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const listUncompactedMessagesBySessionSQL = `
SELECT
  m.id,
  m.created_at
FROM bot_visible_history_messages m
LEFT JOIN channel_identities ci
  ON ci.id = m.sender_channel_identity_id
 AND ci.team_id = public.memoh_current_team_id()
JOIN bot_sessions s
  ON s.id = m.session_id
 AND s.team_id = public.memoh_current_team_id()
LEFT JOIN bot_channel_routes r
  ON r.id = s.route_id
 AND r.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id()
  AND m.session_id = $1
  AND (m.compact_id IS NULL OR NOT EXISTS (
    SELECT 1 FROM bot_history_message_compacts c
    WHERE c.team_id = public.memoh_current_team_id()
      AND c.id = m.compact_id
      AND c.bot_id = m.bot_id
      AND c.session_id = s.id
      AND c.compaction_epoch = s.compaction_epoch
      AND (
        (c.status = 'ok' AND NULLIF(BTRIM(c.summary, E' \t\n\r\f\x0B'), '') IS NOT NULL)
        OR (c.status = 'pending' AND c.started_at > now() - INTERVAL '15 minutes')
      )
  ))
  AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
ORDER BY m.turn_position ASC, m.turn_message_seq ASC, m.created_at ASC, m.id ASC
`

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
	applyPreTeamBaseline(t, ctx, tx)
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

	rows, err := tx.Query(ctx, listUncompactedMessagesBySessionSQL, sessionID)
	if err != nil {
		t.Fatalf("ListUncompactedMessagesBySession: %v", err)
	}
	defer rows.Close()

	got := make(map[string]bool)
	var prevCreatedAt time.Time
	for i := 0; rows.Next(); i++ {
		var id uuid.UUID
		var createdAt time.Time
		if err := rows.Scan(&id, &createdAt); err != nil {
			t.Fatalf("scan uncompacted row: %v", err)
		}
		got[id.String()] = true
		if i > 0 && createdAt.Before(prevCreatedAt) {
			t.Fatalf("rows not ordered by created_at: %v after %v", createdAt, prevCreatedAt)
		}
		prevCreatedAt = createdAt
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate uncompacted rows: %v", err)
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
