package timeline

import (
	"strings"
	"testing"
)

func budgetTestRC(texts ...string) RenderedContext {
	rc := make(RenderedContext, 0, len(texts))
	for i, text := range texts {
		rc = append(rc, RenderedSegment{
			MessageID:    "m" + string(rune('1'+i)),
			ReceivedAtMs: int64(100 * (i + 1)),
			Content:      []RenderedContentPiece{{Type: "text", Text: text}},
		})
	}
	return rc
}

func TestComposeBudgetedUnderBudgetKeepsEverything(t *testing.T) {
	rc := budgetTestRC("hello there", "how are you")
	composed, admission := ComposeContextWithArtifactsBudgeted(rc, nil, nil, ComposeBudget{MaxTokens: 1000})
	if composed == nil {
		t.Fatal("expected a composed result")
	}
	if admission.DroppedEntries != 0 || admission.ProtectedOverflow {
		t.Fatalf("under-budget composition must not drop: %+v", admission)
	}
	full := ComposeContextWithArtifacts(rc, nil, nil)
	if len(composed.Messages) != len(full.Messages) {
		t.Fatalf("budgeted (under budget) diverged from unbudgeted: %d vs %d", len(composed.Messages), len(full.Messages))
	}
}

func TestComposeBudgetedZeroBudgetDisables(t *testing.T) {
	rc := budgetTestRC(strings.Repeat("x", 4096))
	composed, admission := ComposeContextWithArtifactsBudgeted(rc, nil, nil, ComposeBudget{})
	if composed == nil || admission.DroppedEntries != 0 {
		t.Fatalf("zero budget must keep legacy behavior: %+v", admission)
	}
}

func TestComposeBudgetedDropsOldestKeepsNewestAndSummaries(t *testing.T) {
	// Old raw history is large; the artifact summary and the newest message
	// must survive while the oldest raw entries drop.
	old := strings.Repeat("a", 4000) // ~1000 tokens
	mid := strings.Repeat("b", 4000)
	newest := strings.Repeat("c", 400) // ~100 tokens
	trs := []TurnResponseEntry{
		{RequestedAtMs: 100, Role: "assistant", Content: old},
		{RequestedAtMs: 200, Role: "assistant", Content: mid},
	}
	rc := RenderedContext{{
		MessageID:    "m-new",
		ReceivedAtMs: 300,
		Content:      []RenderedContentPiece{{Type: "text", Text: newest}},
	}}
	artifacts := []CompactionArtifact{{ID: "a1", Summary: "earlier conversation summary"}}

	composed, admission := ComposeContextWithArtifactsBudgeted(rc, trs, artifacts, ComposeBudget{MaxTokens: 1200})
	if composed == nil {
		t.Fatalf("expected a composed result, admission %+v", admission)
	}
	if admission.DroppedEntries == 0 {
		t.Fatalf("expected drops, admission %+v", admission)
	}
	var sawSummary, sawNewest bool
	for _, m := range composed.Messages {
		if m.CompactionArtifactID == "a1" {
			sawSummary = true
		}
		if strings.Contains(m.Content, newest) {
			sawNewest = true
		}
		if strings.Contains(m.Content, old) {
			t.Fatal("oldest raw entry must be dropped")
		}
	}
	if !sawSummary || !sawNewest {
		t.Fatalf("summary and newest message must survive: summary=%v newest=%v", sawSummary, sawNewest)
	}
	if admission.SelectedTokens > 1200 {
		t.Fatalf("selected tokens %d exceed budget", admission.SelectedTokens)
	}
}

func TestComposeBudgetedProtectedOverflowFailsClosed(t *testing.T) {
	rc := budgetTestRC(strings.Repeat("x", 40000)) // newest alone ~10k tokens
	composed, admission := ComposeContextWithArtifactsBudgeted(rc, nil, nil, ComposeBudget{MaxTokens: 100})
	if composed != nil {
		t.Fatal("protected overflow must not materialize a result")
	}
	if !admission.ProtectedOverflow {
		t.Fatalf("expected ProtectedOverflow, admission %+v", admission)
	}
}

func TestComposeBudgetedNewestOrphanToolFailsClosed(t *testing.T) {
	// A tool result persisted after the triggering user message sorts newest.
	// When the budget fits only that tool entry, the turn must fail closed
	// with ProtectedOverflow instead of silently skipping or running against
	// a summaries-only context.
	trs := []TurnResponseEntry{
		{RequestedAtMs: 100, Role: "assistant", Content: strings.Repeat("a", 8000)},
		{RequestedAtMs: 400, Role: "tool", Content: strings.Repeat("t", 40)},
	}
	rc := RenderedContext{{
		MessageID:    "m-new",
		ReceivedAtMs: 300,
		Content:      []RenderedContentPiece{{Type: "text", Text: strings.Repeat("c", 400)}},
	}}
	composed, admission := ComposeContextWithArtifactsBudgeted(rc, trs, nil, ComposeBudget{MaxTokens: 50})
	if composed != nil {
		t.Fatal("newest orphan tool response must not materialize a result")
	}
	if !admission.ProtectedOverflow {
		t.Fatalf("expected ProtectedOverflow, admission %+v", admission)
	}
}

func TestComposeBudgetedDropsLeadingOrphanToolResponse(t *testing.T) {
	huge := strings.Repeat("a", 8000)
	trs := []TurnResponseEntry{
		{RequestedAtMs: 100, Role: "assistant", Content: huge},
		{RequestedAtMs: 200, Role: "tool", Content: strings.Repeat("t", 40)},
		{RequestedAtMs: 300, Role: "assistant", Content: strings.Repeat("b", 40)},
	}
	rc := RenderedContext{{
		MessageID:    "m-new",
		ReceivedAtMs: 400,
		Content:      []RenderedContentPiece{{Type: "text", Text: strings.Repeat("c", 40)}},
	}}
	composed, admission := ComposeContextWithArtifactsBudgeted(rc, trs, nil, ComposeBudget{MaxTokens: 200})
	if composed == nil {
		t.Fatalf("expected a composed result, admission %+v", admission)
	}
	for _, m := range composed.Messages {
		if m.Role == "tool" {
			t.Fatal("window must not open on an orphaned tool response")
		}
	}
}
