package application

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/compaction"
	"github.com/memohai/memoh/domains/agent/chat/context/fragment"
	"github.com/memohai/memoh/domains/agent/chat/context/history"
)

type pairingQueries struct {
	uncompacted []compaction.CandidateRecord
	logID       string
	markedIDs   []string
}

func (f *pairingQueries) ListCandidates(context.Context, string) ([]compaction.CandidateRecord, error) {
	return f.uncompacted, nil
}

func (f *pairingQueries) CreateLog(context.Context, compaction.CreateLogInput) (string, error) {
	return f.logID, nil
}

func (f *pairingQueries) ClaimCandidates(_ context.Context, input compaction.ClaimCandidatesInput) (int64, error) {
	f.markedIDs = append([]string(nil), input.MessageIDs...)
	return int64(len(input.MessageIDs)), nil
}

func (*pairingQueries) ListAssets(context.Context, []string) ([]compaction.AssetRecord, error) {
	return nil, nil
}

func (*pairingQueries) CompleteLog(context.Context, compaction.CompleteLogInput) error {
	return nil
}

func (*pairingQueries) CountLogs(context.Context, string) (int64, error) {
	return 0, nil
}

func (*pairingQueries) ListLogs(context.Context, compaction.ListLogsInput) ([]compaction.LogRecord, error) {
	return nil, nil
}

func (*pairingQueries) DeleteLogs(context.Context, string) error {
	return nil
}

type pairingSummarizer struct{ summary string }

func (s pairingSummarizer) RoundTrip(*http.Request) (*http.Response, error) {
	body := `{"id":"stub","object":"chat.completion","created":0,"model":"stub",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":"` + s.summary + `"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

func pairingRow(t *testing.T, role, content string) compaction.CandidateRecord {
	t.Helper()
	return compaction.CandidateRecord{
		ID:      uuid.NewString(),
		Role:    role,
		Content: []byte(content),
		Usage:   []byte(`{"outputTokens":100}`),
	}
}

// TestSelectorToReadPathPreservesOrderEndToEnd drives the real compaction
// selector over a history with a must-keep ask_user island and feeds its
// actual marked rows into replaceCompactedHistoryRecords. It pins the pair of
// invariants that live in two packages: the selector marks one contiguous
// pre-island run under one compact_id, and the read path substitutes it in
// place — content behind the island (like "mid q") must never fold in front
// of it.
func TestSelectorToReadPathPreservesOrderEndToEnd(t *testing.T) {
	t.Parallel()

	rows := []compaction.CandidateRecord{
		pairingRow(t, "user", `"old q"`),
		pairingRow(t, "assistant", `"old a"`),
		pairingRow(t, "assistant", `[{"type":"tool-call","toolCallId":"ask-1","toolName":"ask_user","input":{"questions":[]}}]`),
		pairingRow(t, "tool", `[{"type":"tool-result","toolCallId":"ask-1","toolName":"ask_user","output":"answered"}]`),
		pairingRow(t, "user", `"mid q"`),
		pairingRow(t, "user", `"current q"`),
	}
	q := &pairingQueries{
		uncompacted: rows,
		logID:       uuid.NewString(),
	}
	svc := compaction.NewService(slog.New(slog.DiscardHandler), q, nil)

	res, err := svc.RunCompactionSync(context.Background(), compaction.TriggerConfig{
		BotID:        uuid.NewString(),
		SessionID:    uuid.NewString(),
		ModelID:      "stub-model",
		ClientType:   "openai-completions",
		APIKey:       "test",
		BaseURL:      "http://stub.invalid",
		HTTPClient:   &http.Client{Transport: pairingSummarizer{summary: "condensed old exchange"}},
		TargetTokens: 200,
	})
	if err != nil {
		t.Fatalf("RunCompactionSync: %v", err)
	}
	if res.Status != compaction.StatusOK {
		t.Fatalf("status = %q, want %q", res.Status, compaction.StatusOK)
	}

	marked := make(map[string]bool, len(q.markedIDs))
	for _, id := range q.markedIDs {
		marked[id] = true
	}
	if len(marked) != 2 || !marked[rows[0].ID] || !marked[rows[1].ID] {
		t.Fatalf("marked = %v, want exactly the contiguous pre-island run [old q, old a]", q.markedIDs)
	}

	compactID := q.logID
	texts := []string{`old q`, `old a`, `ask you something`, `answered`, `mid q`, `current q`}
	roles := []string{"user", "assistant", "assistant", "tool", "user", "user"}
	records := make([]history.HistoryRecord, 0, len(rows))
	for i, row := range rows {
		record := historyRecord(row.ID, agentdomain.ModelMessage{
			Role:    roles[i],
			Content: agentdomain.NewTextContent(texts[i]),
		}, nil)
		if marked[row.ID] {
			record.CompactID = compactID
		}
		records = append(records, record)
	}

	got := replaceCompactedHistoryRecords(records, map[string]string{compactID: res.Summary}, fragment.Scope{})
	want := []agentdomain.ModelMessage{
		{Role: "user", Content: agentdomain.NewTextContent("<summary>\n" + res.Summary + "\n</summary>")},
		{Role: "assistant", Content: agentdomain.NewTextContent("ask you something")},
		{Role: "tool", Content: agentdomain.NewTextContent("answered")},
		{Role: "user", Content: agentdomain.NewTextContent("mid q")},
		{Role: "user", Content: agentdomain.NewTextContent("current q")},
	}
	if gotMessages := history.ToModelMessages(got); !reflect.DeepEqual(gotMessages, want) {
		t.Fatalf("selector output broke read-path ordering:\ngot  %#v\nwant %#v", gotMessages, want)
	}
}
