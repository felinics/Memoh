package compaction

import (
	"context"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// starvationCorpus reproduces the leading-unmarkable-island shape: a
// reasoning-only row that renders empty, an ask_user exchange that is
// must-keep, then ordinary old history, then the current turn.
func starvationCorpus(t *testing.T) []sqlc.ListUncompactedMessagesBySessionRow {
	t.Helper()
	return []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "assistant", `[{"type":"reasoning","text":"internal chain of thought"}]`, 400),                                       // 0: renders empty
		mkRow(t, "assistant", `[{"type":"tool-call","toolCallId":"U","toolName":"ask_user","input":{}}]`, 40),                         // 1: must-keep call
		mkRow(t, "tool", `[{"type":"tool-result","toolCallId":"U","toolName":"ask_user","output":"user said yes"}]`, 40),              // 2: must-keep result
		mkRow(t, "user", `"old question about the migration plan with enough surrounding detail to be worth compressing"`, 400),       // 3
		mkRow(t, "assistant", `"old answer walking through every step of the plan in enough words to outweigh a short summary"`, 400), // 4
		mkRow(t, "user", `"current question"`, 40), // 5: protected current turn
	}
}

func TestDoCompactionAdvancesPastUnmarkableLeadingIsland(t *testing.T) {
	t.Parallel()

	rows := starvationCorpus(t)
	q := &fakeQueries{uncompacted: rows}
	stub := &stubModel{summary: "compacted the old exchange"}
	svc := newMachineryService(q)

	res, err := svc.RunCompactionSync(context.Background(), machineryConfig(stub, 100))
	if err != nil {
		t.Fatalf("RunCompactionSync: %v", err)
	}
	if res.Status != StatusOK {
		t.Fatalf("status = %q, want %q: an unmarkable leading island must not starve the history behind it", res.Status, StatusOK)
	}

	marked := idSet(q.markedIDs)
	if len(marked) != 2 || !marked[rows[3].ID] || !marked[rows[4].ID] {
		t.Fatalf("marked = %v, want exactly the old question/answer pair", q.markedIDs)
	}
	for _, frag := range []string{"old question", "old answer"} {
		if !strings.Contains(stub.prompt, frag) {
			t.Fatalf("summarizer prompt missing %q:\n%s", frag, stub.prompt)
		}
	}
	for _, frag := range []string{"ask_user", "user said yes", "current question", "chain of thought"} {
		if strings.Contains(stub.prompt, frag) {
			t.Fatalf("summarizer prompt leaked raw-retained content %q:\n%s", frag, stub.prompt)
		}
	}
}

func TestDoCompactionNoopsWithoutModelCallWhenOnlyUnmarkableRowsRemain(t *testing.T) {
	t.Parallel()

	rows := starvationCorpus(t)
	remaining := append([]sqlc.ListUncompactedMessagesBySessionRow{}, rows[0], rows[1], rows[2], rows[5])
	q := &fakeQueries{uncompacted: remaining}
	stub := &stubModel{summary: "should never be produced"}
	svc := newMachineryService(q)

	res, err := svc.RunCompactionSync(context.Background(), machineryConfig(stub, 100))
	if err != nil {
		t.Fatalf("RunCompactionSync: %v", err)
	}
	if res.Status != StatusNoop {
		t.Fatalf("status = %q, want %q", res.Status, StatusNoop)
	}
	if stub.calls != 0 {
		t.Fatalf("model called %d times for an unmarkable window, want 0", stub.calls)
	}
	if len(q.markedIDs) != 0 {
		t.Fatalf("marked = %v, want none", q.markedIDs)
	}
	if q.created {
		t.Fatal("a compaction log row was created for a no-op window")
	}
}

func TestDoCompactionCompactsBehindOversizedRenderEmptyHead(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "assistant", `[{"type":"reasoning","text":"a"}]`, 20000), // renders empty, huge raw estimate
		mkRow(t, "assistant", `[{"type":"reasoning","text":"b"}]`, 20000),
		mkRow(t, "user", `"old question"`, 100),
		mkRow(t, "assistant", `"old answer"`, 100),
		mkRow(t, "user", `"current question"`, 40),
	}
	q := &fakeQueries{uncompacted: rows}
	stub := &stubModel{summary: "old exchange condensed"}
	svc := newMachineryService(q)

	cfg := machineryConfig(stub, 100)
	cfg.MaxCompactTokens = 10000 // smaller than one reasoning row's raw estimate
	res, err := svc.RunCompactionSync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunCompactionSync: %v", err)
	}
	if res.Status != StatusOK {
		t.Fatalf("status = %q, want %q: a render-empty head must not eat the trim budget", res.Status, StatusOK)
	}
	marked := idSet(q.markedIDs)
	if len(marked) != 2 || !marked[rows[2].ID] || !marked[rows[3].ID] {
		t.Fatalf("marked = %v, want exactly the old question/answer pair", q.markedIDs)
	}
}

func TestDoCompactionCompactsBehindOversizedIncompleteExchange(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "assistant", `[{"type":"text","text":"running the tool"},{"type":"tool-call","toolCallId":"x","toolName":"exec","input":{}}]`, 40000),
		mkRow(t, "tool", `[{"type":"binary","data":"opaque"}]`, 100), // unrecognized part renders empty -> exchange incomplete
		mkRow(t, "user", `"old question"`, 100),
		mkRow(t, "assistant", `"old answer"`, 100),
		mkRow(t, "user", `"current question"`, 40),
	}
	q := &fakeQueries{uncompacted: rows}
	stub := &stubModel{summary: "old exchange condensed"}
	svc := newMachineryService(q)

	cfg := machineryConfig(stub, 100)
	cfg.MaxCompactTokens = 10000 // smaller than the incomplete exchange's raw estimate
	res, err := svc.RunCompactionSync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunCompactionSync: %v", err)
	}
	if res.Status != StatusOK {
		t.Fatalf("status = %q, want %q: an unmarkable oversized exchange must not eat the trim budget", res.Status, StatusOK)
	}
	marked := idSet(q.markedIDs)
	if len(marked) != 2 || !marked[rows[2].ID] || !marked[rows[3].ID] {
		t.Fatalf("marked = %v, want exactly the old question/answer pair", q.markedIDs)
	}
}
