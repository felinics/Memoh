package compaction

import (
	"cmp"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/chat/checkpoint"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// --- stub summarizer model (intercepts the SDK HTTP call) ---------------------

type stubModel struct {
	summary      string
	finishReason string // defaults to "stop"
	// statuses answer the next calls in turn with an HTTP failure; zero
	// answers normally.
	statuses []int
	calls    int
	prompt   string // decoded text of the captured request messages
}

func (s *stubModel) RoundTrip(req *http.Request) (*http.Response, error) {
	s.calls++
	if len(s.statuses) > 0 {
		status := s.statuses[0]
		s.statuses = s.statuses[1:]
		if status != 0 {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"stub failure"}}`)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
		}
	}
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		s.prompt = decodePromptMessages(body)
	}
	finishReason := s.finishReason
	if finishReason == "" {
		finishReason = "stop"
	}
	// The summarizer streams its answer.
	resp := `data: {"id":"stub","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":` + jsonStr(s.summary) + `},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"stub","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":` + jsonStr(finishReason) + `}],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}` + "\n\ndata: [DONE]\n\n"
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(resp)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}, nil
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func decodePromptMessages(body []byte) string {
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &req)
	var sb strings.Builder
	for _, m := range req.Messages {
		var s string
		if json.Unmarshal(m.Content, &s) == nil {
			sb.WriteString(s)
			sb.WriteByte('\n')
			continue
		}
		var parts []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(m.Content, &parts) == nil {
			for _, p := range parts {
				sb.WriteString(p.Text)
				sb.WriteByte('\n')
			}
		}
	}
	return sb.String()
}

// fakeQueries is one session's history, compaction logs and history
// checkpoints in memory, read and written as the SQL the compaction engine
// and the checkpoint store run reads and writes them. What InTx writes rolls
// back when its callback fails.
type fakeQueries struct {
	dbstore.Queries // embedded interface; unimplemented methods would panic if called

	// history is the session's context rows in history order. A row without
	// a turn position takes the next one when the fake first reads it.
	history     []candidateRow
	logs        map[pgtype.UUID]sqlc.BotHistoryMessageCompact
	checkpoints map[pgtype.UUID]sqlc.BotHistoryCheckpoint
	active      pgtype.UUID
	// folded names the rows the newest checkpoint folds, in history order.
	folded []pgtype.UUID

	listPanic   bool
	listStarted chan struct{}
	listRelease <-chan struct{}
	onComplete  func()
	// rewinds records each rewind before a replaced turn asked for.
	rewinds   []sqlc.RewindHistoryCheckpointBeforeTurnParams
	completed sqlc.CompleteCompactionLogParams
}

func (*fakeQueries) SupportsTransactions() bool { return true }

func (f *fakeQueries) InTx(_ context.Context, fn func(dbstore.Queries) error) error {
	logs, checkpoints, active, folded := maps.Clone(f.logs), maps.Clone(f.checkpoints), f.active, f.folded
	if err := fn(f); err != nil {
		f.logs, f.checkpoints, f.active, f.folded = logs, checkpoints, active, folded
		return err
	}
	return nil
}

func (*fakeQueries) LockBotForSessionWrite(_ context.Context, id pgtype.UUID) (pgtype.UUID, error) {
	return id, nil
}

func (f *fakeQueries) RewindHistoryCheckpointBeforeTurn(_ context.Context, arg sqlc.RewindHistoryCheckpointBeforeTurnParams) (bool, error) {
	f.rewinds = append(f.rewinds, arg)
	return false, nil
}

func (f *fakeQueries) CreateCompactionLog(_ context.Context, arg sqlc.CreateCompactionLogParams) (sqlc.BotHistoryMessageCompact, error) {
	log := sqlc.BotHistoryMessageCompact{ID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, BotID: arg.BotID, SessionID: arg.SessionID, Status: "pending"}
	f.putLog(log)
	return log, nil
}

func (f *fakeQueries) CompleteCompactionLog(_ context.Context, arg sqlc.CompleteCompactionLogParams) (sqlc.BotHistoryMessageCompact, error) {
	if f.onComplete != nil {
		f.onComplete()
	}
	f.completed = arg
	log := f.logs[arg.ID]
	log.Status, log.Summary, log.MessageCount, log.ErrorMessage, log.ModelID = arg.Status, arg.Summary, arg.MessageCount, arg.ErrorMessage, arg.ModelID
	f.putLog(log)
	return log, nil
}

func (f *fakeQueries) CountHistoryCheckpointCandidates(_ context.Context, arg sqlc.CountHistoryCheckpointCandidatesParams) (int64, error) {
	return int64(len(f.candidates(arg.AfterTurnPosition, arg.AfterTurnMessageSeq, arg.KeptIds, arg.BeforeTurnPosition, arg.BeforeTurnMessageSeq))), nil
}

func (f *fakeQueries) ListHistoryRowsBySources(_ context.Context, arg sqlc.ListHistoryRowsBySourcesParams) ([]sqlc.ListHistoryRowsBySourcesRow, error) {
	var rows []sqlc.ListHistoryRowsBySourcesRow
	for _, row := range f.ordered() {
		if slices.Contains(arg.Ids, row.ID) || row.ExternalMessageID.Valid && slices.Contains(arg.ExternalMessageIds, row.ExternalMessageID.String) {
			rows = append(rows, sqlc.ListHistoryRowsBySourcesRow{ID: row.ID, SourceMessageID: row.ExternalMessageID, TurnPosition: row.TurnPosition, TurnMessageSeq: row.TurnMessageSeq})
		}
	}
	return rows, nil
}

func (f *fakeQueries) ListHistoryCheckpointCandidatesWithinBytes(_ context.Context, arg sqlc.ListHistoryCheckpointCandidatesWithinBytesParams) ([]candidateRow, error) {
	if f.listPanic {
		panic("boom: injected query panic")
	}
	if f.listStarted != nil {
		close(f.listStarted)
	}
	if f.listRelease != nil {
		<-f.listRelease
	}
	candidates := f.candidates(arg.AfterTurnPosition, arg.AfterTurnMessageSeq, arg.KeptIds, arg.BeforeTurnPosition, arg.BeforeTurnMessageSeq)
	// A read takes whole call groups, a row other than a tool's and the tool
	// rows after it: every group that ends within the budget, and the group
	// that crosses it when what it reads past the budget fits a row.
	group, ends := make([]int, len(candidates)), map[int]int64{}
	open, total := 0, int64(0)
	for i, row := range candidates {
		if row.Role != "tool" {
			open++
		}
		group[i], total = open, total+payloadBytesForTest(row)
		ends[open] = total
	}
	crossing := -1
	for i := range candidates {
		if ends[group[i]] > arg.MaxBytes {
			crossing = group[i]
			break
		}
	}
	admitted := make([]candidateRow, 0, len(candidates))
	for i, row := range candidates {
		if end := ends[group[i]]; end > arg.MaxBytes && (group[i] != crossing || end-arg.MaxBytes > arg.MaxRowBytes) {
			break
		}
		row.CandidateCount = int64(len(candidates))
		admitted = append(admitted, row)
	}
	return admitted, nil
}

// candidates are the rows a pass reads: the ones the checkpoint it folds
// onto keeps and the ones after its position, before the cutoff.
func (f *fakeQueries) candidates(afterTurn, afterSeq int64, keptIDs []pgtype.UUID, beforeTurn, beforeSeq pgtype.Int8) []candidateRow {
	after := messagepkg.HistoryPosition{Turn: afterTurn, Seq: afterSeq}
	var rows []candidateRow
	for _, row := range f.ordered() {
		position := rowPosition(row)
		if !after.Before(position) && !slices.Contains(keptIDs, row.ID) {
			continue
		}
		if beforeTurn.Valid && !position.Before(messagepkg.HistoryPosition{Turn: beforeTurn.Int64, Seq: beforeSeq.Int64}) {
			continue
		}
		rows = append(rows, row)
	}
	return rows
}

// payloadBytesForTest measures a row the way the candidate reads do.
func payloadBytesForTest(row candidateRow) int64 {
	return int64(len(row.Content))
}

// ordered is the session's context rows in history order, every one placed.
func (f *fakeQueries) ordered() []candidateRow {
	var last int64
	for _, row := range f.history {
		if row.TurnPosition.Valid {
			last = max(last, row.TurnPosition.Int64)
		}
	}
	for i := range f.history {
		if !f.history[i].TurnPosition.Valid {
			last++
			f.history[i].TurnPosition = pgtype.Int8{Int64: last, Valid: true}
			f.history[i].TurnMessageSeq = pgtype.Int8{Int64: 1, Valid: true}
		}
	}
	rows := slices.Clone(f.history)
	slices.SortStableFunc(rows, func(a, b candidateRow) int {
		return cmp.Or(cmp.Compare(a.TurnPosition.Int64, b.TurnPosition.Int64), cmp.Compare(a.TurnMessageSeq.Int64, b.TurnMessageSeq.Int64))
	})
	return rows
}

func (f *fakeQueries) GetActiveHistoryCheckpoint(ctx context.Context, _ pgtype.UUID) (sqlc.BotHistoryCheckpoint, error) {
	return f.GetHistoryCheckpoint(ctx, f.active)
}

func (f *fakeQueries) GetHistoryCheckpoint(_ context.Context, id pgtype.UUID) (sqlc.BotHistoryCheckpoint, error) {
	row, ok := f.checkpoints[id]
	if !ok {
		return sqlc.BotHistoryCheckpoint{}, pgx.ErrNoRows
	}
	return row, nil
}

func (f *fakeQueries) ListHistoryCheckpointSummaries(_ context.Context, arg sqlc.ListHistoryCheckpointSummariesParams) ([]sqlc.ListHistoryCheckpointSummariesRow, error) {
	var rows []sqlc.ListHistoryCheckpointSummariesRow
	for _, id := range arg.Ids {
		if log, ok := f.logs[id]; ok && log.Status == "ok" {
			rows = append(rows, sqlc.ListHistoryCheckpointSummariesRow{ID: id, Summary: log.Summary})
		}
	}
	return rows, nil
}

func (f *fakeQueries) ListHistoryCheckpointSpanRows(_ context.Context, arg sqlc.ListHistoryCheckpointSpanRowsParams) ([]sqlc.ListHistoryCheckpointSpanRowsRow, error) {
	after := messagepkg.HistoryPosition{Turn: arg.AfterTurnPosition, Seq: arg.AfterTurnMessageSeq}
	through := messagepkg.HistoryPosition{Turn: arg.ThroughTurnPosition, Seq: arg.ThroughTurnMessageSeq}
	var rows []sqlc.ListHistoryCheckpointSpanRowsRow
	for _, row := range f.ordered() {
		if position := rowPosition(row); after.Before(position) && !through.Before(position) || slices.Contains(arg.Ids, row.ID) {
			rows = append(rows, sqlc.ListHistoryCheckpointSpanRowsRow{ID: row.ID, TurnPosition: row.TurnPosition, TurnMessageSeq: row.TurnMessageSeq})
		}
	}
	return rows, nil
}

func (f *fakeQueries) CreateHistoryCheckpoint(_ context.Context, arg sqlc.CreateHistoryCheckpointParams) (sqlc.BotHistoryCheckpoint, error) {
	row := sqlc.BotHistoryCheckpoint{
		ID:             pgtype.UUID{Bytes: uuid.New(), Valid: true},
		SessionID:      arg.SessionID,
		ParentID:       arg.ParentID,
		TurnPosition:   arg.TurnPosition,
		TurnMessageSeq: arg.TurnMessageSeq,
		Items:          arg.Items,
		CreatedAt:      pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}
	f.putCheckpoint(row)
	// What the checkpoint folds: the rows after its parent's position through
	// its own, and the rows its parent keeps, that it does not keep itself.
	parent := f.checkpoints[row.ParentID]
	after := messagepkg.HistoryPosition{Turn: parent.TurnPosition, Seq: parent.TurnMessageSeq}
	through := messagepkg.HistoryPosition{Turn: row.TurnPosition, Seq: row.TurnMessageSeq}
	parentItems, items := decodeItemsForTest(parent.Items), decodeItemsForTest(row.Items)
	f.folded = nil
	for _, h := range f.ordered() {
		id, position := formatUUID(h.ID), rowPosition(h)
		inSpan := after.Before(position) && !through.Before(position) || slices.Contains(parentItems, checkpoint.Item{RowID: id})
		if inSpan && !slices.Contains(items, checkpoint.Item{RowID: id}) {
			f.folded = append(f.folded, h.ID)
		}
	}
	return row, nil
}

func (f *fakeQueries) SetHistoryCheckpoint(_ context.Context, arg sqlc.SetHistoryCheckpointParams) (int64, error) {
	if f.active != arg.ExpectedCheckpointID {
		return 0, nil
	}
	f.active = arg.CheckpointID
	return 1, nil
}

func (f *fakeQueries) putLog(log sqlc.BotHistoryMessageCompact) {
	if f.logs == nil {
		f.logs = make(map[pgtype.UUID]sqlc.BotHistoryMessageCompact)
	}
	f.logs[log.ID] = log
}

func (f *fakeQueries) putCheckpoint(row sqlc.BotHistoryCheckpoint) {
	if f.checkpoints == nil {
		f.checkpoints = make(map[pgtype.UUID]sqlc.BotHistoryCheckpoint)
	}
	f.checkpoints[row.ID] = row
}

// hold makes a checkpoint after the active one that holds the summaries, in
// order, at the start of the history the session lists, and makes it the
// session's active one.
func (f *fakeQueries) hold(summaries ...string) sqlc.BotHistoryCheckpoint {
	items := make([]checkpoint.Item, 0, len(summaries))
	for _, summary := range summaries {
		log := sqlc.BotHistoryMessageCompact{ID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, Status: "ok", Summary: summary}
		f.putLog(log)
		items = append(items, checkpoint.Item{SummaryID: formatUUID(log.ID)})
	}
	encoded, _ := json.Marshal(items)
	row := sqlc.BotHistoryCheckpoint{
		ID:        pgtype.UUID{Bytes: uuid.New(), Valid: true},
		ParentID:  f.active,
		Items:     encoded,
		CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}
	f.putCheckpoint(row)
	f.active = row.ID
	return row
}

// held is what the session's active checkpoint holds.
func (f *fakeQueries) held() []checkpoint.Item {
	return decodeItemsForTest(f.checkpoints[f.active].Items)
}

func decodeItemsForTest(raw []byte) []checkpoint.Item {
	var items []checkpoint.Item
	_ = json.Unmarshal(raw, &items)
	return items
}

// summaryIDs names the summaries a checkpoint holds, in order.
func summaryIDs(row sqlc.BotHistoryCheckpoint) []string {
	var ids []string
	for _, item := range decodeItemsForTest(row.Items) {
		if item.SummaryID != "" {
			ids = append(ids, item.SummaryID)
		}
	}
	return ids
}

// --- harness ------------------------------------------------------------------

func newMachineryService(q dbstore.Queries) *Service {
	return NewService(slog.New(slog.DiscardHandler), q)
}

func machineryConfig(stub *stubModel, historyBudgetTokens int) TriggerConfig {
	return TriggerConfig{
		BotID:               uuid.NewString(),
		SessionID:           uuid.NewString(),
		ModelID:             "stub-model",
		ClientType:          "openai-completions",
		APIKey:              "test",
		BaseURL:             "http://stub.invalid",
		HTTPClient:          &http.Client{Transport: stub},
		SummaryWindowTokens: 128000,
		HistoryBudgetTokens: historyBudgetTokens,
	}
}

// currentConfig is machineryConfig for a request whose current input is
// current, an anchor.
func currentConfig(stub *stubModel, historyBudgetTokens int, current candidateRow) TriggerConfig {
	cfg := machineryConfig(stub, historyBudgetTokens)
	cfg.Anchors = historySources(current)
	return cfg
}

func historySources(rows ...candidateRow) []turn.ContextMessageSource {
	sources := make([]turn.ContextMessageSource, 0, len(rows))
	for _, row := range rows {
		sources = append(sources, turn.ContextMessageSource{Kind: "history", ID: row.ID.String()})
	}
	return sources
}

// machineryCorpus returns a deterministic session whose oldest portion contains
// two tool exchanges (one base64-image result, one structured stdout result),
// plus recent text turns. Rows 3
// and 9 take more than the verbatim budget a 4000-token history leaves, so a
// fold that reads up to either keeps none of the rows before it raw.
func machineryCorpus(t *testing.T) []candidateRow {
	t.Helper()
	b64 := strings.Repeat("QUJD", 100) // 400 base64 chars
	long := strings.Repeat(" in detail", 500)
	return []candidateRow{
		mkRow(t, "user", `[{"type":"text","text":"deploy please`+strings.Repeat(", with the usual checks", 20)+`"}]`),                                         // 0
		mkRow(t, "assistant", `[{"type":"text","text":"on it"},{"type":"tool-call","toolCallId":"A","toolName":"screenshot","input":{}}]`),                    // 1 call A
		mkRow(t, "tool", `[{"type":"tool-result","toolCallId":"A","toolName":"screenshot","result":{"mime":"image/png","data":"`+b64+`"}}]`),                  // 2 result A (base64)
		mkRow(t, "assistant", `[{"type":"text","text":"captured the screen`+long+`"}]`),                                                                       // 3
		mkRow(t, "user", `[{"type":"text","text":"now build"}]`),                                                                                              // 4
		mkRow(t, "assistant", `[{"type":"text","text":"running"},{"type":"tool-call","toolCallId":"B","toolName":"exec_command","input":{"cmd":"make"}}]`),    // 5 call B
		mkRow(t, "tool", `[{"type":"tool-result","toolCallId":"B","toolName":"exec_command","result":{"exit_code":0,"stdout":"build ok done","stderr":""}}]`), // 6 result B (structured)
		mkRow(t, "assistant", `[{"type":"text","text":"build finished"}]`),                                                                                    // 7
		mkRow(t, "user", `[{"type":"text","text":"recent question"}]`),                                                                                        // 8
		mkRow(t, "assistant", `[{"type":"text","text":"recent answer`+long+`"}]`),                                                                             // 9
	}
}

// rowPosition is where a row stands in the session's history.
func rowPosition(row candidateRow) messagepkg.HistoryPosition {
	return messagepkg.HistoryPosition{Turn: row.TurnPosition.Int64, Seq: row.TurnMessageSeq.Int64}
}
