package contextview

import (
	"context"
	"encoding/json"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
)

func pinnedBudgetTestFrags(currentTokens int) []contextfrag.ContextFrag {
	system := contextfrag.TextFrag(contextfrag.TextFragInput{
		ID:            "system.required",
		Kind:          contextfrag.KindSystemPrompt,
		Role:          sdk.MessageRoleSystem,
		Slot:          contextfrag.SlotSystem,
		Text:          "required system",
		RetentionTier: contextfrag.RetentionRequired,
		Trust:         contextfrag.TrustSystem,
	})
	system.TokenEstimate = 100
	frags := []contextfrag.ContextFrag{system}
	for i := range 5 {
		row := contextfrag.TextFrag(contextfrag.TextFragInput{
			ID:    "history." + string(rune('a'+i)),
			Kind:  contextfrag.KindConversationEvent,
			Role:  sdk.MessageRoleUser,
			Slot:  contextfrag.SlotHistory,
			Text:  "history row",
			Trust: contextfrag.TrustExternal,
		})
		row.TokenEstimate = 1000
		frags = append(frags, row)
	}
	current := contextfrag.TextFrag(contextfrag.TextFragInput{
		ID:    "current",
		Kind:  contextfrag.KindCurrentUserMessage,
		Role:  sdk.MessageRoleUser,
		Slot:  contextfrag.SlotCurrentUser,
		Text:  "current",
		Trust: contextfrag.TrustUser,
	})
	current.TokenEstimate = currentTokens
	return append(frags, current)
}

func marshalMessages(t *testing.T, msgs []sdk.Message) []byte {
	t.Helper()
	raw, err := json.Marshal(msgs)
	if err != nil {
		t.Fatalf("marshal messages: %v", err)
	}
	return raw
}

// Spec rule 4: within an epoch the history budget is pinned. A much larger
// current request on a later turn must not shrink the history budget, so the
// selected history — the provider request prefix — stays byte-identical.
func TestPinnedHistoryBudgetStabilizesSelectionAcrossTurns(t *testing.T) {
	t.Parallel()

	const window = 10000 // output reserve resolves to 2048

	// Turn 1: unpinned — the computed budget becomes the recorded pin.
	var pinned int
	turn1, err := applyProviderRunConfig(context.Background(), nil, agentpkg.RunConfig{
		ContextSourceFrags:     pinnedBudgetTestFrags(100),
		ContextBudgetMaxTokens: window,
		RecordHistoryBudgetPin: func(_ context.Context, tokens int) { pinned = tokens },
	})
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	plan1 := turn1.ContextManifest.BudgetPlan
	if plan1 == nil || plan1.HistoryBudgetPinned {
		t.Fatalf("turn 1 plan pinned=%v, want the computed budget", plan1)
	}
	// SystemBudget = 10000-2500-100 = 7400; minus actual system cost 100.
	if want := 7400 - 100; plan1.HistoryBudget != want || pinned != want {
		t.Fatalf("turn 1 history budget = %d, pinned = %d, want %d", plan1.HistoryBudget, pinned, want)
	}

	// Turn 2: the current request balloons to 3000 tokens. Unpinned, the
	// history budget would shrink to 10000-2500-3000-100 = 4400 and force
	// history rows out; the pin keeps the first turn's 7300.
	turn2, err := applyProviderRunConfig(context.Background(), nil, agentpkg.RunConfig{
		ContextSourceFrags:        pinnedBudgetTestFrags(3000),
		ContextBudgetMaxTokens:    window,
		PinnedHistoryBudgetTokens: pinned,
		RecordHistoryBudgetPin:    func(_ context.Context, _ int) { t.Error("pin must not be re-recorded inside the epoch") },
	})
	if err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	plan2 := turn2.ContextManifest.BudgetPlan
	if plan2 == nil || !plan2.HistoryBudgetPinned || plan2.HistoryBudget != pinned {
		t.Fatalf("turn 2 plan = %#v, want pinned budget %d", plan2, pinned)
	}

	// Both turns selected the same history: everything but the trailing
	// current user message is byte-identical.
	if len(turn1.Messages) == 0 || len(turn2.Messages) != len(turn1.Messages) {
		t.Fatalf("message counts diverged: %d vs %d", len(turn1.Messages), len(turn2.Messages))
	}
	before := marshalMessages(t, turn1.Messages[:len(turn1.Messages)-1])
	after := marshalMessages(t, turn2.Messages[:len(turn2.Messages)-1])
	if string(before) != string(after) {
		t.Fatalf("history prefix drifted under a larger current request:\nbefore %s\nafter  %s", before, after)
	}
}

// A pinned budget that no longer fits the shrunken window (e.g. after a
// model switch) must not overflow every turn: the computed budget takes over
// until the next explicit epoch transition re-pins.
func TestPinnedHistoryBudgetExceededFallsBackToComputed(t *testing.T) {
	t.Parallel()

	out, err := applyProviderRunConfig(context.Background(), nil, agentpkg.RunConfig{
		ContextSourceFrags:        pinnedBudgetTestFrags(100),
		ContextBudgetMaxTokens:    3000, // reserve 750 -> SystemBudget 2150
		PinnedHistoryBudgetTokens: 9000,
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	plan := out.ContextManifest.BudgetPlan
	if plan == nil || plan.HistoryBudgetPinned {
		t.Fatalf("plan = %#v, want the pin refused", plan)
	}
	if plan.HistoryBudget >= 9000 {
		t.Fatalf("history budget = %d, want the computed budget below the refused pin", plan.HistoryBudget)
	}
}
