package contextview

import (
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
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
