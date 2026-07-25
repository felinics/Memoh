package compaction

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// --- stub summarizer model (intercepts the SDK HTTP call) ---------------------

type stubModel struct {
	summary string
	calls   int
	prompt  string // decoded text of the captured request messages
}

func (s *stubModel) RoundTrip(req *http.Request) (*http.Response, error) {
	s.calls++
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		s.prompt = decodePromptMessages(body)
	}
	resp := `{"id":"stub","object":"chat.completion","created":0,"model":"stub",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":` + jsonStr(s.summary) + `},"finish_reason":"stop"}],` +
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
	uncompacted    []CandidateRecord
	priorLogs      []ArtifactRecord
	completeErr    error
	listPanic      bool
	listStarted    chan struct{}
	listRelease    <-chan struct{}
	onComplete     func()
	markedRowCount *int64

	created        bool
	createArg      CreateLogInput
	createErr      error
	markedIDs      []string
	markArg        ClaimCandidatesInput
	queryCalls     []string
	completed      CompleteLogInput
	completeCalls  []CompleteLogInput
	completeErrors []error
}

func (f *fakeQueries) CreateLog(_ context.Context, arg CreateLogInput) (string, error) {
	f.created = true
	f.createArg = arg
	if f.createErr != nil {
		return "", f.createErr
	}
	return uuid.NewString(), nil
}

func (f *fakeQueries) ListCandidates(_ context.Context, _ string) ([]CandidateRecord, error) {
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

func (f *fakeQueries) ListAssets(_ context.Context, _ []string) ([]AssetRecord, error) {
	f.queryCalls = append(f.queryCalls, "assets")
	return nil, nil
}

func (f *fakeQueries) ListArtifactsBySession(_ context.Context, _ string) ([]ArtifactRecord, error) {
	return f.priorLogs, nil
}

func (f *fakeQueries) GetArtifact(_ context.Context, id string) (ArtifactRecord, error) {
	for _, artifact := range f.priorLogs {
		if artifact.ID == id {
			return artifact, nil
		}
	}
	return ArtifactRecord{}, ErrArtifactNotFound
}

func (f *fakeQueries) ListParentIDs(_ context.Context, input ArtifactParentsInput) ([]string, error) {
	var ids []string
	for _, artifact := range f.priorLogs {
		if artifact.Status == "ok" &&
			artifact.SupersededBy == input.SuccessorID &&
			artifact.BotID == input.BotID &&
			artifact.SessionID == input.SessionID {
			ids = append(ids, artifact.ID)
		}
	}
	return ids, nil
}

func (f *fakeQueries) CountLogs(context.Context, string) (int64, error) {
	return int64(len(f.priorLogs)), nil
}

func (*fakeQueries) ListLogs(context.Context, ListLogsInput) ([]LogRecord, error) {
	return nil, nil
}

func (f *fakeQueries) DeleteLogs(context.Context, string) error {
	f.priorLogs = nil
	return nil
}

func (f *fakeQueries) ClaimCandidates(_ context.Context, arg ClaimCandidatesInput) (int64, error) {
	f.queryCalls = append(f.queryCalls, "mark")
	f.markedIDs = append([]string(nil), arg.MessageIDs...)
	f.markArg = arg
	f.markArg.MessageIDs = append([]string(nil), arg.MessageIDs...)
	f.markArg.ExpectedCompactIDs = append([]string(nil), arg.ExpectedCompactIDs...)
	if f.markedRowCount != nil {
		return *f.markedRowCount, nil
	}
	return int64(len(arg.MessageIDs)), nil
}

func (f *fakeQueries) CompleteLog(_ context.Context, arg CompleteLogInput) error {
	f.completeCalls = append(f.completeCalls, arg)
	if f.onComplete != nil {
		f.onComplete()
	}
	if len(f.completeErrors) > 0 {
		err := f.completeErrors[0]
		f.completeErrors = f.completeErrors[1:]
		if err != nil {
			return err
		}
	}
	if f.completeErr != nil {
		return f.completeErr
	}
	f.completed = arg
	return nil
}

// --- harness ------------------------------------------------------------------

type testStore interface {
	CompactionStore
	ArtifactStore
}

func newMachineryService(q testStore) *Service {
	return NewService(slog.New(slog.DiscardHandler), q, q)
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

func idSet(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// machineryCorpus returns a deterministic session whose oldest portion contains
// two tool exchanges (one base64-image result, one structured stdout result),
// plus recent text turns. Indices are returned for precise assertions.
func machineryCorpus(t *testing.T) []CandidateRecord {
	t.Helper()
	b64 := strings.Repeat("QUJD", 100) // 400 base64 chars
	return []CandidateRecord{
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
		priorLogs: []ArtifactRecord{{
			ID:        uuid.NewString(),
			BotID:     cfg.BotID,
			SessionID: cfg.SessionID,
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

// TestDoCompactionWarnsWhenEntryFloorsExceedBudget drives one unsplittable
// tool exchange with more minimal entries than MaxCompactTokens can hold:
// the progress guarantee still compacts it, but the overshoot must be
// surfaced instead of silently trusted as capped.

func TestDoCompactionWarnsWhenEntryFloorsExceedBudget(t *testing.T) {
	const fanout = 40
	callParts := make([]string, 0, fanout)
	for i := 0; i < fanout; i++ {
		callParts = append(callParts, fmt.Sprintf(`{"type":"tool-call","toolCallId":"c%d","toolName":"probe","input":{}}`, i))
	}
	rows := []CandidateRecord{
		mkRow(t, "assistant", "["+strings.Join(callParts, ",")+"]", 100),
	}
	for i := 0; i < fanout; i++ {
		rows = append(rows, mkRow(t, "tool", fmt.Sprintf(`[{"type":"tool-result","toolCallId":"c%d","toolName":"probe","output":{"type":"text","value":"ok"}}]`, i), 100))
	}
	rows = append(rows, mkRow(t, "user", `[{"type":"text","text":"recent question"}]`, 100))

	q := &fakeQueries{uncompacted: rows}
	stub := &stubModel{summary: "SUMMARY-OK"}
	var logBuf strings.Builder
	svc := NewService(slog.New(slog.NewTextHandler(&logBuf, nil)), q, q)

	cfg := machineryConfig(stub, 100)
	cfg.MaxCompactTokens = 40

	res, err := svc.RunCompactionSync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunCompactionSync: %v", err)
	}
	if res.Status != StatusOK {
		t.Fatalf("the oversized exchange must still compact (progress guarantee), got %q", res.Status)
	}
	if !strings.Contains(logBuf.String(), "entry floors exceed the budget") {
		t.Fatalf("budget overshoot not surfaced in logs:\n%s", logBuf.String())
	}
}

func TestDoCompactionAllEmptyWindowSkipsModelAndMarking(t *testing.T) {
	rows := []CandidateRecord{
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
	rows := []CandidateRecord{
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
	rows := []CandidateRecord{
		mkRow(t, "user", `[{"type":"text","text":"old question"}]`, 100),       // 0
		mkRow(t, "assistant", `[{"type":"reasoning","text":"thinking"}]`, 100), // 1 renders empty
		mkRow(t, "assistant", `[{"type":"text","text":"old answer"}]`, 100),    // 2
		mkRow(t, "user", `[{"type":"text","text":"recent question"}]`, 100),    // 3 kept
		mkRow(t, "assistant", `[{"type":"text","text":"recent answer"}]`, 100), // 4 kept
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
