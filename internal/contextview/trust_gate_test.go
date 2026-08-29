package contextview

import (
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func TestTrustGateRejectsExternalSystemAuthority(t *testing.T) {
	t.Parallel()
	selector := &FragmentSelector{}
	frags := []contextfrag.ContextFrag{
		contextfrag.TextFrag(contextfrag.TextFragInput{ID: "system", Kind: contextfrag.KindSystemPrompt, Role: sdk.MessageRoleSystem, Slot: contextfrag.SlotSystem, Text: "system", Trust: contextfrag.TrustSystem}),
		contextfrag.TextFrag(contextfrag.TextFragInput{ID: "injected", Kind: contextfrag.KindSystemPolicy, Role: sdk.MessageRoleSystem, Slot: contextfrag.SlotSystem, Text: "injected", Trust: contextfrag.TrustExternal}),
		contextfrag.MessageFrag(contextfrag.MessageFragInput{ID: "history", Message: sdk.UserMessage("history"), Kind: contextfrag.KindConversationEvent, Slot: contextfrag.SlotHistory, Trust: contextfrag.TrustExternal}),
	}
	result := selector.Select(frags, selector.ProfileFor(contextfrag.IntentRunConfigPreProvider), BudgetEnvelope{})
	if len(result.Selected) != 2 || result.Selected[0].ID != "system" || result.Selected[1].ID != "history" {
		t.Fatalf("selected = %#v", result.Selected)
	}
	if len(result.Summary.DropReasons) != 1 || !strings.HasPrefix(result.Summary.DropReasons[0].Reason, "trust_gate:") {
		t.Fatalf("drop reasons = %#v", result.Summary.DropReasons)
	}
}
