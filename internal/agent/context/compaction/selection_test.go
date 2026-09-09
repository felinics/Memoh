package compaction

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func testUUID(t *testing.T) pgtype.UUID {
	t.Helper()
	return pgtype.UUID{Bytes: uuid.New(), Valid: true}
}

func mkRow(t *testing.T, role, content string, outputTokens int) sqlc.ListUncompactedMessagesBySessionRow {
	t.Helper()
	row := sqlc.ListUncompactedMessagesBySessionRow{
		ID:      testUUID(t),
		Role:    role,
		Content: json.RawMessage(content),
	}
	if outputTokens > 0 {
		row.Usage = json.RawMessage(`{"outputTokens":` + strconv.Itoa(outputTokens) + `}`)
	}
	return row
}

func itemTokens(items []CompactionCandidate) []int {
	out := make([]int, len(items))
	for i, it := range items {
		out[i] = estimateItemTokens(it)
	}
	return out
}

func TestEstimateItemTokensPrefersOutputTokensThenContentFallback(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "user", `"hello"`, 50),
		mkRow(t, "assistant", `"`+repeat("a", 400)+`"`, 0),
	}
	items, _ := itemsFromRows(rows)
	got := itemTokens(items)
	// First: persisted usage outputTokens. Second: len(content)/4 = 402/4 = 100.
	if got[0] != 50 {
		t.Fatalf("usage token estimate = %d, want 50", got[0])
	}
	if got[1] != len(items[1].RawContent)/4 {
		t.Fatalf("content token estimate = %d, want %d", got[1], len(items[1].RawContent)/4)
	}
}

func TestEstimateItemTokensAcceptsSnakeCaseUsage(t *testing.T) {
	t.Parallel()

	row := mkRow(t, "assistant", `"hello"`, 0)
	row.Usage = json.RawMessage(`{"output_tokens":77}`)
	items, _ := itemsFromRows([]sqlc.ListUncompactedMessagesBySessionRow{row})
	if got := estimateItemTokens(items[0]); got != 77 {
		t.Fatalf("snake-case usage token estimate = %d, want 77", got)
	}
}

func TestItemsFromRowsPreservesIDContentAndClassifiesRecord(t *testing.T) {
	t.Parallel()

	row := mkRow(t, "user", `"hi there"`, 0)
	items, _ := itemsFromRows([]sqlc.ListUncompactedMessagesBySessionRow{row})
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
	it := items[0]
	if it.ID != row.ID {
		t.Fatalf("item id mismatch")
	}
	if string(it.RawContent) != string(row.Content) {
		t.Fatalf("item content = %q, want %q", it.RawContent, row.Content)
	}
	if it.Record.ModelMessage.Role != "user" {
		t.Fatalf("record role = %q, want user", it.Record.ModelMessage.Role)
	}
	if it.Record.ModelMessage.TextContent() != "hi there" {
		t.Fatalf("record text = %q, want 'hi there'", it.Record.ModelMessage.TextContent())
	}
}

func TestItemsFromRowsPreservesDirectedSignalMetadata(t *testing.T) {
	t.Parallel()

	row := mkRow(t, "user", `"please check the reply"`, 0)
	row.SenderUserID = testUUID(t)
	row.ExternalMessageID = pgtype.Text{String: "msg-123", Valid: true}
	row.SourceReplyToMessageID = pgtype.Text{String: "msg-parent", Valid: true}
	row.EventID = testUUID(t)
	row.SenderDisplayName = pgtype.Text{String: "Alice", Valid: true}
	row.Platform = pgtype.Text{String: "telegram", Valid: true}
	row.ConversationType = pgtype.Text{String: "group", Valid: true}
	row.ConversationName = "Ops Room"
	row.ReplyTarget = pgtype.Text{String: "thread-9", Valid: true}

	items, barrierCount := itemsFromRows([]sqlc.ListUncompactedMessagesBySessionRow{row})
	if barrierCount != 0 || len(items) != 1 {
		t.Fatalf("items=%d barriers=%d, want one classified row", len(items), barrierCount)
	}
	record := items[0].Record
	if record.ExternalMessageID != "msg-123" ||
		record.SourceReplyToMessageID != "msg-parent" ||
		record.SenderDisplayName != "Alice" ||
		record.Platform != "telegram" ||
		record.Scope.ConversationType != "group" ||
		record.Scope.ConversationName != "Ops Room" ||
		record.Scope.ReplyTarget != "thread-9" {
		t.Fatalf("directed signal was not preserved: %#v scope=%#v", record, record.Scope)
	}
}

func TestItemsFromRowsAnnotatesCompactionCandidatePolicy(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "user", `"old question"`, 100),
		mkRow(t, "assistant", `"old answer"`, 100),
		mkRow(t, "user", `"current question"`, 100),
		toolCallRow(t, 100),
		toolResultRow(t, 100),
	}
	items, barrierCount := itemsFromRows(rows)
	if barrierCount != 0 || len(items) != 5 {
		t.Fatalf("items=%d barriers=%d, want five classified candidates", len(items), barrierCount)
	}

	assertNoPolicy(t, items[0], CompactPolicyPreserveRecent)
	assertNoPolicy(t, items[0], CompactPolicyMustKeep)
	assertNoPolicy(t, items[1], CompactPolicyPreserveRecent)
	assertNoPolicy(t, items[1], CompactPolicyMustKeep)
	assertPolicy(t, items[2], CompactPolicyPreserveRecent)
	assertPolicy(t, items[3], CompactPolicyPreserveToolClosure)
	assertPolicy(t, items[3], CompactPolicyPreserveRecent)
	assertPolicy(t, items[4], CompactPolicyPreserveToolClosure)
	assertPolicy(t, items[4], CompactPolicyPreserveRecent)
}

func TestItemsFromRowsKeepsAskUserToolExchange(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "user", `"current instruction"`, 100),
		mkRow(t, "assistant", `[{"type":"tool-call","toolName":"`+userinput.ToolNameAskUser+`","toolCallId":"ask-1","input":{"questions":[]}}]`, 100),
		mkRow(t, "tool", `[{"type":"tool-result","toolName":"`+userinput.ToolNameAskUser+`","toolCallId":"ask-1","result":{"status":"submitted"}}]`, 100),
		mkRow(t, "assistant", `"latest tail"`, 100),
	}
	items, barrierCount := itemsFromRows(rows)
	if barrierCount != 0 || len(items) != 4 {
		t.Fatalf("items=%d barriers=%d, want four classified candidates", len(items), barrierCount)
	}

	for _, idx := range []int{1, 2} {
		assertPolicy(t, items[idx], CompactPolicy("must_keep"))
		assertPolicy(t, items[idx], CompactPolicyPreserveToolClosure)
	}
}

func TestGuardedSelectionCompactsAroundMustKeepAskExchange(t *testing.T) {
	t.Parallel()

	askCall := mkRow(t, "assistant", `[{"type":"tool-call","toolName":"`+userinput.ToolNameAskUser+`","toolCallId":"ask-1","input":{"questions":[]}}]`, 100)
	askResult := mkRow(t, "tool", `[{"type":"tool-result","toolName":"`+userinput.ToolNameAskUser+`","toolCallId":"ask-1","result":{"status":"submitted"}}]`, 100)
	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		askCall,
		askResult,
		mkRow(t, "user", `"question 2"`, 100),
		mkRow(t, "assistant", `"answer 2"`, 100),
		mkRow(t, "user", `"question 3"`, 100),
		mkRow(t, "assistant", `"answer 3"`, 100),
		mkRow(t, "user", `"current question"`, 100),
		mkRow(t, "assistant", `"current answer"`, 100),
	}
	items, _ := itemsFromRows(rows)

	_, ids := buildEntriesAndIDs(splitByTarget(items, 200))
	if len(ids) == 0 {
		t.Fatal("a must-keep ask exchange at the window head must not starve compaction of newer history")
	}
	for _, id := range ids {
		if id == askCall.ID || id == askResult.ID {
			t.Fatalf("must-keep ask_user row was marked for compaction")
		}
		for _, protected := range []pgtype.UUID{rows[6].ID, rows[7].ID} {
			if id == protected {
				t.Fatalf("current turn row was marked for compaction")
			}
		}
	}

	if _, ratioIDs := buildEntriesAndIDs(splitByRatio(items, 800, 80)); len(ratioIDs) == 0 {
		t.Fatal("ratio-based selection must also compact around the must-keep island")
	}
}

func TestParallelToolCallsPropagateMustKeepToSiblingResult(t *testing.T) {
	t.Parallel()

	// One assistant message carries two parallel tool calls (ask_user + calc).
	// The row is must-keep because of ask_user, but the calc result is a
	// separate row that only carries the tool-closure policy on its own.
	assistantCall := mkRow(t, "assistant", `[{"type":"tool-call","toolName":"`+userinput.ToolNameAskUser+`","toolCallId":"ask-1","input":{"questions":[]}},`+
		`{"type":"tool-call","toolName":"calc","toolCallId":"calc-1","input":{}}]`, 100)
	calcResult := mkRow(t, "tool", `[{"type":"tool-result","toolName":"calc","toolCallId":"calc-1","output":{"type":"text","value":"42"}}]`, 100)
	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "user", `"question 1"`, 100),
		assistantCall,
		calcResult,
		mkRow(t, "user", `"question 2"`, 100),
		mkRow(t, "assistant", `"answer 2"`, 100),
		mkRow(t, "user", `"current question"`, 100),
		mkRow(t, "assistant", `"current answer"`, 100),
	}
	items, _ := itemsFromRows(rows)

	assertPolicy(t, items[2], CompactPolicyMustKeep)

	_, ids := buildEntriesAndIDs(splitByTarget(items, 200))
	if len(ids) == 0 {
		t.Fatal("compaction should still proceed around the must-keep exchange")
	}
	for _, id := range ids {
		if id == assistantCall.ID {
			t.Fatalf("must-keep assistant row with parallel tool calls was marked for compaction")
		}
		if id == calcResult.ID {
			t.Fatalf("sibling tool-result row of a must-keep exchange was marked for compaction")
		}
	}
}

func TestGuardedSelectionKeepsCompactRunContiguousAcrossMustKeepIsland(t *testing.T) {
	t.Parallel()

	askCall := mkRow(t, "assistant", `[{"type":"tool-call","toolName":"`+userinput.ToolNameAskUser+`","toolCallId":"ask-1","input":{"questions":[]}}]`, 100)
	askResult := mkRow(t, "tool", `[{"type":"tool-result","toolName":"`+userinput.ToolNameAskUser+`","toolCallId":"ask-1","result":{"status":"submitted"}}]`, 100)
	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "user", `"old q"`, 100),          // 0 run before island
		mkRow(t, "assistant", `"old a"`, 100),     // 1
		askCall,                                   // 2 must-keep island
		askResult,                                 // 3
		mkRow(t, "user", `"mid q"`, 100),          // 4 run after island
		mkRow(t, "assistant", `"mid a"`, 100),     // 5
		mkRow(t, "user", `"current q"`, 100),      // 6 protected recent
		mkRow(t, "assistant", `"current a"`, 100), // 7
	}
	items, _ := itemsFromRows(rows)

	// One pass must select a single contiguous run so the read path can drop it
	// in place. It may only be the rows before the first must-keep island, never
	// straddling ask_user into "mid q"/"mid a" under a shared compact_id.
	_, gotIDs := buildEntriesAndIDs(splitByTarget(items, 200))
	wantIDs := []pgtype.UUID{rows[0].ID, rows[1].ID}
	if len(gotIDs) != len(wantIDs) {
		t.Fatalf("selected %d rows, want the contiguous pre-island run [old q, old a]", len(gotIDs))
	}
	for i := range wantIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Fatalf("selected run must be the contiguous pre-island run, got id[%d]=%s", i, formatUUID(gotIDs[i]))
		}
	}

	// The run after the island is not starved: once the pre-island run is
	// compacted away, the next pass makes progress on "mid q"/"mid a".
	nextItems, _ := itemsFromRows(rows[2:])
	_, nextIDs := buildEntriesAndIDs(splitByTarget(nextItems, 200))
	if len(nextIDs) == 0 {
		t.Fatal("run after the must-keep island must be compactable on a later pass")
	}
	for _, id := range nextIDs {
		if id == askCall.ID || id == askResult.ID {
			t.Fatalf("must-keep ask_user row was marked for compaction")
		}
	}
}

func TestItemsFromRowsAllowsCurrentTurnMiddleCompaction(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "user", `"current instruction"`, 100),
		mkRow(t, "assistant", `"loop step 1"`, 100),
		mkRow(t, "assistant", `"loop step 2"`, 100),
		mkRow(t, "assistant", `"latest tail"`, 100),
	}
	items, barrierCount := itemsFromRows(rows)
	if barrierCount != 0 || len(items) != 4 {
		t.Fatalf("items=%d barriers=%d, want four classified candidates", len(items), barrierCount)
	}

	assertPolicy(t, items[0], CompactPolicyPreserveRecent)
	assertNoPolicy(t, items[1], CompactPolicyPreserveRecent)
	assertNoPolicy(t, items[1], CompactPolicyMustKeep)
	assertNoPolicy(t, items[2], CompactPolicyPreserveRecent)
	assertNoPolicy(t, items[2], CompactPolicyMustKeep)
	assertPolicy(t, items[3], CompactPolicyPreserveRecent)
}

func TestItemsFromRowsPreservesUnparseableRowsAsSelectionBarriers(t *testing.T) {
	t.Parallel()

	before := mkRow(t, "user", `"before"`, 100)
	bad := sqlc.ListUncompactedMessagesBySessionRow{
		ID:      pgtype.UUID{Valid: false}, // empty id -> FromDBMessage rejects it
		Role:    "user",
		Content: json.RawMessage(`"x"`),
	}
	after := mkRow(t, "assistant", `"after"`, 100)
	current := mkRow(t, "user", `"current"`, 100)
	items, barrierCount := itemsFromRows([]sqlc.ListUncompactedMessagesBySessionRow{before, bad, after, current})
	if barrierCount != 1 {
		t.Fatalf("barrier count = %d, want 1", barrierCount)
	}
	if len(items) != 4 || !items[1].HasPolicy(CompactPolicyMustKeep) {
		t.Fatalf("an unparseable row must remain as a selection barrier: %#v", items)
	}

	_, ids := buildEntriesAndIDs(splitByTarget(items, 100))
	if len(ids) != 1 || ids[0] != before.ID {
		t.Fatalf("marked = %#v, want only the contiguous run before the barrier", ids)
	}
}

func assertPolicy(t *testing.T, item CompactionCandidate, policy CompactPolicy) {
	t.Helper()
	if !item.HasPolicy(policy) {
		t.Fatalf("candidate %s missing policy %s; got %#v", formatUUID(item.ID), policy, item.Policies)
	}
}

func assertNoPolicy(t *testing.T, item CompactionCandidate, policy CompactPolicy) {
	t.Helper()
	if item.HasPolicy(policy) {
		t.Fatalf("candidate %s unexpectedly has policy %s; got %#v", formatUUID(item.ID), policy, item.Policies)
	}
}

func TestSplitByTargetCompactsOldestBeyondTarget(t *testing.T) {
	t.Parallel()

	// Tokens (oldest -> newest): 100, 100, 100. Target 150: newest fits (100),
	// adding the next exceeds 150, so keep the newest one and compact the oldest two.
	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "user", `"a"`, 100),
		mkRow(t, "assistant", `"b"`, 100),
		mkRow(t, "user", `"c"`, 100),
	}
	items, _ := itemsFromRows(rows)
	toCompact := splitByTarget(items, 150)
	if len(toCompact) != 2 {
		t.Fatalf("compact count = %d, want 2", len(toCompact))
	}
	if toCompact[0].ID != rows[0].ID || toCompact[1].ID != rows[1].ID {
		t.Fatalf("compacted the wrong (non-oldest) messages")
	}
}

func TestSplitByRatioKeepsNewestByRatio(t *testing.T) {
	t.Parallel()

	// total=300, ratio=50 -> keepTokens=150 -> keep newest one, compact oldest two.
	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "user", `"a"`, 100),
		mkRow(t, "assistant", `"b"`, 100),
		mkRow(t, "user", `"c"`, 100),
	}
	items, _ := itemsFromRows(rows)
	toCompact := splitByRatio(items, 300, 50)
	if len(toCompact) != 2 {
		t.Fatalf("compact count = %d, want 2", len(toCompact))
	}
	if toCompact[0].ID != rows[0].ID || toCompact[1].ID != rows[1].ID {
		t.Fatalf("compacted the wrong messages")
	}
}

func TestSplitByRatioBoundaryConditions(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{mkRow(t, "user", `"a"`, 100)}
	items, _ := itemsFromRows(rows)

	if got := splitByRatio(items, 0, 50); got != nil {
		t.Fatalf("zero total tokens should yield nil, got %d", len(got))
	}
	if got := splitByRatio(items, 100, 0); got != nil {
		t.Fatalf("zero ratio should yield nil, got %d", len(got))
	}
	if got := splitByRatio(items, 100, 100); len(got) != 0 {
		t.Fatalf("ratio 100 should preserve the only recent candidate, got %d", len(got))
	}
}

func TestTrimCompactMessagesDefersNewestBeyondBudget(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		textRow(t, "user", 100),
		textRow(t, "assistant", 100),
		textRow(t, "user", 100),
	}
	items, _ := itemsFromRows(rows)
	trimmed := trimCompactMessages(items, 150)
	// Budget 150, accumulate rendered costs from oldest: ~102 (a) fits,
	// +~102 (b) > 150 -> keep the oldest row and defer the newer overflow to
	// a later pass, so passes chew history front-to-back in chronological
	// order.
	if len(trimmed) != 1 {
		t.Fatalf("trimmed count = %d, want 1", len(trimmed))
	}
	if trimmed[0].ID != rows[0].ID {
		t.Fatalf("trim should keep the oldest message and defer newer ones")
	}
}

func TestTrimCompactMessagesAccountsForDirectedSignalHeaders(t *testing.T) {
	t.Parallel()

	longID := repeat("m", entryMetadataMaxBytes)
	longSender := repeat("s", entryMetadataMaxBytes)
	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		textRow(t, "assistant", 1),
		textRow(t, "assistant", 1),
		textRow(t, "assistant", 1),
	}
	for i := range rows {
		rows[i].ExternalMessageID = pgtype.Text{String: longID, Valid: true}
		rows[i].SenderDisplayName = pgtype.Text{String: longSender, Valid: true}
	}
	items, _ := itemsFromRows(rows)

	trimmed := trimCompactMessages(items, 300)
	if len(trimmed) != 2 {
		t.Fatalf("trimmed count = %d, want 2 after accounting for header tokens", len(trimmed))
	}
	if trimmed[0].ID != rows[0].ID || trimmed[1].ID != rows[1].ID {
		t.Fatalf("trim should defer the newest message when headers exceed budget")
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
