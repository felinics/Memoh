package timeline

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/chat/checkpoint"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

const checkpointTestSessionID = "66666666-6666-6666-6666-666666666666"

// fakeCheckpointQueries serves a session's active checkpoint, which holds,
// and the texts of its summaries.
type fakeCheckpointQueries struct {
	dbstore.Queries
	active     sqlc.BotHistoryCheckpoint
	summaries  map[string]string
	classified []sqlc.ClassifyHistoryCheckpointMessagesRow
	classify   []sqlc.ClassifyHistoryCheckpointMessagesParams
}

func (f *fakeCheckpointQueries) GetActiveHistoryCheckpoint(context.Context, pgtype.UUID) (sqlc.BotHistoryCheckpoint, error) {
	if !f.active.ID.Valid {
		return sqlc.BotHistoryCheckpoint{}, pgx.ErrNoRows
	}
	return f.active, nil
}

func (f *fakeCheckpointQueries) ListHistoryCheckpointSummaries(_ context.Context, arg sqlc.ListHistoryCheckpointSummariesParams) ([]sqlc.ListHistoryCheckpointSummariesRow, error) {
	rows := make([]sqlc.ListHistoryCheckpointSummariesRow, 0, len(arg.Ids))
	for _, id := range arg.Ids {
		if text, ok := f.summaries[id.String()]; ok {
			rows = append(rows, sqlc.ListHistoryCheckpointSummariesRow{ID: id, Summary: text})
		}
	}
	return rows, nil
}

func (f *fakeCheckpointQueries) ClassifyHistoryCheckpointMessages(_ context.Context, arg sqlc.ClassifyHistoryCheckpointMessagesParams) ([]sqlc.ClassifyHistoryCheckpointMessagesRow, error) {
	f.classify = append(f.classify, arg)
	return f.classified, nil
}

type fakeCheckpointRows struct {
	messages []messagepkg.Message
	ids      []string
}

func (f *fakeCheckpointRows) ListTurnResponseSourcesByIDs(_ context.Context, _ string, ids []string) ([]messagepkg.Message, error) {
	f.ids = ids
	return f.messages, nil
}

func mustCheckpointUUID(t *testing.T, id string) pgtype.UUID {
	t.Helper()
	parsed, err := dbpkg.ParseUUID(id)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func checkpointRow(t *testing.T, position messagepkg.HistoryPosition, items ...checkpoint.Item) sqlc.BotHistoryCheckpoint {
	t.Helper()
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	return sqlc.BotHistoryCheckpoint{
		ID:             mustCheckpointUUID(t, "cccccccc-cccc-cccc-cccc-cccccccccccc"),
		SessionID:      mustCheckpointUUID(t, checkpointTestSessionID),
		TurnPosition:   position.Turn,
		TurnMessageSeq: position.Seq,
		Items:          encoded,
	}
}

func storedModelMessage(t *testing.T, role string, content any) []byte {
	t.Helper()
	parts, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	message, err := json.Marshal(turn.ModelMessage{Role: role, Content: parts})
	if err != nil {
		t.Fatal(err)
	}
	return message
}

// Load projects the active checkpoint as composition renders it: a summary
// with its text, a kept input row as the inbound message it records, a kept
// response as its turn response, and how the checkpoint stands for each
// inbound message of the rendered context. A kept user row that records no
// inbound message renders nothing.
func TestCheckpointSourceLoadProjectsTheActiveCheckpoint(t *testing.T) {
	const (
		summaryID  = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa1"
		inputID    = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbb1"
		feedbackID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbb2"
		replyID    = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbb3"
		resultID   = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbb4"
	)
	queries := &fakeCheckpointQueries{
		active: checkpointRow(t, messagepkg.HistoryPosition{Turn: 4, Seq: 3},
			checkpoint.Item{SummaryID: summaryID},
			checkpoint.Item{RowID: inputID},
			checkpoint.Item{RowID: feedbackID},
			checkpoint.Item{RowID: replyID},
			checkpoint.Item{RowID: resultID},
		),
		summaries:  map[string]string{summaryID: "folded work"},
		classified: []sqlc.ClassifyHistoryCheckpointMessagesRow{{ExternalMessageID: "m-input", Kept: true}, {ExternalMessageID: "m-folded"}},
	}
	rows := &fakeCheckpointRows{messages: []messagepkg.Message{
		{ID: inputID, Role: "user", ExternalMessageID: "m-input", Content: storedModelMessage(t, "user", "kept input")},
		{ID: feedbackID, Role: "user", Content: storedModelMessage(t, "user", "internal feedback")},
		{ID: replyID, Role: "assistant", CreatedAt: time.UnixMilli(1500), Content: storedModelMessage(t, "assistant", "kept reply")},
		{ID: resultID, Role: "tool", CreatedAt: time.UnixMilli(1600), Content: storedModelMessage(t, "tool", []map[string]any{{
			"type": "tool-result", "toolCallId": "call-1", "toolName": "read", "output": "file body",
		}})},
	}}
	source := NewCheckpointSource(slog.New(slog.DiscardHandler), queries, rows)
	rc := RenderedContext{textSegment("m-folded", 900, "folded input"), textSegment("m-input", 1000, "kept input"), textSegment("", 1100, "unnamed")}

	held, err := source.Load(context.Background(), checkpointTestSessionID, rc)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if held.Position != (messagepkg.HistoryPosition{Turn: 4, Seq: 3}) {
		t.Fatalf("position %+v, want the checkpoint's", held.Position)
	}
	if want := []string{inputID, feedbackID, replyID, resultID}; !reflect.DeepEqual(rows.ids, want) {
		t.Fatalf("kept rows read = %v, want %v", rows.ids, want)
	}
	if len(held.Items) != 4 {
		t.Fatalf("items = %+v, want the summary, the input and the two responses", held.Items)
	}
	if item := held.Items[0]; item.SummaryID != summaryID || item.Summary != "folded work" {
		t.Fatalf("item 0 = %+v, want the summary with its text", item)
	}
	if item := held.Items[1]; item.MessageID != "m-input" || item.Response != nil {
		t.Fatalf("item 1 = %+v, want the kept input's message", item)
	}
	if item := held.Items[2]; item.Response == nil || item.Response.SourceMessageID != replyID || item.Response.Role != "assistant" || item.Response.Content != "kept reply" {
		t.Fatalf("item 2 = %+v, want the kept reply", item)
	}
	if item := held.Items[3]; item.Response == nil || item.Response.SourceMessageID != resultID || item.Response.Role != "tool" {
		t.Fatalf("item 3 = %+v, want the kept tool result", item)
	}

	if len(queries.classify) != 1 {
		t.Fatalf("classification reads = %d, want 1", len(queries.classify))
	}
	classify := queries.classify[0]
	if classify.AfterTurnPosition != 4 || classify.AfterTurnMessageSeq != 3 || classify.SessionID.String() != checkpointTestSessionID {
		t.Fatalf("classification after %d/%d in %s, want the checkpoint's position in the session", classify.AfterTurnPosition, classify.AfterTurnMessageSeq, classify.SessionID)
	}
	kept := make([]string, 0, len(classify.KeptIds))
	for _, id := range classify.KeptIds {
		kept = append(kept, id.String())
	}
	if !reflect.DeepEqual(kept, rows.ids) {
		t.Fatalf("classification kept ids = %v, want %v", kept, rows.ids)
	}
	if want := []string{"m-folded", "m-input"}; !reflect.DeepEqual(classify.ExternalMessageIds, want) {
		t.Fatalf("classified messages = %v, want %v", classify.ExternalMessageIds, want)
	}
	if want := map[string]CheckpointMessage{"m-input": {Kept: true}, "m-folded": {}}; !reflect.DeepEqual(held.Messages, want) {
		t.Fatalf("messages = %v, want %v", held.Messages, want)
	}

	// A rendered context without inbound messages has nothing to classify.
	held, err = source.Load(context.Background(), checkpointTestSessionID, RenderedContext{textSegment("", 1100, "unnamed")})
	if err != nil || len(held.Items) != 4 || held.Messages != nil || len(queries.classify) != 1 {
		t.Fatalf("held=%+v err=%v classification reads=%d, want the items without a classification", held, err, len(queries.classify))
	}
}

func TestCheckpointSourceLoadWithoutCheckpoint(t *testing.T) {
	queries := &fakeCheckpointQueries{}
	rows := &fakeCheckpointRows{}
	held, err := NewCheckpointSource(slog.New(slog.DiscardHandler), queries, rows).
		Load(context.Background(), checkpointTestSessionID, RenderedContext{textSegment("m1", 1000, "hello")})
	if err != nil || !reflect.DeepEqual(held, HistoryCheckpoint{}) {
		t.Fatalf("held=%+v err=%v, want the zero checkpoint", held, err)
	}
	if rows.ids != nil || len(queries.classify) != 0 {
		t.Fatalf("rows read %v, classification reads %d, want neither", rows.ids, len(queries.classify))
	}
}
