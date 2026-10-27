package compaction

import (
	"testing"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func toolCallRow(t *testing.T, tokens int) sqlc.ListUncompactedMessagesBySessionRow {
	t.Helper()
	return mkRow(t, "assistant", `[{"type":"tool-call","toolName":"calc","toolCallId":"c1","input":{}}]`, tokens)
}

func toolResultRow(t *testing.T, tokens int) sqlc.ListUncompactedMessagesBySessionRow {
	t.Helper()
	return mkRow(t, "tool", `[{"type":"tool-result","toolName":"calc","toolCallId":"c1","output":{"type":"text","value":"42"}}]`, tokens)
}

// textRow builds a plain text row whose rendered entry is ~tokens tokens, so
// trim tests drive the rendered-byte budget directly.
func textRow(t *testing.T, role string, tokens int) sqlc.ListUncompactedMessagesBySessionRow {
	t.Helper()
	body := make([]byte, 0, tokens*4)
	for len(body) < tokens*4 {
		body = append(body, 'x')
	}
	return mkRow(t, role, `"`+string(body)+`"`, 0)
}

func firstKeptIsNotOrphanTool(t *testing.T, items, toCompact []CompactionCandidate) {
	t.Helper()
	keepStart := len(toCompact)
	if keepStart < len(items) && isToolResultItem(items[keepStart]) {
		t.Fatalf("keep side starts with an orphan tool result at index %d", keepStart)
	}
}

func TestFoldableItemsStopsBeforeCurrentInputWithoutOrphaningTools(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "assistant", `"context"`, 0),
		toolCallRow(t, 0),
		toolResultRow(t, 0),
		mkRow(t, "user", `"current"`, 0),
	}
	items, _ := candidatesFromRows(rows)
	folded := foldBefore(rows, rows[3])
	firstKeptIsNotOrphanTool(t, items, folded)
	if len(folded) != 3 || !isToolResultItem(folded[2]) {
		t.Fatalf("folded %d rows, want the history before the current input with its tool exchange whole", len(folded))
	}
	if folded := foldBefore(rows, rows[0]); len(folded) != 0 {
		t.Fatalf("folded %d rows, want none when the current input comes first", len(folded))
	}
}

func TestAdjustForToolBoundaryNoOpWhenKeepStartsClean(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "user", `"a"`, 100),
		mkRow(t, "assistant", `"b"`, 100),
		mkRow(t, "user", `"c"`, 100),
	}
	items, _ := candidatesFromRows(rows)
	if got := adjustForToolBoundary(items, 2); got != 2 {
		t.Fatalf("clean boundary should be unchanged, got %d", got)
	}
}

func TestAdjustForToolBoundaryAdvancesPastConsecutiveResults(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		toolCallRow(t, 100),
		toolResultRow(t, 100),
		toolResultRow(t, 100),
		mkRow(t, "assistant", `"done"`, 100),
	}
	items, _ := candidatesFromRows(rows)
	// cutoff=1 would keep two consecutive tool results; advance past both.
	if got := adjustForToolBoundary(items, 1); got != 3 {
		t.Fatalf("should advance past consecutive tool results, got %d", got)
	}
}

func TestAdjustForToolBoundaryAllToolsCompactsAll(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		toolResultRow(t, 100),
		toolResultRow(t, 100),
	}
	items, _ := candidatesFromRows(rows)
	if got := adjustForToolBoundary(items, 1); got != 2 {
		t.Fatalf("all-tool tail should compact everything, got %d", got)
	}
}

func TestToolBoundaryGuardRequiresToolClosurePolicy(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		toolCallRow(t, 100),
		toolResultRow(t, 100),
		mkRow(t, "assistant", `"done"`, 100),
	}
	items, _ := candidatesFromRows(rows)
	items[1].Policies = nil
	if got := adjustForToolBoundary(items, 1); got != 1 {
		t.Fatalf("boundary should be policy-driven when tool-result policy is absent, got %d", got)
	}
}

func TestTrimCompactMessagesKeepsOldestAndToolExchangeIntact(t *testing.T) {
	t.Parallel()

	// compact input (oldest -> newest): a small tool exchange, then two text
	// rows of ~130 rendered tokens. maxTokens 200 keeps the oldest groups
	// within budget — the whole exchange plus the first text row — and defers
	// the newest overflow to a later pass.
	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		toolCallRow(t, 0),
		toolResultRow(t, 0),
		textRow(t, "user", 100),
		textRow(t, "assistant", 100),
	}
	items, _ := candidatesFromRows(rows)
	trimmed := trimCompactMessages(items, 200)
	if len(trimmed) != 3 {
		t.Fatalf("trimmed = %d, want the oldest exchange plus the first text row", len(trimmed))
	}
	if trimmed[0].ID != items[0].ID || trimmed[1].ID != items[1].ID {
		t.Fatalf("trim must keep the oldest tool exchange intact at the head")
	}
	if trimmed[len(trimmed)-1].ID != items[2].ID {
		t.Fatalf("newest overflow must be deferred, got tail %s", formatUUID(trimmed[len(trimmed)-1].ID))
	}
}

func TestTrimCompactMessagesNeverCutsAnOversizedFirstGroup(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		textRow(t, "user", 600),
		textRow(t, "assistant", 100),
	}
	items, _ := candidatesFromRows(rows)
	if trimmed := trimCompactMessages(items, 350); len(trimmed) != 0 {
		t.Fatalf("trimmed = %d, want nothing: a summary covers only what its model read whole", len(trimmed))
	}
}

func TestAdjustForToolBoundaryIgnoresZeroCutoff(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		toolResultRow(t, 100),
		mkRow(t, "assistant", `"x"`, 100),
	}
	items, _ := candidatesFromRows(rows)
	// cutoff 0 means "compact nothing"; never start compacting from the front.
	if got := adjustForToolBoundary(items, 0); got != 0 {
		t.Fatalf("zero cutoff must stay zero, got %d", got)
	}
}

func TestTrimCompactMessagesFloorsTinyRowCosts(t *testing.T) {
	t.Parallel()

	rows := make([]sqlc.ListUncompactedMessagesBySessionRow, 0, 100)
	for i := 0; i < 100; i++ {
		rows = append(rows, mkRow(t, "user", `"a"`, 0)) // no usage; raw estimate rounds to 0
	}
	items, _ := candidatesFromRows(rows)
	trimmed := trimCompactMessages(items, markableGroupCost(items, []int{0}))
	// Each markable row costs at least one token, so a swarm of tiny rows
	// cannot ride into the prompt for free.
	if len(trimmed) != 1 {
		t.Fatalf("trimmed = %d rows, want 1 (per-row cost floor)", len(trimmed))
	}
}
