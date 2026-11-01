package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/chat/checkpoint"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

const (
	checkpointTestID        = "00000000-0000-0000-0000-00000000c0a1"
	checkpointTestParentID  = "00000000-0000-0000-0000-00000000c0a2"
	checkpointTestSummaryID = "00000000-0000-0000-0000-00000000c0b1"
	checkpointTestEarlierID = "00000000-0000-0000-0000-00000000c0b2"
	checkpointTestTurnID    = "00000000-0000-0000-0000-00000000c0d1"
)

// historyCheckpointQueries serves a session's history checkpoints from
// memory, every one of them holding: the active one, the one a replacement
// of each turn keeps, the usable summaries and how each inbound message
// stands.
type historyCheckpointQueries struct {
	dbstore.Queries
	checkpoints map[pgtype.UUID]sqlc.BotHistoryCheckpoint
	active      pgtype.UUID
	activeErr   error
	keptBefore  map[pgtype.UUID]pgtype.UUID
	summaries   map[pgtype.UUID]string
	classified  []sqlc.ClassifyHistoryCheckpointMessagesRow
	// replacedTurns are the turns the checkpoint kept before was asked of.
	replacedTurns []pgtype.UUID
}

// add stores a checkpoint of the session.
func (q *historyCheckpointQueries) add(row sqlc.BotHistoryCheckpoint) pgtype.UUID {
	if q.checkpoints == nil {
		q.checkpoints = make(map[pgtype.UUID]sqlc.BotHistoryCheckpoint)
	}
	q.checkpoints[row.ID] = row
	return row.ID
}

// activate stores a checkpoint as the session's active one.
func (q *historyCheckpointQueries) activate(row sqlc.BotHistoryCheckpoint) {
	q.active = q.add(row)
}

// keepBefore stores the checkpoint a replacement of the turn keeps.
func (q *historyCheckpointQueries) keepBefore(t *testing.T, turnID string, row sqlc.BotHistoryCheckpoint) {
	t.Helper()
	if q.keptBefore == nil {
		q.keptBefore = make(map[pgtype.UUID]pgtype.UUID)
	}
	q.keptBefore[mustPGUUID(t, turnID)] = q.add(row)
}

// summary stores a usable summary and names it.
func (q *historyCheckpointQueries) summary(t *testing.T, id, text string) string {
	t.Helper()
	if q.summaries == nil {
		q.summaries = make(map[pgtype.UUID]string)
	}
	q.summaries[mustPGUUID(t, id)] = text
	return id
}

func (q *historyCheckpointQueries) GetActiveHistoryCheckpoint(context.Context, pgtype.UUID) (sqlc.BotHistoryCheckpoint, error) {
	if q.activeErr != nil {
		return sqlc.BotHistoryCheckpoint{}, q.activeErr
	}
	row, ok := q.checkpoints[q.active]
	if !ok {
		return sqlc.BotHistoryCheckpoint{}, pgx.ErrNoRows
	}
	return row, nil
}

func (q *historyCheckpointQueries) GetHistoryCheckpoint(_ context.Context, id pgtype.UUID) (sqlc.BotHistoryCheckpoint, error) {
	row, ok := q.checkpoints[id]
	if !ok {
		return sqlc.BotHistoryCheckpoint{}, pgx.ErrNoRows
	}
	return row, nil
}

func (q *historyCheckpointQueries) GetHistoryCheckpointKeptBeforeTurn(_ context.Context, arg sqlc.GetHistoryCheckpointKeptBeforeTurnParams) (sqlc.GetHistoryCheckpointKeptBeforeTurnRow, error) {
	q.replacedTurns = append(q.replacedTurns, arg.TurnID)
	return sqlc.GetHistoryCheckpointKeptBeforeTurnRow{TurnFound: true, CheckpointID: q.keptBefore[arg.TurnID]}, nil
}

func (q *historyCheckpointQueries) ListHistoryCheckpointSummaries(_ context.Context, arg sqlc.ListHistoryCheckpointSummariesParams) ([]sqlc.ListHistoryCheckpointSummariesRow, error) {
	var rows []sqlc.ListHistoryCheckpointSummariesRow
	for _, id := range arg.Ids {
		if text, ok := q.summaries[id]; ok {
			rows = append(rows, sqlc.ListHistoryCheckpointSummariesRow{ID: id, Summary: text})
		}
	}
	return rows, nil
}

func (q *historyCheckpointQueries) ClassifyHistoryCheckpointMessages(context.Context, sqlc.ClassifyHistoryCheckpointMessagesParams) ([]sqlc.ClassifyHistoryCheckpointMessagesRow, error) {
	return q.classified, nil
}

// historyCheckpointRow is a checkpoint at a position holding items in order.
func historyCheckpointRow(t *testing.T, id string, position messagepkg.HistoryPosition, items ...checkpoint.Item) sqlc.BotHistoryCheckpoint {
	t.Helper()
	if items == nil {
		items = []checkpoint.Item{}
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("encode checkpoint items: %v", err)
	}
	return sqlc.BotHistoryCheckpoint{
		ID:             mustPGUUID(t, id),
		TurnPosition:   position.Turn,
		TurnMessageSeq: position.Seq,
		Items:          encoded,
	}
}

func checkpointTestService(messages messagepkg.Service, queries dbstore.Queries) *Service {
	logger := slog.New(slog.DiscardHandler)
	return &Service{messageService: messages, checkpoints: checkpoint.NewStore(logger, queries), logger: logger}
}

// emptyHistoryCheckpoints serves sessions whose history has not been folded.
func emptyHistoryCheckpoints() *checkpoint.Store {
	return checkpoint.NewStore(slog.New(slog.DiscardHandler), &historyCheckpointQueries{})
}

// A request renders what the session's checkpoint holds, in its order, and
// then the rows after its position however long ago that is: the summary
// stands for the rows it folds and a row it keeps renders where it holds it.
func TestLoadHistoryRecordsRendersTheCheckpointBeforeTheRowsAfterIt(t *testing.T) {
	t.Parallel()

	messages := &memoryQueryMessageService{messages: []messagepkg.Message{
		persistedHistoryMessage(t, "folded-question", "user", "folded question"),
		persistedHistoryMessage(t, "kept-input", "user", "kept input"),
		persistedHistoryMessage(t, "folded-answer", "assistant", "folded answer"),
		persistedHistoryMessage(t, "next-question", "user", "next question"),
		persistedHistoryMessage(t, "next-answer", "assistant", "next answer"),
	}}
	queries := &historyCheckpointQueries{}
	summaryID := queries.summary(t, checkpointTestSummaryID, "condensed history")
	queries.activate(historyCheckpointRow(t, checkpointTestID, historyTestPosition(2),
		checkpoint.Item{RowID: "kept-input"}, checkpoint.Item{SummaryID: summaryID}))
	svc := checkpointTestService(messages, queries)
	scope := compactionSummaryScope(lifecycleTestBotID, "chat-1", lifecycleTestSessionID, "group", "Dev Chat", "target-1")

	records, _, err := svc.loadHistoryRecords(t.Context(), historyfrag.ScopeFallback{}, scope, 1000, historyCut{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"kept input", turn.SummaryText("condensed history"), "next question", "next answer"}
	if got := recordTexts(records); !reflect.DeepEqual(got, want) {
		t.Fatalf("history = %#v, want %#v", got, want)
	}
	if len(messages.windows) != 1 || messages.windows[0].After != historyTestPosition(2) || messages.windows[0].Before != nil ||
		messages.windows[0].MaxBytes != svc.historyLoadMaxBytes(1000) {
		t.Fatalf("history windows = %+v, want one load after the checkpoint within the budget", messages.windows)
	}
	if records[0].DBMessageID != "kept-input" {
		t.Fatalf("kept row renders as %+v", records[0])
	}
	summary := records[1]
	if summary.SourceKind != historyfrag.SourceCompactionLog || summary.Lifecycle != historyfrag.LifecycleActiveSummary ||
		summary.Kind != contextfrag.KindConversationSummary {
		t.Fatalf("summary record source/lifecycle/kind = %s/%s/%s", summary.SourceKind, summary.Lifecycle, summary.Kind)
	}
	if summary.Ref.Namespace != historyfrag.NamespaceCompactionLog || summary.Ref.ID != summaryID || summary.Ref.Durability != contextfrag.RefDurable {
		t.Fatalf("summary ref should be the durable compaction log identity: %#v", summary.Ref)
	}
	// A summary speaks for the conversation, not for whoever sent the first
	// row it folds.
	if !reflect.DeepEqual(summary.Scope, scope) {
		t.Fatalf("summary scope = %#v, want the conversation scope %#v", summary.Scope, scope)
	}
}

// An edit or retry renders the history as its replacement leaves it: the
// checkpoint that does not depend on the replaced turn, then the rows after
// it before the cutoff. A summary that folded the replaced turn does not
// render. Without a cut the request renders the active checkpoint.
func TestPrepareHistoryContextRendersTheCheckpointAReplacementKeeps(t *testing.T) {
	t.Parallel()

	messages := &memoryQueryMessageService{messages: []messagepkg.Message{
		persistedHistoryMessage(t, "old", "user", "old"),
		persistedHistoryMessage(t, "between", "assistant", "between"),
		persistedHistoryMessage(t, "request", "user", "replaced request"),
		persistedHistoryMessage(t, "answer", "assistant", "replaced answer"),
	}}
	queries := &historyCheckpointQueries{}
	queries.keepBefore(t, checkpointTestTurnID, historyCheckpointRow(t, checkpointTestParentID, historyTestPosition(0),
		checkpoint.Item{SummaryID: queries.summary(t, checkpointTestEarlierID, "the summary before the turn")}))
	queries.activate(historyCheckpointRow(t, checkpointTestID, historyTestPosition(3),
		checkpoint.Item{SummaryID: queries.summary(t, checkpointTestSummaryID, "the rolling summary through the turn")}))
	svc := checkpointTestService(messages, queries)
	edit := ChatRequest{
		BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID, HistoryCutoffBeforeMessageID: "request",
		TurnReplacement: &messagepkg.TurnReplacement{OldTurnID: checkpointTestTurnID},
	}

	cut, err := prepareHistoryFor(t.Context(), svc, edit, 100000)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{turn.SummaryText("the summary before the turn"), "between"}; !reflect.DeepEqual(recordTexts(cut.records), want) {
		t.Fatalf("edited history = %#v, want %#v", recordTexts(cut.records), want)
	}
	if len(queries.replacedTurns) != 1 || queries.replacedTurns[0] != mustPGUUID(t, checkpointTestTurnID) {
		t.Fatalf("checkpoint kept before %v, want the replaced turn", queries.replacedTurns)
	}
	if window := messages.windows[0]; window.After != historyTestPosition(0) || window.Before == nil || *window.Before != historyTestPosition(2) {
		t.Fatalf("edited history window = %+v, want after the kept checkpoint and before the request", window)
	}

	live, err := prepareHistoryFor(t.Context(), svc, ChatRequest{BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID}, 100000)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{turn.SummaryText("the rolling summary through the turn")}; !reflect.DeepEqual(recordTexts(live.records), want) {
		t.Fatalf("live history = %#v, want %#v", recordTexts(live.records), want)
	}
}

// A history whose checkpoint does not load is not loaded at all.
func TestPrepareHistoryContextFailsWithoutItsCheckpoint(t *testing.T) {
	t.Parallel()

	messages := &memoryQueryMessageService{messages: []messagepkg.Message{persistedHistoryMessage(t, "question", "user", "question")}}
	loadErr := errors.New("checkpoint unavailable")
	svc := checkpointTestService(messages, &historyCheckpointQueries{activeErr: loadErr})
	if _, err := prepareHistoryFor(t.Context(), svc, ChatRequest{BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID}, 1000); !errors.Is(err, loadErr) {
		t.Fatalf("history error = %v, want checkpoint load failure", err)
	}
	if len(messages.windows) != 0 {
		t.Fatalf("history loaded unbounded: %+v", messages.windows)
	}
}
