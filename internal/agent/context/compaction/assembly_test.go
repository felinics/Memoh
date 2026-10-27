package compaction

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func TestBuildEntriesAndIDsCoversMiddleRowThatRendersNothing(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "user", `"first question"`, 0),
		// reasoning-only: renders empty. It is covered with its neighbours so
		// the span stays one contiguous range instead of two short fragments.
		mkRow(t, "assistant", `[{"type":"reasoning","text":"thinking"}]`, 0),
		mkRow(t, "assistant", `"the answer"`, 0),
	}
	items, _ := candidatesFromRows(rows)

	entries, ids := buildEntriesAndIDs(items)

	if len(ids) != 3 || ids[0] != rows[0].ID || ids[1] != rows[1].ID || ids[2] != rows[2].ID {
		t.Fatalf("ids = %#v, want the whole contiguous span marked", ids)
	}
	if len(entries) != 2 || entries[0].Content != "first question" || entries[1].Content != "the answer" {
		t.Fatalf("entries = %#v, want the two rendered rows", entries)
	}
	for _, e := range entries {
		if strings.TrimSpace(e.Content) == "" {
			t.Fatalf("emitted an empty entry")
		}
	}
}

func TestBuildEntriesAndIDsCoversWholeExchangeWhenResultRendersEmpty(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "user", `"question"`, 0),
		toolCallRow(t, 0),
		// degenerate content: still a "tool" role closure row, but has no
		// recognizable parts, so it renders empty.
		mkRow(t, "tool", `[]`, 0),
	}
	items, _ := candidatesFromRows(rows)

	entries, ids := buildEntriesAndIDs(items)

	// The exchange is covered whole: the call is summarized and its empty
	// result goes with it, so neither stays behind as an orphan in raw history.
	if len(entries) != 2 || entries[0].Content != "question" {
		t.Fatalf("entries = %#v, want the question and the tool call", entries)
	}
	if len(ids) != 3 {
		t.Fatalf("ids = %#v, want the question and both exchange rows marked", ids)
	}
}

func TestBuildEntriesAndIDsRendersLegacyToolResultEnvelopeExchange(t *testing.T) {
	t.Parallel()

	// The tool row uses the older OpenAI-style envelope (tool_call_id at the
	// top level, content is the result payload directly), which previously
	// rendered empty and withheld the whole exchange from marking.
	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		toolCallRow(t, 0),
		mkRow(t, "tool", `{"role":"tool","tool_call_id":"c1","content":{"output":"search results here"}}`, 0),
	}
	items, _ := candidatesFromRows(rows)

	entries, ids := buildEntriesAndIDs(items)

	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2 (call marker + legacy tool result)", len(entries))
	}
	if !strings.Contains(entries[1].Content, "search results here") {
		t.Fatalf("legacy tool result content lost: %+v", entries)
	}
	if len(ids) != 2 || ids[0] != rows[0].ID || ids[1] != rows[1].ID {
		t.Fatalf("ids = %#v, want both exchange rows marked", ids)
	}
}

func TestBuildEntriesAndIDsKeepsToolExchangeAcrossUnparseableBarrier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		breakIndex int
	}{
		{name: "call is unparseable", breakIndex: 2},
		{name: "result is unparseable", breakIndex: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := []sqlc.ListUncompactedMessagesBySessionRow{
				mkRow(t, "user", `"old question"`, 100),
				mkRow(t, "assistant", `"old answer"`, 100),
				toolCallRow(t, 100),
				toolResultRow(t, 100),
				mkRow(t, "user", `"current question"`, 100),
				mkRow(t, "assistant", `"current answer"`, 100),
			}
			rows[tt.breakIndex].ID = pgtype.UUID{Valid: false}

			items, barrierCount := candidatesFromRows(rows)
			if barrierCount != 1 {
				t.Fatalf("barrier count = %d, want 1", barrierCount)
			}
			protectCurrentSources(items, historySources(rows[4]))
			entries, ids := buildEntriesAndIDs(foldableItems(items))

			if len(ids) != 2 || ids[0] != rows[0].ID || ids[1] != rows[1].ID {
				t.Fatalf("ids = %#v, want only the run before the protected tool exchange", ids)
			}
			if len(entries) != 2 {
				t.Fatalf("entries = %#v, want the old question and answer", entries)
			}
		})
	}
}

// Selection prices a kept entry in its wrapper, as the prompt renders it, so
// what it selects never costs more than the limit it was given.
func TestTrimCompactMessagesPricesKeptEntriesAsRendered(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "assistant", `"old work"`, 0),
		mkRow(t, "user", `"kept input"`, 0),
		mkRow(t, "assistant", `"later work"`, 0),
	}
	items, _ := candidatesFromRows(rows)
	markAnchors(items, historySources(rows[1]))
	all, _ := buildEntriesAndIDs(items)
	for limit := 0; limit <= entriesPromptCost(all); limit++ {
		selected, _ := buildEntriesAndIDs(trimCompactMessages(items, limit))
		if cost := entriesPromptCost(selected); cost > limit {
			t.Fatalf("limit=%d: selected %d entries costing %d", limit, len(selected), cost)
		}
	}
}

// Kept entries after the last one a summary covers stay after the summary in
// the request; the summarizer reads them there, unwrapped.
func TestKeptAfterSummarySplitsTrailingKeptEntries(t *testing.T) {
	t.Parallel()

	entries := []messageEntry{
		{Role: "tool", Content: "first result"},
		{Role: "user", Content: "steer", Kept: true},
		{Role: "tool", Content: "second result"},
		{Role: "user", Content: "later steer", Kept: true},
	}
	conversation, after := keptAfterSummary(entries)
	if len(conversation) != 3 || !conversation[1].Kept || conversation[2].Content != "second result" {
		t.Fatalf("conversation = %+v, want the entries through the last covered one", conversation)
	}
	if len(after) != 1 || after[0].Kept || after[0].Content != "later steer" {
		t.Fatalf("after = %+v, want the trailing steer unwrapped", after)
	}
}
