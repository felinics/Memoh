package compaction

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func TestBuildEntriesAndIDsBreaksSpanAtSkippedMiddleGroup(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "user", `"first question"`, 0),
		// reasoning-only: renders empty. It must NOT be marked, and because it
		// sits between two rendered rows, it breaks the contiguous compact span:
		// marking row 0 and row 2 under one compact_id would leave this raw row
		// between them, and the read path would fold row 2's summary before it.
		mkRow(t, "assistant", `[{"type":"reasoning","text":"thinking"}]`, 0),
		mkRow(t, "assistant", `"the answer"`, 0),
	}
	items, _ := itemsFromRows(rows)

	entries, ids := buildEntriesAndIDs(items)

	// Only the first contiguous run (row 0) is marked; "the answer" is deferred
	// to a later pass so the marked ids stay contiguous in history.
	if len(ids) != 1 || ids[0] != rows[0].ID {
		t.Fatalf("ids = %#v, want only the leading contiguous row 0 marked", ids)
	}
	if len(entries) != 1 || entries[0].Content != "first question" {
		t.Fatalf("entries = %#v, want only 'first question'", entries)
	}
	for _, e := range entries {
		if strings.TrimSpace(e.Content) == "" {
			t.Fatalf("emitted an empty entry")
		}
	}
}

func TestBuildEntriesAndIDsWithholdsWholeExchangeWhenResultRendersEmpty(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "user", `"question"`, 0),
		toolCallRow(t, 0),
		// degenerate content: still a "tool" role closure row, but has no
		// recognizable parts, so it renders empty.
		mkRow(t, "tool", `[]`, 0),
	}
	items, _ := itemsFromRows(rows)

	entries, ids := buildEntriesAndIDs(items)

	// The incomplete exchange (renderable call, empty-rendering result) is
	// withheld from BOTH the summarizer prompt and the marked ids: summarizing
	// the call while it stays in raw history would duplicate its content.
	if len(entries) != 1 || entries[0].Content != "question" {
		t.Fatalf("entries = %#v, want only the unrelated question", entries)
	}
	if len(ids) != 1 || ids[0] != rows[0].ID {
		t.Fatalf("ids = %#v, want only the unrelated question row marked", ids)
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
	items, _ := itemsFromRows(rows)

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
		{name: "call is unparseable", breakIndex: 0},
		{name: "result is unparseable", breakIndex: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := []sqlc.ListUncompactedMessagesBySessionRow{
				toolCallRow(t, 100),
				toolResultRow(t, 100),
				mkRow(t, "user", `"old question"`, 100),
				mkRow(t, "assistant", `"old answer"`, 100),
				mkRow(t, "user", `"current question"`, 100),
				mkRow(t, "assistant", `"current answer"`, 100),
			}
			rows[tt.breakIndex].ID = pgtype.UUID{Valid: false}

			items, barrierCount := itemsFromRows(rows)
			if barrierCount != 1 {
				t.Fatalf("barrier count = %d, want 1", barrierCount)
			}
			selected := splitByTarget(items, 200)
			entries, ids := buildEntriesAndIDs(selected)

			if len(ids) != 2 || ids[0] != rows[2].ID || ids[1] != rows[3].ID {
				t.Fatalf("ids = %#v, want only the complete run after the protected tool exchange", ids)
			}
			if len(entries) != 2 {
				t.Fatalf("entries = %#v, want the old question and answer", entries)
			}
		})
	}
}

func TestBuildUserPromptPriorContextBranch(t *testing.T) {
	t.Parallel()

	entries := []messageEntry{{Role: "user", Content: "hi"}}

	withPrior := buildUserPrompt([]string{"earlier summary"}, entries)
	if !strings.Contains(withPrior, "<prior_context>") || !strings.Contains(withPrior, "earlier summary") {
		t.Fatalf("prior-summary branch not rendered: %q", withPrior)
	}
	if !strings.Contains(withPrior, "user: hi") {
		t.Fatalf("entries not included with prior context: %q", withPrior)
	}

	without := buildUserPrompt(nil, entries)
	if strings.Contains(without, "<prior_context>") {
		t.Fatalf("no-prior branch should omit prior context: %q", without)
	}
	if !strings.Contains(without, "Summarize the following conversation:") {
		t.Fatalf("no-prior branch missing lead-in: %q", without)
	}
}
