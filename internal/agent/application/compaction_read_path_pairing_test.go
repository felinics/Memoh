package application

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/chat/checkpoint"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// pairingQueries serves a compaction pass over a session's rows and then the
// session's history checkpoints as the pass left them.
type pairingQueries struct {
	*historyCheckpointQueries
	rows  []sqlc.ListHistoryCheckpointCandidatesWithinBytesRow
	logID pgtype.UUID
}

func pairingPosition(row sqlc.ListHistoryCheckpointCandidatesWithinBytesRow) messagepkg.HistoryPosition {
	return messagepkg.HistoryPosition{Turn: row.TurnPosition.Int64, Seq: row.TurnMessageSeq.Int64}
}

func (f *pairingQueries) candidates(after messagepkg.HistoryPosition) []sqlc.ListHistoryCheckpointCandidatesWithinBytesRow {
	var rows []sqlc.ListHistoryCheckpointCandidatesWithinBytesRow
	for _, row := range f.rows {
		if after.Before(pairingPosition(row)) {
			rows = append(rows, row)
		}
	}
	return rows
}

func (f *pairingQueries) CountHistoryCheckpointCandidates(_ context.Context, arg sqlc.CountHistoryCheckpointCandidatesParams) (int64, error) {
	return int64(len(f.candidates(messagepkg.HistoryPosition{Turn: arg.AfterTurnPosition, Seq: arg.AfterTurnMessageSeq}))), nil
}

func (f *pairingQueries) ListHistoryCheckpointCandidatesWithinBytes(_ context.Context, arg sqlc.ListHistoryCheckpointCandidatesWithinBytesParams) ([]sqlc.ListHistoryCheckpointCandidatesWithinBytesRow, error) {
	rows := f.candidates(messagepkg.HistoryPosition{Turn: arg.AfterTurnPosition, Seq: arg.AfterTurnMessageSeq})
	for i := range rows {
		rows[i].CandidateCount = int64(len(rows))
	}
	return rows, nil
}

func (f *pairingQueries) ListHistoryRowsBySources(_ context.Context, arg sqlc.ListHistoryRowsBySourcesParams) ([]sqlc.ListHistoryRowsBySourcesRow, error) {
	var rows []sqlc.ListHistoryRowsBySourcesRow
	for _, row := range f.rows {
		if slices.Contains(arg.Ids, row.ID) {
			rows = append(rows, sqlc.ListHistoryRowsBySourcesRow{ID: row.ID, TurnPosition: row.TurnPosition, TurnMessageSeq: row.TurnMessageSeq})
		}
	}
	return rows, nil
}

func (f *pairingQueries) CreateCompactionLog(context.Context, sqlc.CreateCompactionLogParams) (sqlc.BotHistoryMessageCompact, error) {
	return sqlc.BotHistoryMessageCompact{ID: f.logID}, nil
}

func (f *pairingQueries) ListHistoryCheckpointSpanRows(_ context.Context, arg sqlc.ListHistoryCheckpointSpanRowsParams) ([]sqlc.ListHistoryCheckpointSpanRowsRow, error) {
	after := messagepkg.HistoryPosition{Turn: arg.AfterTurnPosition, Seq: arg.AfterTurnMessageSeq}
	through := messagepkg.HistoryPosition{Turn: arg.ThroughTurnPosition, Seq: arg.ThroughTurnMessageSeq}
	var span []sqlc.ListHistoryCheckpointSpanRowsRow
	for _, row := range f.rows {
		at := pairingPosition(row)
		if after.Before(at) && !through.Before(at) || slices.Contains(arg.Ids, row.ID) {
			span = append(span, sqlc.ListHistoryCheckpointSpanRowsRow{ID: row.ID, TurnPosition: row.TurnPosition, TurnMessageSeq: row.TurnMessageSeq})
		}
	}
	return span, nil
}

func (f *pairingQueries) CreateHistoryCheckpoint(_ context.Context, arg sqlc.CreateHistoryCheckpointParams) (sqlc.BotHistoryCheckpoint, error) {
	row := sqlc.BotHistoryCheckpoint{
		ID:             pgtype.UUID{Bytes: uuid.New(), Valid: true},
		SessionID:      arg.SessionID,
		ParentID:       arg.ParentID,
		TurnPosition:   arg.TurnPosition,
		TurnMessageSeq: arg.TurnMessageSeq,
		Items:          arg.Items,
	}
	f.add(row)
	return row, nil
}

func (f *pairingQueries) SetHistoryCheckpoint(_ context.Context, arg sqlc.SetHistoryCheckpointParams) (int64, error) {
	if arg.ExpectedCheckpointID != f.active {
		return 0, nil
	}
	f.active = arg.CheckpointID
	return 1, nil
}

func (f *pairingQueries) CompleteCompactionLog(_ context.Context, arg sqlc.CompleteCompactionLogParams) (sqlc.BotHistoryMessageCompact, error) {
	if arg.Status == "ok" {
		f.summaries[arg.ID] = arg.Summary
	}
	return sqlc.BotHistoryMessageCompact{ID: arg.ID, Status: arg.Status, Summary: arg.Summary}, nil
}

type pairingSummarizer struct{ summary string }

func (s pairingSummarizer) RoundTrip(*http.Request) (*http.Response, error) {
	body := `data: {"id":"stub","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"` + s.summary + `"},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"stub","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}, nil
}

// pairingRow is the i-th row of the session, filed where the history read
// files it (historyTestPosition).
func pairingRow(i int, role, content string) sqlc.ListHistoryCheckpointCandidatesWithinBytesRow {
	position := historyTestPosition(i)
	return sqlc.ListHistoryCheckpointCandidatesWithinBytesRow{
		ID:             pgtype.UUID{Bytes: uuid.New(), Valid: true},
		Role:           role,
		Content:        []byte(content),
		TurnPosition:   pgtype.Int8{Int64: position.Turn, Valid: true},
		TurnMessageSeq: pgtype.Int8{Int64: position.Seq, Valid: true},
	}
}

// TestSelectorToReadPathFoldsAClosedAskUserCall drives a compaction from rows
// over a history whose ask_user call the conversation moved on from without
// an answer, commits its checkpoint, and reads the history back through it.
// It pins the pair of invariants that live in different packages: the fold
// takes the closed call with the history around it as one contiguous run,
// and the read path renders the summary in that run's place, ahead of the
// current input it never covers.
func TestSelectorToReadPathFoldsAClosedAskUserCall(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListHistoryCheckpointCandidatesWithinBytesRow{
		pairingRow(0, "user", `"old q with plenty of extra descriptive words`+strings.Repeat(" so the raw exchange clearly outweighs its summary", 4)+`"`),
		pairingRow(1, "assistant", `"old a walking through the answer`+strings.Repeat(" in enough detail that compaction genuinely shrinks it", 4)+`"`),
		pairingRow(2, "assistant", `[{"type":"tool-call","toolCallId":"ask-1","toolName":"ask_user","input":{"questions":[]}}]`),
		pairingRow(3, "user", `"mid q`+strings.Repeat(" with the context the history around it needs", 60)+`"`),
		pairingRow(4, "user", `"current q"`),
	}
	q := &pairingQueries{
		historyCheckpointQueries: &historyCheckpointQueries{summaries: map[pgtype.UUID]string{}},
		rows:                     rows,
		logID:                    pgtype.UUID{Bytes: uuid.New(), Valid: true},
	}
	logger := slog.New(slog.DiscardHandler)
	sessionID := uuid.NewString()

	passes, err := compaction.NewService(logger, q).Compact(context.Background(), compaction.TriggerConfig{
		BotID:               uuid.NewString(),
		SessionID:           sessionID,
		ModelID:             "stub-model",
		ClientType:          "openai-completions",
		APIKey:              "test",
		BaseURL:             "http://stub.invalid",
		HTTPClient:          &http.Client{Transport: pairingSummarizer{summary: "condensed old exchange"}},
		Anchors:             []turn.ContextMessageSource{{Kind: "history", ID: uuid.UUID(rows[4].ID.Bytes).String(), Current: true}},
		SummaryWindowTokens: 128000,
		HistoryBudgetTokens: 1000,
	})
	if err != nil || len(passes) != 1 || passes[0].More {
		t.Fatalf("passes = %+v err = %v, want one pass that folds the history before the current input", passes, err)
	}

	committed := q.checkpoints[q.active]
	var items []checkpoint.Item
	if err := json.Unmarshal(committed.Items, &items); err != nil {
		t.Fatal(err)
	}
	compactID := uuid.UUID(q.logID.Bytes).String()
	if position := (messagepkg.HistoryPosition{Turn: committed.TurnPosition, Seq: committed.TurnMessageSeq}); position != pairingPosition(rows[3]) ||
		!reflect.DeepEqual(items, []checkpoint.Item{{SummaryID: compactID}}) {
		t.Fatalf("checkpoint at %+v holds %+v, want the summary of the contiguous run [old q, old a, ask_user call, mid q]", position, items)
	}

	messages := make([]messagepkg.Message, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, messagepkg.Message{ID: uuid.UUID(row.ID.Bytes).String(), SessionID: sessionID, Role: row.Role, Content: row.Content})
	}
	svc := &Service{
		messageService: &memoryQueryMessageService{messages: messages},
		checkpoints:    checkpoint.NewStore(logger, q),
		logger:         logger,
	}
	got, _, err := svc.loadHistoryRecords(context.Background(), historyfrag.ScopeFallback{}, contextfrag.Scope{SessionID: sessionID}, 0, historyCut{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{compactID, messages[4].ID}
	if gotIDs := recordSequenceIDs(got); !reflect.DeepEqual(gotIDs, want) {
		t.Fatalf("selector output broke read-path ordering:\ngot  %v\nwant %v", gotIDs, want)
	}
	if text := got[0].ModelMessage.TextContent(); text != turn.SummaryText(passes[0].Summary) {
		t.Fatalf("summary renders as %q", text)
	}
}

func recordSequenceIDs(records []historyfrag.HistoryRecord) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.Ref.ID)
	}
	return ids
}
