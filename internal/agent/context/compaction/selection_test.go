package compaction

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/turn"
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

// foldBefore classifies rows and returns what a compaction may fold when
// current names the caller's current input.
func foldBefore(rows []sqlc.ListUncompactedMessagesBySessionRow, current ...sqlc.ListUncompactedMessagesBySessionRow) []CompactionCandidate {
	items, _ := candidatesFromRows(rows)
	protectCurrentSources(items, historySources(current...))
	return foldableItems(items)
}

func historySources(rows ...sqlc.ListUncompactedMessagesBySessionRow) []turn.ContextMessageSource {
	sources := make([]turn.ContextMessageSource, 0, len(rows))
	for _, row := range rows {
		sources = append(sources, turn.ContextMessageSource{Kind: "history", ID: row.ID.String(), Current: true})
	}
	return sources
}

func TestCandidatesFromRowsPreservesIDContentAndClassifiesRecord(t *testing.T) {
	t.Parallel()

	row := mkRow(t, "user", `"hi there"`, 0)
	items, _ := candidatesFromRows([]sqlc.ListUncompactedMessagesBySessionRow{row})
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

func TestCandidatesFromRowsPreservesDirectedSignalMetadata(t *testing.T) {
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

	items, barrierCount := candidatesFromRows([]sqlc.ListUncompactedMessagesBySessionRow{row})
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

func TestProtectCurrentSourcesKeepsCurrentInputAndWhatFollows(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "user", `"old question"`, 100),
		mkRow(t, "assistant", `"old answer"`, 100),
		mkRow(t, "user", `"current question"`, 100),
		toolCallRow(t, 100),
		toolResultRow(t, 100),
	}
	items, barrierCount := candidatesFromRows(rows)
	if barrierCount != 0 || len(items) != 5 {
		t.Fatalf("items=%d barriers=%d, want five classified candidates", len(items), barrierCount)
	}
	protectCurrentSources(items, historySources(rows[2]))

	assertNoPolicy(t, items[0], CompactPolicyPreserveRecent)
	assertNoPolicy(t, items[0], CompactPolicyMustKeep)
	assertNoPolicy(t, items[1], CompactPolicyPreserveRecent)
	assertNoPolicy(t, items[1], CompactPolicyMustKeep)
	assertPolicy(t, items[2], CompactPolicyPreserveRecent)
	assertPolicy(t, items[3], CompactPolicyPreserveToolClosure)
	assertPolicy(t, items[3], CompactPolicyPreserveRecent)
	assertPolicy(t, items[4], CompactPolicyPreserveToolClosure)
	assertPolicy(t, items[4], CompactPolicyPreserveRecent)
	if folded := foldableItems(items); len(folded) != 2 {
		t.Fatalf("folded %d rows, want the two before the current input", len(folded))
	}
}

func TestCandidatesFromRowsKeepsOnlyPendingAskUserCall(t *testing.T) {
	t.Parallel()

	askCall := mkRow(t, "assistant", `[{"type":"tool-call","toolName":"`+userinput.ToolNameAskUser+`","toolCallId":"ask-1","input":{"questions":[]}}]`, 100)
	askResult := mkRow(t, "tool", `[{"type":"tool-result","toolName":"`+userinput.ToolNameAskUser+`","toolCallId":"ask-1","result":{"status":"submitted"}}]`, 100)
	answered, _ := candidatesFromRows([]sqlc.ListUncompactedMessagesBySessionRow{askCall, askResult})
	for _, idx := range []int{0, 1} {
		assertNoPolicy(t, answered[idx], CompactPolicyMustKeep)
		assertPolicy(t, answered[idx], CompactPolicyPreserveToolClosure)
	}
	pending, _ := candidatesFromRows([]sqlc.ListUncompactedMessagesBySessionRow{mkRow(t, "user", `"question"`, 100), askCall})
	assertPolicy(t, pending[1], CompactPolicyMustKeep)
	closed, _ := candidatesFromRows([]sqlc.ListUncompactedMessagesBySessionRow{askCall, mkRow(t, "user", `"current"`, 100)})
	assertNoPolicy(t, closed[0], CompactPolicyMustKeep)
	twoCalls := mkRow(t, "assistant", `[{"type":"tool-call","toolName":"`+userinput.ToolNameAskUser+`","toolCallId":"ask-1","input":{}},{"type":"tool-call","toolName":"`+userinput.ToolNameAskUser+`","toolCallId":"ask-2","input":{}}]`, 100)
	half, _ := candidatesFromRows([]sqlc.ListUncompactedMessagesBySessionRow{twoCalls, askResult})
	for _, idx := range []int{0, 1} {
		assertPolicy(t, half[idx], CompactPolicyMustKeep)
	}
	halfClosed, _ := candidatesFromRows([]sqlc.ListUncompactedMessagesBySessionRow{twoCalls, askResult, mkRow(t, "user", `"current"`, 100)})
	for _, idx := range []int{0, 1} {
		assertNoPolicy(t, halfClosed[idx], CompactPolicyMustKeep)
	}
}

func TestParallelToolCallsPropagateMustKeepToSiblingResult(t *testing.T) {
	t.Parallel()

	// One assistant message carries two parallel tool calls (ask_user + calc)
	// and the run waits on the ask_user one. The row is must-keep because of
	// ask_user, but the calc result is a separate row that only carries the
	// tool-closure policy on its own.
	assistantCall := mkRow(t, "assistant", `[{"type":"tool-call","toolName":"`+userinput.ToolNameAskUser+`","toolCallId":"ask-1","input":{"questions":[]}},`+
		`{"type":"tool-call","toolName":"calc","toolCallId":"calc-1","input":{}}]`, 100)
	calcResult := mkRow(t, "tool", `[{"type":"tool-result","toolName":"calc","toolCallId":"calc-1","output":{"type":"text","value":"42"}}]`, 100)
	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "user", `"question 1"`, 100),
		mkRow(t, "assistant", `"answer 1"`, 100),
		mkRow(t, "user", `"question 2"`, 100),
		assistantCall,
		calcResult,
	}
	items, _ := candidatesFromRows(rows)
	assertPolicy(t, items[4], CompactPolicyMustKeep)

	_, ids := buildEntriesAndIDs(foldBefore(rows))
	if len(ids) != 3 {
		t.Fatalf("marked %d rows, want the three before the exchange the run waits on", len(ids))
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

func TestCandidatesFromRowsPreservesUnparseableRowsAsSelectionBarriers(t *testing.T) {
	t.Parallel()

	before := mkRow(t, "user", `"before"`, 100)
	bad := sqlc.ListUncompactedMessagesBySessionRow{
		ID:      pgtype.UUID{Valid: false}, // empty id -> FromDBMessage rejects it
		Role:    "user",
		Content: json.RawMessage(`"x"`),
	}
	after := mkRow(t, "assistant", `"after"`, 100)
	current := mkRow(t, "user", `"current"`, 100)
	items, barrierCount := candidatesFromRows([]sqlc.ListUncompactedMessagesBySessionRow{before, bad, after, current})
	if barrierCount != 1 {
		t.Fatalf("barrier count = %d, want 1", barrierCount)
	}
	if len(items) != 4 || !items[1].HasPolicy(CompactPolicyMustKeep) {
		t.Fatalf("an unparseable row must remain as a selection barrier: %#v", items)
	}

	protectCurrentSources(items, historySources(current))
	_, ids := buildEntriesAndIDs(foldableItems(items))
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

func TestTrimCompactMessagesDefersNewestBeyondBudget(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		textRow(t, "user", 100),
		textRow(t, "assistant", 100),
		textRow(t, "user", 100),
	}
	items, _ := candidatesFromRows(rows)
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
	items, _ := candidatesFromRows(rows)

	trimmed := trimCompactMessages(items, 375)
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
