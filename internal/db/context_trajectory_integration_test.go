//go:build integration

package db_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/team"
)

func TestContextTrajectoryAtomicContentAndSessionScope(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT set_config('memoh.team_id', $1, false)", team.DefaultTeamID); err != nil {
		t.Fatal(err)
	}
	const bot = "00000000-0000-0000-0000-00000000c701"
	const session = "00000000-0000-0000-0000-00000000c702"
	const otherSession = "00000000-0000-0000-0000-00000000c703"
	const run = "00000000-0000-0000-0000-00000000c704"
	_, err = conn.Exec(ctx, `
WITH principal AS (
 INSERT INTO users (username, is_active, metadata) VALUES ('trajectory-owner', true, '{}') RETURNING id
), membership AS (
 INSERT INTO team_members (team_id, user_id) SELECT $1, id FROM principal RETURNING user_id
), bot AS (
 INSERT INTO bots (id, team_id, owner_user_id, name, status, metadata)
 SELECT $2, $1, user_id, 'trajectory', 'ready', '{}' FROM membership RETURNING id
)
INSERT INTO bot_sessions (id, bot_id, team_id)
SELECT session.id, bot.id, $1 FROM bot, (VALUES ($3::uuid), ($4::uuid)) AS session(id)
`, team.DefaultTeamID, bot, session, otherSession)
	if err != nil {
		t.Fatal(err)
	}
	queries := sqlc.New(conn)
	pgBot, pgSession, pgRun := mustParseLifecycleUUID(t, bot), mustParseLifecycleUUID(t, session), mustParseLifecycleUUID(t, run)
	input := sqlc.AppendContextTrajectoryEventParams{
		BotID: pgBot, SessionID: pgSession, RunID: pgRun, Sequence: 1,
		CaptureID:     mustParseLifecycleUUID(t, "00000000-0000-0000-0000-00000000c705"),
		ContentHashes: []string{"payload"}, Contents: [][]byte{[]byte("full\x00正文")},
		Event: []byte(`{"stage":"trigger","blocks":[{"chunks":["payload"]}]}`),
	}
	if _, err := queries.AppendContextTrajectoryEvent(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.AppendContextTrajectoryEvent(ctx, input); err != nil {
		t.Fatalf("idempotent capture: %v", err)
	}
	rows, err := queries.ListContextTrajectoryEvents(ctx, sqlc.ListContextTrajectoryEventsParams{
		BotID: pgBot, SessionID: pgSession, RowLimit: 10,
	})
	if err != nil || len(rows) != 1 {
		t.Fatalf("events = %#v, err = %v", rows, err)
	}
	var summary map[string]any
	if err := json.Unmarshal(rows[0].Summary, &summary); err != nil {
		t.Fatal(err)
	}
	if _, ok := summary["blocks"]; ok {
		t.Fatal("list includes unbounded block references")
	}
	contents, err := queries.GetContextTrajectoryEventContents(ctx, sqlc.GetContextTrajectoryEventContentsParams{
		BotID: pgBot, SessionID: pgSession, ID: rows[0].ID,
	})
	if err != nil || len(contents) != 1 || string(contents[0].Content) != "full\x00正文" {
		t.Fatalf("contents = %#v, err = %v", contents, err)
	}
	contents, err = queries.GetContextTrajectoryEventContents(ctx, sqlc.GetContextTrajectoryEventContentsParams{
		BotID: pgBot, SessionID: mustParseLifecycleUUID(t, otherSession), ID: rows[0].ID,
	})
	if err != nil || len(contents) != 0 {
		t.Fatalf("cross-session contents = %#v, err = %v", contents, err)
	}
	input.ContentHashes, input.Contents = []string{"conflict"}, [][]byte{[]byte("conflicting")}
	input.Event = []byte(`{"stage":"changed","blocks":[]}`)
	if _, err := queries.AppendContextTrajectoryEvent(ctx, input); err == nil {
		t.Fatal("existing sequence was silently replaced")
	}
	var orphaned int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM context_trajectory_contents WHERE content_hash = 'conflict'").Scan(&orphaned); err != nil || orphaned != 0 {
		t.Fatalf("rejected event stored orphaned content: %d, %v", orphaned, err)
	}
	input.Sequence = 2
	input.ContentHashes, input.Contents = nil, nil
	input.Event = []byte(`{"stage":"selected","blocks":[]}`)
	if _, err := queries.AppendContextTrajectoryEvent(ctx, input); err != nil {
		t.Fatal(err)
	}
	page, err := queries.ListContextTrajectoryEvents(ctx, sqlc.ListContextTrajectoryEventsParams{BotID: pgBot, SessionID: pgSession, RowLimit: 1})
	if err != nil || len(page) != 1 || page[0].Sequence != 2 {
		t.Fatalf("first page = %#v, %v", page, err)
	}
	page, err = queries.ListContextTrajectoryEvents(ctx, sqlc.ListContextTrajectoryEventsParams{BotID: pgBot, SessionID: pgSession, BeforeID: page[0].ID, RowLimit: 1})
	if err != nil || len(page) != 1 || page[0].Sequence != 1 {
		t.Fatalf("older page = %#v, %v", page, err)
	}
	input.CaptureID = mustParseLifecycleUUID(t, "00000000-0000-0000-0000-00000000c706")
	input.Sequence = 1
	input.Event = []byte(`{"stage":"continuation","blocks":[]}`)
	if _, err := queries.AppendContextTrajectoryEvent(ctx, input); err != nil {
		t.Fatalf("same-run continuation overwrote the previous capture: %v", err)
	}
	input.Sequence = 3
	input.ContentHashes, input.Contents = []string{"rollback"}, [][]byte{[]byte("must roll back")}
	input.Event = []byte(`null`)
	if _, err := queries.AppendContextTrajectoryEvent(ctx, input); err == nil {
		t.Fatal("invalid event was stored")
	}
	var count int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM context_trajectory_contents WHERE content_hash = 'rollback'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("content escaped failed event transaction: %d, %v", count, err)
	}
	if _, err := conn.Exec(ctx, "DELETE FROM bots WHERE id = $1", pgBot); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM context_trajectory_contents").Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleted bot retained content: %d, %v", count, err)
	}
}
