package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// --- stub summarizer model (intercepts the SDK HTTP call) ---------------------

type stubModel struct {
	summary      string
	finishReason string // defaults to "stop"
	calls        int
	prompt       string // decoded text of the captured request messages
	maxTokens    int    // captured max_tokens of the last request
}

func (s *stubModel) RoundTrip(req *http.Request) (*http.Response, error) {
	s.calls++
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		s.prompt = decodePromptMessages(body)
		var limits struct {
			MaxTokens           int `json:"max_tokens"`
			MaxCompletionTokens int `json:"max_completion_tokens"`
		}
		_ = json.Unmarshal(body, &limits)
		s.maxTokens = max(limits.MaxTokens, limits.MaxCompletionTokens)
	}
	finishReason := s.finishReason
	if finishReason == "" {
		finishReason = "stop"
	}
	resp := `{"id":"stub","object":"chat.completion","created":0,"model":"stub",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":` + jsonStr(s.summary) + `},"finish_reason":` + jsonStr(finishReason) + `}],` +
		`"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(resp)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
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

type fakeQueries struct {
	dbstore.Queries  // embedded interface; unimplemented methods would panic if called
	uncompacted      []sqlc.ListUncompactedMessagesBySessionRow
	priorLogs        []sqlc.BotHistoryMessageCompact
	absorbedRows     map[pgtype.UUID][]sqlc.ListMessagesByCompactIDRow
	absorbedRowErrs  map[pgtype.UUID]error
	completeErr      error
	rollupErr        error
	listPanic        bool
	listStarted      chan struct{}
	listRelease      <-chan struct{}
	onComplete       func()
	markedRowCount   *int64
	createdLogID     pgtype.UUID
	claims           map[pgtype.UUID]pgtype.UUID
	logStatuses      map[pgtype.UUID]string
	parentSuccessors map[pgtype.UUID]pgtype.UUID

	created         bool
	createArg       sqlc.CreateCompactionLogParams
	createErr       error
	markedIDs       []pgtype.UUID
	markArg         sqlc.MarkMessagesCompactedParams
	queryCalls      []string
	completed       sqlc.CompleteCompactionLogParams
	completeCalls   []sqlc.CompleteCompactionLogParams
	completeErrors  []error
	rollupCompleted sqlc.CompleteCompactionRollupParams
	rollupCalls     []sqlc.CompleteCompactionRollupParams
}

func (f *fakeQueries) CreateCompactionLog(_ context.Context, arg sqlc.CreateCompactionLogParams) (sqlc.BotHistoryMessageCompact, error) {
	f.created = true
	f.createArg = arg
	if f.createErr != nil {
		return sqlc.BotHistoryMessageCompact{}, f.createErr
	}
	logID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	f.createdLogID = logID
	if f.logStatuses == nil {
		f.logStatuses = make(map[pgtype.UUID]string)
	}
	f.logStatuses[logID] = "pending"
	return sqlc.BotHistoryMessageCompact{ID: logID}, nil
}

func (f *fakeQueries) ListUncompactedMessagesBySession(_ context.Context, _ pgtype.UUID) ([]sqlc.ListUncompactedMessagesBySessionRow, error) {
	if f.listPanic {
		panic("boom: injected query panic")
	}
	if f.listStarted != nil {
		close(f.listStarted)
	}
	if f.listRelease != nil {
		<-f.listRelease
	}
	return f.uncompacted, nil
}

func (f *fakeQueries) ListMessageAssetsBatch(_ context.Context, _ []pgtype.UUID) ([]sqlc.ListMessageAssetsBatchRow, error) {
	f.queryCalls = append(f.queryCalls, "assets")
	return nil, nil
}

func (f *fakeQueries) ListCompactionArtifactLineageBySession(_ context.Context, _ pgtype.UUID) ([]sqlc.BotHistoryMessageCompact, error) {
	return f.priorLogs, nil
}

func (f *fakeQueries) ListMessagesByCompactID(_ context.Context, compactID pgtype.UUID) ([]sqlc.ListMessagesByCompactIDRow, error) {
	if err := f.absorbedRowErrs[compactID]; err != nil {
		return nil, err
	}
	return f.absorbedRows[compactID], nil
}

func (f *fakeQueries) MarkMessagesCompacted(_ context.Context, arg sqlc.MarkMessagesCompactedParams) (int64, error) {
	f.queryCalls = append(f.queryCalls, "mark")
	f.markedIDs = append([]pgtype.UUID(nil), arg.MessageIds...)
	f.markArg = arg
	f.markArg.MessageIds = append([]pgtype.UUID(nil), arg.MessageIds...)
	f.markArg.ExpectedCompactIds = append([]pgtype.UUID(nil), arg.ExpectedCompactIds...)
	if f.claims == nil {
		f.claims = make(map[pgtype.UUID]pgtype.UUID)
	}
	for _, id := range arg.MessageIds {
		f.claims[id] = arg.CompactID
	}
	if f.markedRowCount != nil {
		return *f.markedRowCount, nil
	}
	return int64(len(arg.MessageIds)), nil
}

func (f *fakeQueries) CompleteCompactionLog(_ context.Context, arg sqlc.CompleteCompactionLogParams) (sqlc.BotHistoryMessageCompact, error) {
	f.completeCalls = append(f.completeCalls, arg)
	if f.onComplete != nil {
		f.onComplete()
	}
	if len(f.completeErrors) > 0 {
		err := f.completeErrors[0]
		f.completeErrors = f.completeErrors[1:]
		if err != nil {
			return sqlc.BotHistoryMessageCompact{}, err
		}
	}
	if f.completeErr != nil {
		return sqlc.BotHistoryMessageCompact{}, f.completeErr
	}
	f.completed = arg
	if f.logStatuses == nil {
		f.logStatuses = make(map[pgtype.UUID]string)
	}
	f.logStatuses[arg.ID] = arg.Status
	return sqlc.BotHistoryMessageCompact{ID: arg.ID, Status: arg.Status, Summary: arg.Summary}, nil
}

func (f *fakeQueries) CompleteCompactionRollup(_ context.Context, arg sqlc.CompleteCompactionRollupParams) (sqlc.BotHistoryMessageCompact, error) {
	f.rollupCalls = append(f.rollupCalls, arg)
	if f.rollupErr != nil {
		return sqlc.BotHistoryMessageCompact{}, f.rollupErr
	}
	f.rollupCompleted = arg
	if f.logStatuses == nil {
		f.logStatuses = make(map[pgtype.UUID]string)
	}
	f.logStatuses[arg.ID] = arg.Status
	if f.parentSuccessors == nil {
		f.parentSuccessors = make(map[pgtype.UUID]pgtype.UUID)
	}
	for _, parentID := range arg.Parents {
		f.parentSuccessors[parentID] = arg.ID
	}
	return sqlc.BotHistoryMessageCompact{ID: arg.ID, Status: arg.Status, Summary: arg.Summary}, nil
}

// --- harness ------------------------------------------------------------------

func newMachineryService(q dbstore.Queries) *Service {
	return NewService(slog.New(slog.DiscardHandler), q)
}

func machineryConfig(stub *stubModel, targetTokens int) TriggerConfig {
	return TriggerConfig{
		BotID:        uuid.NewString(),
		SessionID:    uuid.NewString(),
		ModelID:      "stub-model",
		ClientType:   "openai-completions",
		APIKey:       "test",
		BaseURL:      "http://stub.invalid",
		HTTPClient:   &http.Client{Transport: stub},
		TargetTokens: targetTokens,
	}
}

func idSet(ids []pgtype.UUID) map[pgtype.UUID]bool {
	m := make(map[pgtype.UUID]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// machineryCorpus returns a deterministic session whose oldest portion contains
// two tool exchanges (one base64-image result, one structured stdout result),
// plus recent text turns. Indices are returned for precise assertions.
func machineryCorpus(t *testing.T) []sqlc.ListUncompactedMessagesBySessionRow {
	t.Helper()
	b64 := strings.Repeat("QUJD", 100) // 400 base64 chars
	return []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "user", `[{"type":"text","text":"deploy please"}]`, 100),                                                                                          // 0
		mkRow(t, "assistant", `[{"type":"text","text":"on it"},{"type":"tool-call","toolCallId":"A","toolName":"screenshot","input":{}}]`, 100),                    // 1 call A
		mkRow(t, "tool", `[{"type":"tool-result","toolCallId":"A","toolName":"screenshot","result":{"mime":"image/png","data":"`+b64+`"}}]`, 100),                  // 2 result A (base64)
		mkRow(t, "assistant", `[{"type":"text","text":"captured the screen"}]`, 100),                                                                               // 3
		mkRow(t, "user", `[{"type":"text","text":"now build"}]`, 100),                                                                                              // 4
		mkRow(t, "assistant", `[{"type":"text","text":"running"},{"type":"tool-call","toolCallId":"B","toolName":"exec_command","input":{"cmd":"make"}}]`, 100),    // 5 call B
		mkRow(t, "tool", `[{"type":"tool-result","toolCallId":"B","toolName":"exec_command","result":{"exit_code":0,"stdout":"build ok done","stderr":""}}]`, 100), // 6 result B (structured)
		mkRow(t, "assistant", `[{"type":"text","text":"build finished"}]`, 100),                                                                                    // 7
		mkRow(t, "user", `[{"type":"text","text":"recent question"}]`, 100),                                                                                        // 8
		mkRow(t, "assistant", `[{"type":"text","text":"recent answer"}]`, 100),                                                                                     // 9
	}
}

// --- tests --------------------------------------------------------------------

func TestDoCompactionMarksToolAwareWindowAndRendersCleanPrompt(t *testing.T) {
	rows := machineryCorpus(t)
	q := &fakeQueries{uncompacted: rows}
	stub := &stubModel{summary: "SUMMARY-OK"}
	svc := newMachineryService(q)

	// 10 msgs x 100 tokens. target 450: the naive cut would land at index 6
	// (compact 0..5, keep 6..9) — orphaning tool result B at the keep-side head.
	// The boundary guard must advance to 7, pulling result B in with its call, so
	// the marked set is exactly 0..6. If adjustForToolBoundary regressed, this
	// marks only 0..5 and the assertions below fail.
	if _, err := svc.RunCompactionSync(context.Background(), machineryConfig(stub, 450)); err != nil {
		t.Fatalf("RunCompactionSync: %v", err)
	}

	if stub.calls != 1 {
		t.Fatalf("summarizer called %d times, want 1", stub.calls)
	}
	if len(q.markedIDs) != 7 {
		t.Fatalf("marked %d messages, want 7 (0..6, tool exchange kept whole)", len(q.markedIDs))
	}
	marked := idSet(q.markedIDs)
	for i := 0; i <= 6; i++ {
		if !marked[rows[i].ID] {
			t.Fatalf("row %d should be marked compacted", i)
		}
	}
	for i := 7; i <= 9; i++ {
		if marked[rows[i].ID] {
			t.Fatalf("row %d (recent) should NOT be marked", i)
		}
	}
	if !marked[rows[6].ID] {
		t.Fatalf("tool result B must be pulled into the compact set with its call")
	}
	if q.completed.Status != "ok" || q.completed.Summary != "SUMMARY-OK" || q.completed.MessageCount != 7 {
		t.Fatalf("complete log = status=%q summary=%q count=%d", q.completed.Status, q.completed.Summary, q.completed.MessageCount)
	}

	// The summarizer prompt must carry clean rendered outcomes, no media, no noise.
	if !strings.Contains(stub.prompt, "build ok done") {
		t.Fatalf("structured tool outcome missing from prompt:\n%s", stub.prompt)
	}
	if !strings.Contains(stub.prompt, `"cmd":"make"`) {
		t.Fatalf("tool call arguments missing from prompt:\n%s", stub.prompt)
	}
	if !strings.Contains(stub.prompt, "[media]") || strings.Contains(stub.prompt, "QUJDQUJDQUJD") {
		t.Fatalf("base64 image result not scrubbed in prompt")
	}
	if strings.Contains(stub.prompt, `{"type":"text"`) || strings.Contains(stub.prompt, `"toolCallId"`) {
		t.Fatalf("raw JSON-envelope noise leaked into prompt:\n%s", stub.prompt)
	}
}

func TestDoCompactionSkipsWhitespaceOnlyPriorSummaries(t *testing.T) {
	rows := machineryCorpus(t)
	stub := &stubModel{summary: "S3"}
	cfg := machineryConfig(stub, 450)
	q := &fakeQueries{
		uncompacted: rows,
		priorLogs: []sqlc.BotHistoryMessageCompact{{
			ID:        pgtype.UUID{Bytes: uuid.New(), Valid: true},
			BotID:     pgtype.UUID{Bytes: uuid.MustParse(cfg.BotID), Valid: true},
			SessionID: pgtype.UUID{Bytes: uuid.MustParse(cfg.SessionID), Valid: true},
			Summary:   "  \n\t",
			Status:    "ok",
		}},
	}
	svc := newMachineryService(q)

	if _, err := svc.RunCompactionSync(context.Background(), cfg); err != nil {
		t.Fatalf("RunCompactionSync: %v", err)
	}
	if strings.Contains(stub.prompt, "The following are summaries of earlier parts") {
		t.Fatalf("whitespace-only prior summary injected as prior context:\n%s", stub.prompt)
	}
}

// TestDoCompactionFailsClosedWhenEntryFloorsExceedBudget drives one
// unsplittable tool exchange with more minimal entries than MaxCompactTokens
// can hold: sending it anyway would overflow the summarizer window, so the
// attempt must fail closed with zero claims and zero provider calls.
func TestDoCompactionFailsClosedWhenEntryFloorsExceedBudget(t *testing.T) {
	const fanout = 40
	callParts := make([]string, 0, fanout)
	for i := 0; i < fanout; i++ {
		callParts = append(callParts, fmt.Sprintf(`{"type":"tool-call","toolCallId":"c%d","toolName":"probe","input":{}}`, i))
	}
	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "assistant", "["+strings.Join(callParts, ",")+"]", 100),
	}
	for i := 0; i < fanout; i++ {
		rows = append(rows, mkRow(t, "tool", fmt.Sprintf(`[{"type":"tool-result","toolCallId":"c%d","toolName":"probe","output":{"type":"text","value":"ok"}}]`, i), 100))
	}
	rows = append(rows, mkRow(t, "user", `[{"type":"text","text":"recent question"}]`, 100))

	q := &fakeQueries{uncompacted: rows}
	stub := &stubModel{summary: "SUMMARY-OK"}
	svc := newMachineryService(q)

	cfg := machineryConfig(stub, 100)
	cfg.MaxCompactTokens = 40

	_, err := svc.RunCompactionSync(context.Background(), cfg)
	if !errors.Is(err, errCompactionInputOverflow) {
		t.Fatalf("RunCompactionSync error = %v, want errCompactionInputOverflow", err)
	}
	if stub.calls != 0 {
		t.Fatalf("summarizer calls = %d, want 0: an oversized selection must not reach the provider", stub.calls)
	}
	if q.created {
		t.Fatal("attempt row was created: fail closed before claiming sources")
	}
	if len(q.markedIDs) != 0 {
		t.Fatalf("marked rows = %v, want none", q.markedIDs)
	}
}

func TestDoCompactionAllEmptyWindowSkipsModelAndMarking(t *testing.T) {
	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "assistant", `[{"type":"reasoning","text":"thinking a"}]`, 100),
		mkRow(t, "assistant", `[{"type":"reasoning","text":"thinking b"}]`, 100),
		mkRow(t, "assistant", `[{"type":"text","text":"recent kept"}]`, 100),
	}
	q := &fakeQueries{uncompacted: rows}
	stub := &stubModel{summary: "unused"}
	svc := newMachineryService(q)

	if _, err := svc.RunCompactionSync(context.Background(), machineryConfig(stub, 150)); err != nil {
		t.Fatalf("RunCompactionSync: %v", err)
	}
	if stub.calls != 0 {
		t.Fatalf("summarizer should not be called for an all-empty window (calls=%d)", stub.calls)
	}
	if len(q.markedIDs) != 0 {
		t.Fatalf("nothing should be marked for an all-empty window (marked=%d)", len(q.markedIDs))
	}
	if q.created {
		t.Fatal("a no-op compaction must not create a log row")
	}
}

func TestDoCompactionIncompleteToolExchangeSkipsModelAndMarking(t *testing.T) {
	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		toolCallRow(t, 100),
		mkRow(t, "tool", `[]`, 100),
		mkRow(t, "assistant", `[{"type":"text","text":"recent kept"}]`, 100),
	}
	q := &fakeQueries{uncompacted: rows}
	stub := &stubModel{summary: "unused"}
	svc := newMachineryService(q)

	if _, err := svc.RunCompactionSync(context.Background(), machineryConfig(stub, 150)); err != nil {
		t.Fatalf("RunCompactionSync: %v", err)
	}
	if stub.calls != 0 {
		t.Fatalf("summarizer should not be called when no row can be marked (calls=%d)", stub.calls)
	}
	if q.created {
		t.Fatal("a no-op compaction must not create a log row")
	}
	if len(q.markedIDs) != 0 {
		t.Fatalf("nothing should be marked for an incomplete tool exchange (marked=%d)", len(q.markedIDs))
	}
}

func TestDoCompactionMarksOnlyContiguousRunAcrossEmptyMiddleRow(t *testing.T) {
	// Window: an empty-rendering reasoning row sits between two rendered rows.
	// Marking both rendered rows under one compact_id would leave the raw
	// reasoning row between them and let the read path fold history out of
	// order. doCompaction must mark only the first contiguous run (row 0) and
	// leave row 2 for a later pass.
	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "user", `[{"type":"text","text":"old question about a long-running project with plenty of detail to summarize"}]`, 100), // 0
		mkRow(t, "assistant", `[{"type":"reasoning","text":"thinking"}]`, 100),                                                           // 1 renders empty
		mkRow(t, "assistant", `[{"type":"text","text":"old answer covering the whole project state in enough words to compress"}]`, 100), // 2
		mkRow(t, "user", `[{"type":"text","text":"recent question"}]`, 100),                                                              // 3 kept
		mkRow(t, "assistant", `[{"type":"text","text":"recent answer"}]`, 100),                                                           // 4 kept
	}
	q := &fakeQueries{uncompacted: rows}
	stub := &stubModel{summary: "SUMMARY"}
	svc := newMachineryService(q)

	if _, err := svc.RunCompactionSync(context.Background(), machineryConfig(stub, 250)); err != nil {
		t.Fatalf("RunCompactionSync: %v", err)
	}
	if len(q.markedIDs) != 1 || q.markedIDs[0] != rows[0].ID {
		t.Fatalf("marked = %d ids, want only the contiguous leading run [row 0]", len(q.markedIDs))
	}
	if q.completed.Status != "ok" || q.completed.MessageCount != 1 {
		t.Fatalf("complete log = status=%q count=%d, want ok/1", q.completed.Status, q.completed.MessageCount)
	}
}

func TestDoCompactionEmptyHistoryNoOp(t *testing.T) {
	q := &fakeQueries{}
	stub := &stubModel{summary: "unused"}
	svc := newMachineryService(q)

	if _, err := svc.RunCompactionSync(context.Background(), machineryConfig(stub, 100)); err != nil {
		t.Fatalf("RunCompactionSync: %v", err)
	}
	if stub.calls != 0 || len(q.markedIDs) != 0 {
		t.Fatalf("empty history must be a no-op (calls=%d marked=%d)", stub.calls, len(q.markedIDs))
	}
	if q.created {
		t.Fatal("empty history must not create a log row")
	}
}

type failingModel struct{ calls int }

func (f *failingModel) RoundTrip(*http.Request) (*http.Response, error) {
	f.calls++
	return &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"boom"}}`)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

func TestDoCompactionSummarizerFailureRecordsErrorWithReclaimableClaims(t *testing.T) {
	rows := machineryCorpus(t)
	q := &fakeQueries{uncompacted: rows}
	svc := newMachineryService(q)

	cfg := machineryConfig(&stubModel{}, 450)
	cfg.HTTPClient = &http.Client{Transport: &failingModel{}}

	if _, err := svc.RunCompactionSync(context.Background(), cfg); err == nil {
		t.Fatal("summarizer failure must surface an error")
	}
	if len(q.markedIDs) == 0 {
		t.Fatal("a real attempt must claim its selected sources before summarization")
	}
	if !q.created || q.completed.Status != "error" {
		t.Fatalf("a failed attempt must leave an error log row (created=%v status=%q)", q.created, q.completed.Status)
	}
}

func TestDoCompactionEmptySummaryRecordsErrorWithReclaimableClaims(t *testing.T) {
	rows := machineryCorpus(t)
	q := &fakeQueries{uncompacted: rows}
	stub := &stubModel{summary: "   "}
	svc := newMachineryService(q)

	if _, err := svc.RunCompactionSync(context.Background(), machineryConfig(stub, 450)); err == nil {
		t.Fatal("an empty summary must surface an error")
	}
	if len(q.markedIDs) == 0 {
		t.Fatal("a real attempt must claim its selected sources before summarization")
	}
	if !q.created || q.completed.Status != "error" {
		t.Fatalf("an empty summary must leave an error log row (created=%v status=%q)", q.created, q.completed.Status)
	}
}
