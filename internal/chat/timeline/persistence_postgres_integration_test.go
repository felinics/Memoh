package timeline

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/chat/checkpoint"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/dbtest"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// Replay and classification read the session's checkpoint the same way: a
// message whose rows it folds leaves replay unless it changed after its
// rows were written, a message whose rows it leaves raw replays whole, and
// chatter without rows replays.
func TestPostgresReplayAndClassificationFollowTheCheckpoint(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set TEST_POSTGRES_DSN to run the timeline PostgreSQL integration")
	}
	if os.Getenv("TEST_POSTGRES_BOOTSTRAP_SCHEMA") == "1" {
		if err := dbtest.MigratePostgresUp(dsn); err != nil {
			t.Fatalf("migrate: %v", err)
		}
	}
	ctx := context.Background()
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}
	userID, botID, sessionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`WITH u AS (INSERT INTO users (id, username, is_active) VALUES ($1::uuid, $1::text, true) RETURNING id)
		INSERT INTO team_members (user_id, role) SELECT id, 'admin' FROM u`, userID)
	exec(`INSERT INTO bots (id, owner_user_id, name) VALUES ($1::uuid, $2, $1::text)`, botID, userID)
	exec(`INSERT INTO bot_sessions (id, bot_id, channel_type) VALUES ($1, $2, 'local')`, sessionID, botID)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM bots WHERE id = $1`, botID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})

	queries := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))
	events := NewEventStore(nil, queries)
	received := time.Now().Add(-time.Hour).UnixMilli()
	persist := func(event CanonicalEvent) (string, int64) {
		t.Helper()
		id, stamped, err := events.PersistEvent(ctx, botID, sessionID, event)
		if err != nil || id == "" {
			t.Fatalf("persist %T: id %q, %v", event, id, err)
		}
		return id, eventCursorOf(stamped)
	}
	message := func(id string, atMs int64) MessageEvent {
		return MessageEvent{SessionID: sessionID, MessageID: id, ReceivedAtMs: atMs, Content: []ContentNode{{Type: "text", Text: id}}}
	}
	// row writes the context row an inbound message's event produced, at the
	// start of its own turn.
	rows := make(map[string]checkpoint.Row)
	row := func(messageID, eventID string, turn int64) {
		t.Helper()
		id := uuid.NewString()
		exec(`INSERT INTO bot_history_messages
			(id, bot_id, session_id, role, content, source_message_id, event_id, turn_id, turn_position, turn_message_seq, turn_visible, created_at)
			VALUES ($1, $2, $3, 'user', '{"role":"user","content":"m"}'::jsonb, $4, $5, $6, $7, 1, true, clock_timestamp())`,
			id, botID, sessionID, messageID, eventID, uuid.NewString(), turn)
		rows[messageID] = checkpoint.Row{ID: id, Position: messagepkg.HistoryPosition{Turn: turn, Seq: 1}}
	}

	cursors := make(map[string]int64)
	for i, id := range []string{"m1", "m2", "m3", "m4"} {
		eventID, cursor := persist(message(id, received+int64(i)*1000))
		cursors[id] = cursor
		if id != "m4" {
			row(id, eventID, int64(i+1))
		}
	}
	store := checkpoint.NewStore(nil, queries)
	summaryID := uuid.NewString()
	exec(`INSERT INTO bot_history_message_compacts (id, bot_id, session_id, status, summary, completed_at)
		VALUES ($1, $2, $3, 'ok', 'm1 and m2', now())`, summaryID, botID, sessionID)
	if err := queries.InTx(ctx, func(q dbstore.Queries) error {
		_, err := store.Commit(ctx, q, checkpoint.Fold{
			SessionID: sessionID, SummaryID: summaryID, Summary: "m1 and m2",
			Folded: []checkpoint.Row{rows["m1"], rows["m2"]},
			Read:   []string{rows["m1"].ID, rows["m2"].ID, rows["m3"].ID},
		})
		return err
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	persist(EditEvent{SessionID: sessionID, MessageID: "m2", ReceivedAtMs: time.Now().UnixMilli(), Content: []ContentNode{{Type: "text", Text: "m2 edited"}}})

	replayed := func() []string {
		t.Helper()
		loaded, err := events.LoadEventsForReplay(ctx, botID, sessionID)
		if err != nil {
			t.Fatalf("replay: %v", err)
		}
		var got []string
		for _, event := range loaded {
			got = append(got, string(event.Kind())+":"+extractExternalMessageID(event))
		}
		return got
	}
	if got, want := replayed(), []string{"message:m2", "message:m3", "message:m4", "edit:m2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("replay = %v, want %v", got, want)
	}

	source := NewCheckpointSource(nil, queries, messagepkg.NewService(nil, queries))
	rc := RenderedContext{{MessageID: "m1"}, {MessageID: "m2"}, {MessageID: "m3"}, {MessageID: "m4"}}
	held, err := source.Load(ctx, sessionID, rc)
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	want := map[string]CheckpointMessage{
		"m1": {EventCursor: cursors["m1"]},
		"m2": {EventCursor: cursors["m2"]},
		"m3": {Kept: true, EventCursor: cursors["m3"]},
	}
	if !reflect.DeepEqual(held.Messages, want) {
		t.Fatalf("classified = %+v, want %+v", held.Messages, want)
	}
}
