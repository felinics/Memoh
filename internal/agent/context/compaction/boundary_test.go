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

func TestSplitByTargetDoesNotOrphanToolResult(t *testing.T) {
	t.Parallel()

	// oldest -> newest: assistant, assistant(tool-call), tool(result), assistant.
	// target 250 would otherwise cut between the tool call and its result,
	// leaving the kept side starting with an orphan tool result.
	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "assistant", `"context"`, 100),
		toolCallRow(t, 100),
		toolResultRow(t, 100),
		mkRow(t, "assistant", `"done"`, 100),
	}
	items, _ := itemsFromRows(rows)
	toCompact := splitByTarget(items, 250)
	firstKeptIsNotOrphanTool(t, items, toCompact)
	if len(toCompact) != 3 {
		t.Fatalf("compact count = %d, want 3 (tool result pulled into compact set)", len(toCompact))
	}
	if !isToolResultItem(toCompact[2]) {
		t.Fatalf("last compacted message should be the tool result")
	}
}

func TestSplitByRatioDoesNotOrphanToolResult(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "assistant", `"context"`, 100),
		toolCallRow(t, 100),
		toolResultRow(t, 100),
		mkRow(t, "assistant", `"done"`, 100),
	}
	items, _ := itemsFromRows(rows)
	// total=500, ratio=50 -> keepTokens=250 -> unadjusted cutoff would keep the
	// tool result at the head of the keep side.
	toCompact := splitByRatio(items, 500, 50)
	firstKeptIsNotOrphanTool(t, items, toCompact)
	if len(toCompact) != 3 {
		t.Fatalf("compact count = %d, want 3", len(toCompact))
	}
}

func TestAdjustForToolBoundaryNoOpWhenKeepStartsClean(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "user", `"a"`, 100),
		mkRow(t, "assistant", `"b"`, 100),
		mkRow(t, "user", `"c"`, 100),
	}
	items, _ := itemsFromRows(rows)
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
	items, _ := itemsFromRows(rows)
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
	items, _ := itemsFromRows(rows)
	if got := adjustForToolBoundary(items, 1); got != 2 {
		t.Fatalf("all-tool tail should compact everything, got %d", got)
	}
}

func TestSplitByRatioNoOpsWhenToolBoundaryWouldConsumeRecentTail(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		toolCallRow(t, 100),
		toolResultRow(t, 100),
	}
	items, _ := itemsFromRows(rows)
	toCompact := splitByRatio(items, 200, 100)
	if len(toCompact) != 0 {
		t.Fatalf("compaction should no-op rather than consume the recent tool tail, got %d items", len(toCompact))
	}
}

func TestSplitByRatioPreservesNewestUserTurnSuffix(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "user", `"old question"`, 100),
		mkRow(t, "assistant", `"old answer"`, 100),
		mkRow(t, "user", `"current question"`, 100),
		mkRow(t, "assistant", `"current answer"`, 100),
	}
	items, _ := itemsFromRows(rows)
	toCompact := splitByRatio(items, 400, 100)
	if len(toCompact) != 2 {
		t.Fatalf("compact count = %d, want 2 (keep newest user turn suffix)", len(toCompact))
	}
	if toCompact[0].ID != rows[0].ID || toCompact[1].ID != rows[1].ID {
		t.Fatalf("should compact only rows before the newest user turn")
	}
}

func TestSplitByRatioCompactsCurrentTurnMiddleWhenUserIsFirst(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "user", `"current instruction"`, 100),
		mkRow(t, "assistant", `"loop step 1"`, 100),
		mkRow(t, "assistant", `"loop step 2"`, 100),
		mkRow(t, "assistant", `"latest tail"`, 100),
	}
	items, _ := itemsFromRows(rows)
	toCompact := splitByRatio(items, 400, 100)
	if len(toCompact) != 2 {
		t.Fatalf("compact count = %d, want 2 middle current-turn messages", len(toCompact))
	}
	if toCompact[0].ID != rows[1].ID || toCompact[1].ID != rows[2].ID {
		t.Fatalf("should compact current-turn middle while keeping user and latest tail")
	}
}

func TestSplitByRatioPreservesCurrentTurnTailToolClosure(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "user", `"current instruction"`, 100),
		mkRow(t, "assistant", `"loop step 1"`, 100),
		toolCallRow(t, 100),
		toolResultRow(t, 100),
	}
	items, _ := itemsFromRows(rows)
	toCompact := splitByRatio(items, 400, 100)
	if len(toCompact) != 1 {
		t.Fatalf("compact count = %d, want only pre-tool middle message", len(toCompact))
	}
	if toCompact[0].ID != rows[1].ID {
		t.Fatalf("should keep user plus tail tool closure")
	}
}

func TestToolBoundaryGuardRequiresToolClosurePolicy(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		toolCallRow(t, 100),
		toolResultRow(t, 100),
		mkRow(t, "assistant", `"done"`, 100),
	}
	items, _ := itemsFromRows(rows)
	items[1].Policies = nil
	if got := adjustForToolBoundary(items, 1); got != 1 {
		t.Fatalf("boundary should be policy-driven when tool-result policy is absent, got %d", got)
	}
}

func TestTrimCompactMessagesKeepsOldestAndToolExchangeIntact(t *testing.T) {
	t.Parallel()

	// compact input (oldest -> newest): a tool exchange (~110 rendered tokens
	// with its role prefixes), then two ~100-token text rows. maxTokens 320
	// keeps the oldest groups within budget — the whole exchange plus the
	// first text row — and defers the newest overflow to a later pass.
	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		toolCallRow(t, 0),
		toolResultRow(t, 0),
		textRow(t, "user", 100),
		textRow(t, "assistant", 100),
	}
	items, _ := itemsFromRows(rows)
	trimmed := trimCompactMessages(items, 120)
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

func TestTrimCompactMessagesKeepsOversizedFirstGroup(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		textRow(t, "user", 600),
		textRow(t, "assistant", 100),
	}
	items, _ := itemsFromRows(rows)
	trimmed := trimCompactMessages(items, 350)
	if len(trimmed) != 1 {
		t.Fatalf("trimmed = %d, want the oversized head row kept whole for progress", len(trimmed))
	}
	if trimmed[0].ID != items[0].ID {
		t.Fatalf("the oversized first markable row must be kept")
	}
}

func TestTrimCompactMessagesMustKeepRowsCostNoBudget(t *testing.T) {
	t.Parallel()

	askCall := mkRow(t, "assistant", `[{"type":"tool-call","toolName":"ask_user","toolCallId":"a","input":{}}]`, 600)
	askResult := mkRow(t, "tool", `[{"type":"tool-result","toolName":"ask_user","toolCallId":"a","output":"yes"}]`, 600)
	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		askCall,
		askResult,
		textRow(t, "user", 100),
		textRow(t, "assistant", 100),
		textRow(t, "user", 100),
	}
	items, _ := itemsFromRows(rows)
	trimmed := trimCompactMessages(items, 250)
	if len(trimmed) != 4 {
		t.Fatalf("trimmed = %d, want must-keep island (free) plus two rows within budget", len(trimmed))
	}
	if !trimmed[0].HasPolicy(CompactPolicyMustKeep) || !trimmed[1].HasPolicy(CompactPolicyMustKeep) {
		t.Fatalf("must-keep island must stay in place at the head")
	}
}

func TestAdjustForToolBoundaryIgnoresZeroCutoff(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		toolResultRow(t, 100),
		mkRow(t, "assistant", `"x"`, 100),
	}
	items, _ := itemsFromRows(rows)
	// cutoff 0 means "compact nothing"; never start compacting from the front.
	if got := adjustForToolBoundary(items, 0); got != 0 {
		t.Fatalf("zero cutoff must stay zero, got %d", got)
	}
}

func TestTrimCompactMessagesKeepsOversizedFirstRealGroupBehindFreeHead(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "assistant", `[{"type":"reasoning","text":"a"}]`, 300), // renders empty: zero budget cost
		toolCallRow(t, 400),
		toolResultRow(t, 400),
		mkRow(t, "user", `"newer"`, 100),
	}
	items, _ := itemsFromRows(rows)
	trimmed := trimCompactMessages(items, 500)
	// The zero-cost head must not defeat the keep-first-group progress
	// guarantee: the oversized first real exchange stays in.
	if len(trimmed) < 3 {
		t.Fatalf("trimmed = %d rows, want the free head plus the oversized first real exchange", len(trimmed))
	}
	if trimmed[1].ID != items[1].ID || trimmed[2].ID != items[2].ID {
		t.Fatalf("oversized first real exchange must be kept for progress")
	}
}

func TestTrimCompactMessagesFloorsTinyRowCosts(t *testing.T) {
	t.Parallel()

	rows := make([]sqlc.ListUncompactedMessagesBySessionRow, 0, 100)
	for i := 0; i < 100; i++ {
		rows = append(rows, mkRow(t, "user", `"a"`, 0)) // no usage; raw estimate rounds to 0
	}
	items, _ := itemsFromRows(rows)
	trimmed := trimCompactMessages(items, 1)
	// Each markable row costs at least one token, so a swarm of tiny rows
	// cannot ride into the prompt for free.
	if len(trimmed) != 1 {
		t.Fatalf("trimmed = %d rows, want 1 (per-row cost floor)", len(trimmed))
	}
}
