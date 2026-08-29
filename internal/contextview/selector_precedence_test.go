package contextview

import (
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func conflictFrag(id, key string, trust contextfrag.TrustLevel, scope contextfrag.Scope) contextfrag.ContextFrag {
	return contextfrag.TextFrag(contextfrag.TextFragInput{
		ID:          id,
		Kind:        contextfrag.KindSystemPolicy,
		Role:        sdk.MessageRoleSystem,
		Slot:        contextfrag.SlotSystem,
		Text:        id,
		Trust:       trust,
		Scope:       scope,
		Source:      "test",
		Collector:   "test",
		ConflictKey: key,
	})
}

func TestConflictGroupClosestScopeWins(t *testing.T) {
	t.Parallel()
	selector := &FragmentSelector{}
	frags := []contextfrag.ContextFrag{
		conflictFrag("policy.bot", "policy", contextfrag.TrustSystem, contextfrag.Scope{BotID: "b"}),
		conflictFrag("policy.session", "policy", contextfrag.TrustSystem, contextfrag.Scope{BotID: "b", ChatID: "c", SessionID: "s"}),
		conflictFrag("other", "", contextfrag.TrustSystem, contextfrag.Scope{BotID: "b"}),
	}
	result := selector.Select(frags, selector.ProfileFor(contextfrag.IntentRunConfigPreProvider), BudgetEnvelope{})
	if len(result.Selected) != 2 || result.Selected[0].ID != "policy.session" || result.Selected[1].ID != "other" {
		t.Fatalf("selected = %#v", result.Selected)
	}
	if len(result.Summary.DropReasons) != 1 || !strings.HasPrefix(result.Summary.DropReasons[0].Reason, "precedence:") {
		t.Fatalf("drop reasons = %#v", result.Summary.DropReasons)
	}
}

func TestConflictGroupTrustThenCollectionBreakTies(t *testing.T) {
	t.Parallel()
	selector := &FragmentSelector{}
	scope := contextfrag.Scope{BotID: "b", SessionID: "s"}
	result := selector.Select([]contextfrag.ContextFrag{
		conflictFrag("workspace", "identity", contextfrag.TrustWorkspace, scope),
		conflictFrag("system.old", "identity", contextfrag.TrustSystem, scope),
		conflictFrag("system.new", "identity", contextfrag.TrustSystem, scope),
	}, selector.ProfileFor(contextfrag.IntentRunConfigPreProvider), BudgetEnvelope{})
	if len(result.Selected) != 1 || result.Selected[0].ID != "system.new" {
		t.Fatalf("selected = %#v", result.Selected)
	}
}
