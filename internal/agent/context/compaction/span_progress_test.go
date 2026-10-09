package compaction

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func askUserExchange(t *testing.T, n int) []sqlc.ListUncompactedMessagesBySessionRow {
	t.Helper()
	id := fmt.Sprintf("ask-%d", n)
	return []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "assistant", `[{"type":"text","text":"需要你确认第 `+strconv.Itoa(n)+` 个选项"},{"type":"tool-call","toolCallId":"`+id+`","toolName":"ask_user","input":{"questions":[{"question":"继续吗？"}]}}]`, 40),
		mkRow(t, "tool", `[{"type":"tool-result","toolCallId":"`+id+`","toolName":"ask_user","output":{"type":"text","value":"继续"}}]`, 10),
	}
}

func reasoningOnlyRow(t *testing.T) sqlc.ListUncompactedMessagesBySessionRow {
	t.Helper()
	return mkRow(t, "assistant", `[{"type":"reasoning","text":"REASONING_BARRIER internal chain of thought"}]`, 300)
}

func execExchange(t *testing.T, n int) []sqlc.ListUncompactedMessagesBySessionRow {
	t.Helper()
	id := fmt.Sprintf("exec-%d", n)
	stdout := strings.Repeat(fmt.Sprintf("LATER_SPAN build step %d ok; ", n), 60)
	return []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "assistant", `[{"type":"text","text":"LATER_SPAN running step `+strconv.Itoa(n)+`"},{"type":"tool-call","toolCallId":"`+id+`","toolName":"exec","input":{"command":"make step-`+strconv.Itoa(n)+`"}}]`, 80),
		mkRow(t, "tool", `[{"type":"tool-result","toolCallId":"`+id+`","toolName":"exec","output":{"type":"text","value":`+jsonStr(stdout)+`}}]`, 20),
	}
}

// productionOrderRows replays, in stored-row shape, the candidate order that
// stalled MEMOH-100: thirty leading rows held by ask_user exchanges and two
// reasoning-only rows, a 14-character user message, a reasoning-only row,
// then the later history (user turns and exec exchanges) that never reached
// the summarizer, ending with the current user turn.
func productionOrderRows(t *testing.T) (rows []sqlc.ListUncompactedMessagesBySessionRow, tiny, laterStart int) {
	t.Helper()
	ask := 0
	for len(rows) < 30 {
		if len(rows) == 10 || len(rows) == 21 {
			rows = append(rows, reasoningOnlyRow(t))
			continue
		}
		ask++
		rows = append(rows, askUserExchange(t, ask)...)
	}
	tiny = len(rows)
	rows = append(rows, mkRow(t, "user", jsonStr("请帮我继续处理剩下的这些任务"), 10), reasoningOnlyRow(t))
	laterStart = len(rows)
	for n := 1; n <= 3; n++ {
		rows = append(rows, mkRow(t, "user", jsonStr(fmt.Sprintf("LATER_SPAN request %d: migrate the build scripts and report every failing target", n)), 30))
		rows = append(rows, execExchange(t, n)...)
		rows = append(rows, mkRow(t, "assistant", jsonStr(strings.Repeat(fmt.Sprintf("LATER_SPAN answer %d explains the migration result. ", n), 20)), 200))
	}
	rows = append(rows, mkRow(t, "user", jsonStr("current question"), 10))
	return rows, tiny, laterStart
}

// summaryOfTokens returns a summary whose replay estimate is exactly tokens.
func summaryOfTokens(t *testing.T, tokens int) string {
	t.Helper()
	summary := strings.Repeat("s", 4*tokens-21)
	if got := estimateSummaryReplayTokens(summary); got != tokens {
		t.Fatalf("summary fixture = %d tokens, want %d", got, tokens)
	}
	return summary
}

func TestCompactionCommitsLaterSpanPastTinyProductionPrefix(t *testing.T) {
	t.Parallel()

	rows, _, laterStart := productionOrderRows(t)
	q := &fakeQueries{uncompacted: rows}
	stub := &stubModel{summary: summaryOfTokens(t, 254)}
	cfg := machineryConfig(stub, 100)
	cfg.Manual = true

	res, err := newMachineryService(q).RunCompactionSync(context.Background(), cfg)
	if err != nil || res.Status != StatusOK {
		t.Fatalf("result = %+v, %v; want the later span committed", res, err)
	}
	if stub.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", stub.calls)
	}
	want := rows[laterStart : len(rows)-1]
	if len(q.markedIDs) != len(want) {
		t.Fatalf("marked %d rows, want the %d-row later span", len(q.markedIDs), len(want))
	}
	for i, row := range want {
		if q.markedIDs[i] != row.ID {
			t.Fatalf("marked[%d] is not later-span row %d: claims must cover the later span in order", i, laterStart+i)
		}
	}
	if res.MessageCount != len(want) || q.completed.Status != "ok" {
		t.Fatalf("completed = %s with %d rows, want ok with %d", q.completed.Status, res.MessageCount, len(want))
	}
	marked := idSet(q.markedIDs)
	for i := 0; i < laterStart; i++ {
		if marked[rows[i].ID] {
			t.Fatalf("row %d (protected, reasoning or tiny prefix) was claimed", i)
		}
	}
	for _, leaked := range []string{"请帮我继续处理", "REASONING_BARRIER", "ask_user", "current question"} {
		if strings.Contains(stub.prompt, leaked) {
			t.Fatalf("summarizer prompt carried retained content %q", leaked)
		}
	}
	if !strings.Contains(stub.prompt, "LATER_SPAN answer 3") {
		t.Fatal("summarizer prompt missing the end of the later span")
	}
}

func TestCompactionMinimalTinySpanCounterexampleProgresses(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "assistant", jsonStr(strings.Repeat("p", 92)), 100),
		mkRow(t, "assistant", `[{"type":"reasoning","text":"hidden"}]`, 100),
		mkRow(t, "assistant", jsonStr(strings.Repeat("LONG_SUFFIX ", 1000)), 3000),
		mkRow(t, "user", `"current question"`, 100),
	}
	q := &fakeQueries{uncompacted: rows}
	stub := &stubModel{}
	cfg := machineryConfig(stub, 100)
	cfg.Manual = true
	svc := newMachineryService(q)
	for pass, summaryTokens := range []int{280, 71, 172, 86, 254} {
		stub.summary = summaryOfTokens(t, summaryTokens)
		res, err := svc.RunCompactionSync(context.Background(), cfg)
		if err == nil && res.Status == StatusOK && idSet(q.markedIDs)[rows[2].ID] && strings.Contains(stub.prompt, "LONG_SUFFIX") {
			if len(q.markedIDs) != 1 || stub.calls != pass+1 {
				t.Fatalf("pass %d: marked %d rows after %d calls, want only the suffix", pass+1, len(q.markedIDs), stub.calls)
			}
			if stub.calls > 1 {
				t.Fatalf("suffix needed %d provider calls; a span that cannot shrink must not be sent", stub.calls)
			}
			return
		}
		for i := range q.uncompacted {
			q.uncompacted[i].CompactID = q.claims[q.uncompacted[i].ID]
		}
	}
	t.Fatalf("suffix not committed within 5 passes: calls=%d marked=%v", stub.calls, q.markedIDs)
}

// truncatedQueries serves the fake's rows as one read window that has more
// candidates behind it.
type truncatedQueries struct {
	*fakeQueries
	extra int64
}

func (q *truncatedQueries) ListUncompactedMessagesBySessionWithinBytes(ctx context.Context, arg sqlc.ListUncompactedMessagesBySessionWithinBytesParams) ([]sqlc.ListUncompactedMessagesBySessionWithinBytesRow, error) {
	rows, err := q.fakeQueries.ListUncompactedMessagesBySessionWithinBytes(ctx, arg)
	for i := range rows {
		rows[i].CandidateCount += q.extra
	}
	return rows, err
}

func TestCompactionNeverClaimsToolCallWhoseResultIsPastReadWindow(t *testing.T) {
	t.Parallel()

	head := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "user", jsonStr(strings.Repeat("old question with plenty of detail ", 40)), 400),
		mkRow(t, "assistant", jsonStr(strings.Repeat("old answer with plenty of detail ", 40)), 400),
	}
	call := execExchange(t, 9)[0]
	q := &fakeQueries{uncompacted: append(head, call)}
	stub := &stubModel{summary: "old exchange condensed"}
	res, err := newMachineryService(&truncatedQueries{fakeQueries: q, extra: 1}).RunCompactionSync(context.Background(), machineryConfig(stub, 100))
	if err != nil || res.Status != StatusOK {
		t.Fatalf("result = %+v, %v", res, err)
	}
	marked := idSet(q.markedIDs)
	if marked[call.ID] {
		t.Fatal("a tool call at the read-window edge was claimed without its result")
	}
	if len(marked) != 2 || !marked[head[0].ID] || !marked[head[1].ID] {
		t.Fatalf("marked = %v, want the complete exchange before the window edge", q.markedIDs)
	}
}
