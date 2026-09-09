package contextview

import (
	"context"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func TestACPSelectorProfileKeepsCurrentUserButNotDocumentSections(t *testing.T) {
	t.Parallel()

	profile := (&FragmentSelector{}).ProfileFor(contextfrag.IntentExternalAgentPrompt)

	if profile.Intent != contextfrag.IntentExternalAgentPrompt {
		t.Fatalf("Intent = %q, want %q", profile.Intent, contextfrag.IntentExternalAgentPrompt)
	}
	if acpProfileHasSlot(profile, contextfrag.SlotSystem) {
		t.Fatalf("MustKeepSlots = %#v, system is document position and must retain per-section budgets", profile.MustKeepSlots)
	}
	if !acpProfileHasSlot(profile, contextfrag.SlotCurrentUser) {
		t.Fatalf("MustKeepSlots = %#v, want current_user", profile.MustKeepSlots)
	}
}

func TestACPSelectorRetainsAllUnbudgetedFragments(t *testing.T) {
	t.Parallel()

	frags := []contextfrag.ContextFrag{
		contextfrag.TextFrag(contextfrag.TextFragInput{
			ID: "system", Kind: contextfrag.KindRuntimeContext, Role: sdk.MessageRoleSystem,
			Slot: contextfrag.SlotSystem, Text: "system", Trust: contextfrag.TrustExternal,
		}),
		contextfrag.MessageFrag(contextfrag.MessageFragInput{
			ID: "old-user", Message: sdk.UserMessage("old question"), Kind: contextfrag.KindConversationEvent,
			Slot: contextfrag.SlotHistory, Trust: contextfrag.TrustUser,
		}),
		contextfrag.MessageFrag(contextfrag.MessageFragInput{
			ID: "old-assistant", Message: sdk.AssistantMessage("old answer"), Kind: contextfrag.KindConversationEvent,
			Slot: contextfrag.SlotHistory, Trust: contextfrag.TrustSystem,
		}),
		contextfrag.TextFrag(contextfrag.TextFragInput{
			ID: "current", Kind: contextfrag.KindCurrentUserMessage, Role: sdk.MessageRoleUser,
			Slot: contextfrag.SlotCurrentUser, Text: "latest question", Trust: contextfrag.TrustUser,
		}),
	}
	selector := &FragmentSelector{}
	result := selector.Select(frags, selector.ProfileFor(contextfrag.IntentExternalAgentPrompt), BudgetEnvelope{})

	if len(result.Selected) != len(frags) || len(result.Dropped) != 0 {
		t.Fatalf("selected = %d dropped = %d, want all %d fragments", len(result.Selected), len(result.Dropped), len(frags))
	}
	for i := range frags {
		if result.Selected[i].ID != frags[i].ID {
			t.Fatalf("selected[%d].ID = %q, want %q", i, result.Selected[i].ID, frags[i].ID)
		}
	}
}

func TestRuntimeSectionsCollectorMapsSectionMetadata(t *testing.T) {
	t.Parallel()

	frags, err := (&RuntimeSectionsCollector{}).Collect(context.Background(), CollectRequest{
		Scope:  contextfrag.Scope{BotID: "bot-1"},
		Intent: contextfrag.IntentExternalAgentPrompt,
		Config: RuntimeSectionsConfig{Sections: []RuntimeSection{
			{
				ID: "runtime.preamble", Text: "# Memoh Runtime Context", Kind: contextfrag.KindSystemPolicy,
				Priority: 10, CacheClass: contextfrag.CacheStable,
				Budget: contextfrag.BudgetPolicy{Overflow: contextfrag.OverflowKeep},
			},
			{
				ID: "runtime.section.file.000", Text: "## Bot Soul\n\nsoul text",
				Kind: contextfrag.KindWorkspaceInstruction, Trust: contextfrag.TrustWorkspace,
			},
			{ID: "runtime.section.runtime-notes", Text: "notes"},
		}},
	})
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(frags) != 3 {
		t.Fatalf("frags = %d, want 3", len(frags))
	}

	preamble := frags[0]
	if preamble.Kind != contextfrag.KindSystemPolicy || preamble.Priority != 10 ||
		preamble.CacheClass != contextfrag.CacheStable || preamble.Budget.Overflow != contextfrag.OverflowKeep {
		t.Fatalf("preamble metadata = %+v", preamble)
	}
	file := frags[1]
	if file.Kind != contextfrag.KindWorkspaceInstruction || file.Trust != contextfrag.TrustWorkspace {
		t.Fatalf("file metadata = kind:%s trust:%s", file.Kind, file.Trust)
	}
	defaulted := frags[2]
	if defaulted.Kind != contextfrag.KindRuntimeContext || defaulted.Trust != contextfrag.TrustSystem ||
		defaulted.Priority != 35 || defaulted.CacheClass != contextfrag.CacheDynamic {
		t.Fatalf("defaulted metadata = %+v", defaulted)
	}
}

func acpProfileHasSlot(profile IntentProfile, slot contextfrag.Slot) bool {
	for _, candidate := range profile.MustKeepSlots {
		if candidate == slot {
			return true
		}
	}
	return false
}
