package contextview

import (
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func TestStablePrefixTokenEstimateCoversToolDefsAndPlacedPrefix(t *testing.T) {
	t.Parallel()
	frags := []contextfrag.ContextFrag{
		systemTextFrag("system", "system", contextfrag.KindSystemPrompt, 20),
		stableHistoryMessageFrag("history", sdk.UserMessage("history")),
		currentMessageFrag("current", "current"),
	}
	placement := StablePrefixPlacer{}.Place(frags, contextfrag.IntentRunConfigPreProvider)
	defs := []contextfrag.ToolDefAccounting{{TokenEstimate: 11}, {TokenEstimate: 7}}
	want := 18 + contextfrag.ResolveFragTokens(frags[0]) + contextfrag.ResolveFragTokens(frags[1])
	if got := stablePrefixTokenEstimate(placement, frags, defs); got != want {
		t.Fatalf("estimate = %d, want %d", got, want)
	}
}

func TestStablePrefixTokenEstimateEmptyPlacementCountsTools(t *testing.T) {
	t.Parallel()
	if got := stablePrefixTokenEstimate(PlacementPlan{}, nil, []contextfrag.ToolDefAccounting{{TokenEstimate: 9}}); got != 9 {
		t.Fatalf("estimate = %d", got)
	}
}

func stableSpanFixture(counts []int) (PlacementPlan, []contextfrag.ContextFrag) {
	system := contextfrag.TextFrag(contextfrag.TextFragInput{
		ID: "system.prompt", Kind: contextfrag.KindSystemPrompt, Slot: contextfrag.SlotSystem,
		CacheClass: contextfrag.CacheStable, Text: strings.Repeat("s", 400),
	})
	placement := PlacementPlan{Items: []PlacementItem{{FragID: system.ID, Slot: system.Slot, CacheHint: contextfrag.CacheStable}}}
	selected := []contextfrag.ContextFrag{system}
	for i, tokens := range counts {
		frag := contextfrag.MessageFrag(contextfrag.MessageFragInput{
			ID:      "history.db_message.m" + string(rune('1'+i)),
			Message: sdk.Message{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.TextPart{Text: "x"}}},
			Kind:    contextfrag.KindConversationEvent,
			Slot:    contextfrag.SlotHistory,
		})
		frag.TokenEstimate = tokens
		placement.Items = append(placement.Items, PlacementItem{FragID: frag.ID, Slot: frag.Slot, CacheHint: contextfrag.CacheStable})
		selected = append(selected, frag)
	}
	placement.FirstVolatileIndex = len(placement.Items)
	return placement, selected
}

func TestMidStableMessageCountSplitsLargeSpans(t *testing.T) {
	t.Parallel()

	placement, selected := stableSpanFixture([]int{1000, 1000, 1000, 1000})
	if got := midStableMessageCount(placement, selected); got != 2 {
		t.Fatalf("midStableMessageCount = %d, want 2 (half of the 4000-token span)", got)
	}
}

func TestMidStableMessageCountSkipsSmallSpans(t *testing.T) {
	t.Parallel()

	placement, selected := stableSpanFixture([]int{300, 300})
	if got := midStableMessageCount(placement, selected); got != 0 {
		t.Fatalf("midStableMessageCount = %d, want 0 for spans below the insurance threshold", got)
	}
}

func TestMidStableMessageCountNeverEqualsFullSpan(t *testing.T) {
	t.Parallel()

	placement, selected := stableSpanFixture([]int{4000})
	if got := midStableMessageCount(placement, selected); got != 0 {
		t.Fatalf("midStableMessageCount = %d, want 0 when the midpoint would duplicate the tail breakpoint", got)
	}
}
