package timeline

import (
	"reflect"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/turn"
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
	composed, admission := ComposeContextWithCheckpointBudgeted(rc, nil, HistoryCheckpoint{}, ComposeBudget{MaxTokens: 1000})
	if composed == nil {
		t.Fatal("expected a composed result")
	}
	if admission.DroppedEntries != 0 || admission.ProtectedOverflow {
		t.Fatalf("under-budget composition must not drop: %+v", admission)
	}
	full := ComposeContextWithCheckpoint(rc, nil, HistoryCheckpoint{})
	if len(composed.Messages) != len(full.Messages) {
		t.Fatalf("budgeted (under budget) diverged from unbudgeted: %d vs %d", len(composed.Messages), len(full.Messages))
	}
}

func TestComposeBudgetedZeroBudgetDisables(t *testing.T) {
	rc := budgetTestRC(strings.Repeat("x", 4096))
	composed, admission := ComposeContextWithCheckpointBudgeted(rc, nil, HistoryCheckpoint{}, ComposeBudget{})
	if composed == nil || admission.DroppedEntries != 0 {
		t.Fatalf("zero budget must keep legacy behavior: %+v", admission)
	}
}

func TestComposeBudgetedDropsOldestKeepsNewestAndSummaries(t *testing.T) {
	// Old raw history is large; the checkpoint's summary and the newest
	// message must survive while the oldest raw entries drop.
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
	held := HistoryCheckpoint{Items: []CheckpointItem{{SummaryID: "a1", Summary: "earlier conversation summary"}}}

	composed, admission := ComposeContextWithCheckpointBudgeted(rc, trs, held, ComposeBudget{MaxTokens: 1200})
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

// Everything the checkpoint holds is pinned: under a tight budget a kept
// input and a kept tool exchange stay beside the summary, in its order, while
// the older raw history after the checkpoint drops.
func TestComposeBudgetedKeepsWhatTheCheckpointHolds(t *testing.T) {
	call := TurnResponseEntry{RequestedAtMs: 150, Role: "assistant", Content: "calls a tool", SourceMessageID: "h-call"}
	result := TurnResponseEntry{RequestedAtMs: 160, Role: "tool", Content: "tool result", SourceMessageID: "h-result"}
	held := HistoryCheckpoint{
		Items: []CheckpointItem{
			{SummaryID: "a1", Summary: "earlier conversation summary"},
			{MessageID: "m-kept"},
			{Response: &call},
			{Response: &result},
		},
		Messages: map[string]CheckpointMessage{"m-kept": {Kept: true}, "m-new": {Kept: true}},
	}
	trs := []TurnResponseEntry{{RequestedAtMs: 200, Role: "assistant", Content: strings.Repeat("a", 4000), SourceMessageID: "h-old"}}
	rc := RenderedContext{
		{MessageID: "m-kept", ReceivedAtMs: 100, Content: []RenderedContentPiece{{Type: "text", Text: "kept input"}}},
		{MessageID: "m-new", ReceivedAtMs: 300, Content: []RenderedContentPiece{{Type: "text", Text: "newest input"}}},
	}

	composed, admission := ComposeContextWithCheckpointBudgeted(rc, trs, held, ComposeBudget{MaxTokens: 200})
	if composed == nil || admission.ProtectedOverflow || admission.DroppedEntries != 1 {
		t.Fatalf("composed=%v admission=%+v, want only the older raw response dropped", composed, admission)
	}
	want := []string{
		"user:" + turn.SummaryText("earlier conversation summary"),
		"user:kept input",
		"assistant:calls a tool",
		"tool:tool result",
		"user:newest input",
	}
	if got := messageTexts(composed.Messages); !reflect.DeepEqual(got, want) {
		t.Fatalf("composed %q, want %q", got, want)
	}
	for i, message := range composed.Messages {
		if message.Pinned != (i < len(want)-1) {
			t.Fatalf("message %d %q pinned = %v", i, message.Content, message.Pinned)
		}
	}
}

func TestComposeBudgetedProtectedOverflowFailsClosed(t *testing.T) {
	rc := budgetTestRC(strings.Repeat("x", 40000)) // newest alone ~10k tokens
	composed, admission := ComposeContextWithCheckpointBudgeted(rc, nil, HistoryCheckpoint{}, ComposeBudget{MaxTokens: 100})
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
	composed, admission := ComposeContextWithCheckpointBudgeted(rc, trs, HistoryCheckpoint{}, ComposeBudget{MaxTokens: 50})
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
	composed, admission := ComposeContextWithCheckpointBudgeted(rc, trs, HistoryCheckpoint{}, ComposeBudget{MaxTokens: 200})
	if composed == nil {
		t.Fatalf("expected a composed result, admission %+v", admission)
	}
	for _, m := range composed.Messages {
		if m.Role == "tool" {
			t.Fatal("window must not open on an orphaned tool response")
		}
	}
}

func TestComposeProtectsExternalInputBeforeSelfEcho(t *testing.T) {
	rc := budgetTestRC(strings.Repeat("u", 4400), "echo")
	rc[1].IsSelfSent = true
	_, got := ComposeContextWithCheckpointBudgeted(rc, nil, HistoryCheckpoint{}, ComposeBudget{MaxTokens: 1000})
	if !got.ProtectedOverflow {
		t.Fatalf("external input dropped for echo: %+v", got)
	}
}

func TestComposeProtectsEveryUnconsumedExternalInput(t *testing.T) {
	rc := budgetTestRC(strings.Repeat("h", 8000), strings.Repeat("a", 1600), strings.Repeat("b", 1600), "echo")
	rc[3].IsMyself = true
	after := DiscussCursorPosition{SourceCursor: 100}
	composed, admission := ComposeContextWithCheckpointBudgeted(rc, nil, HistoryCheckpoint{}, ComposeBudget{MaxTokens: 810, After: &after})
	if composed == nil || admission.CurrentTokens != 800 || len(composed.Messages) != 3 {
		t.Fatalf("batch selection=%+v result=%+v", admission, composed)
	}
	for _, message := range composed.Messages[:2] {
		if message.Source == nil || !message.Source.Current || message.Source.ID == "" {
			t.Fatalf("lost source: %+v", message)
		}
	}
	composed, admission = ComposeContextWithCheckpointBudgeted(rc, nil, HistoryCheckpoint{}, ComposeBudget{MaxTokens: 700, After: &after})
	if composed == nil || admission.ProtectedOverflow || admission.CurrentTokens != 400 || len(composed.Messages) != 2 || composed.Messages[0].Source.ID != "m3" {
		t.Fatalf("an oversized batch must keep its newest fitting suffix: admission=%+v result=%+v", admission, composed)
	}
	if len(admission.OmittedSources) != 1 || admission.OmittedSources[0].ID != "m2" || !admission.OmittedSources[0].Current {
		t.Fatalf("older unconsumed input must be reported, not silently dropped: %+v", admission.OmittedSources)
	}
}
