package contextview

import (
	"context"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func TestProviderSystemMustKeepUsesPerFragmentPolicy(t *testing.T) {
	t.Parallel()

	intents := []contextfrag.Intent{
		contextfrag.IntentRunConfigPreProvider,
		contextfrag.IntentDiscussReply,
	}
	tiers := []contextfrag.RetentionTier{
		contextfrag.RetentionUnspecified,
		contextfrag.RetentionRequired,
		contextfrag.RetentionPreferred,
		contextfrag.RetentionOptional,
	}
	selector := &FragmentSelector{}
	for _, intent := range intents {
		profile := selector.ProfileFor(intent)
		if slotInMustKeepSlots(profile, contextfrag.SlotSystem) {
			t.Fatalf("%s MustKeepSlots = %#v, system must use the per-fragment seam", intent, profile.MustKeepSlots)
		}
		if !slotInMustKeepSlots(profile, contextfrag.SlotCurrentUser) {
			t.Fatalf("%s MustKeepSlots = %#v, want current_user", intent, profile.MustKeepSlots)
		}
		for _, tier := range tiers {
			frag := contextfrag.ContextFrag{Slot: contextfrag.SlotSystem, RetentionTier: tier}
			if !isMustKeepFrag(frag, profile) {
				t.Fatalf("%s system retention %q must remain kept before the system-budget pass exists", intent, tier)
			}
		}
	}
}

func TestProviderHistoryBudgetNeverDropsSystemFragments(t *testing.T) {
	t.Parallel()

	intents := []contextfrag.Intent{
		contextfrag.IntentRunConfigPreProvider,
		contextfrag.IntentDiscussReply,
	}
	tiers := []contextfrag.RetentionTier{
		contextfrag.RetentionUnspecified,
		contextfrag.RetentionRequired,
		contextfrag.RetentionPreferred,
		contextfrag.RetentionOptional,
	}
	selector := &FragmentSelector{}
	for _, intent := range intents {
		for _, tier := range tiers {
			system := contextfrag.TextFrag(contextfrag.TextFragInput{
				ID: "system", Kind: contextfrag.KindSystemPrompt, Role: sdk.MessageRoleSystem,
				Slot: contextfrag.SlotSystem, Text: "oversized system", RetentionTier: tier,
				Trust:  contextfrag.TrustSystem,
				Budget: contextfrag.BudgetPolicy{MaxChars: 1, Overflow: contextfrag.OverflowDrop},
			})
			history := contextfrag.TextFrag(contextfrag.TextFragInput{
				ID: "history", Kind: contextfrag.KindConversationEvent, Role: sdk.MessageRoleUser,
				Slot: contextfrag.SlotHistory, Text: "oversized history", Trust: contextfrag.TrustExternal,
				Budget: contextfrag.BudgetPolicy{MaxChars: 1, Overflow: contextfrag.OverflowDrop},
			})
			result := selector.Select(
				[]contextfrag.ContextFrag{system, history},
				selector.ProfileFor(intent),
				BudgetEnvelope{},
			)

			if !containsFragID(result.Selected, "system") {
				t.Fatalf("%s system retention %q was dropped: %#v", intent, tier, result.Dropped)
			}
			if !containsFragID(result.Dropped, "history") {
				t.Fatalf("%s retention %q did not exercise droppable history: %#v", intent, tier, result)
			}
		}
	}
}

func TestLegacySystemReverseParsersStampRequired(t *testing.T) {
	t.Parallel()

	const toolUsage = "## Tool usage\nUse tools carefully."
	const system = "Base system prompt.\n\n" + toolUsage + "\n\nTail guidance."
	collected, err := (&SystemPromptCollector{}).Collect(context.Background(), CollectRequest{
		Intent: contextfrag.IntentRunConfigPreProvider,
		Config: SystemPromptConfig{System: system, ToolUsage: toolUsage},
	})
	if err != nil {
		t.Fatalf("collect system prompt: %v", err)
	}
	compiled := contextfrag.CompileFrags(contextfrag.CompileInput{System: system, ToolUsage: toolUsage})
	for name, frags := range map[string][]contextfrag.ContextFrag{
		"collector": collected,
		"compiler":  compiled,
	} {
		if len(frags) != 3 {
			t.Fatalf("%s fragments = %d, want 3", name, len(frags))
		}
		for _, frag := range frags {
			if frag.RetentionTier != contextfrag.RetentionRequired {
				t.Fatalf("%s fragment %s retention = %q, want required", name, frag.ID, frag.RetentionTier)
			}
		}
	}

	raw, err := (&SystemPromptCollector{}).Collect(context.Background(), CollectRequest{
		Intent: contextfrag.IntentRunConfigPreProvider,
		Config: SystemPromptConfig{System: "  byte-exact system \n"},
	})
	if err != nil {
		t.Fatalf("collect byte-exact system prompt: %v", err)
	}
	if len(raw) != 1 || raw[0].RetentionTier != contextfrag.RetentionRequired {
		t.Fatalf("byte-exact fallback fragments = %#v, want one required fragment", raw)
	}
}

func TestToolUsageFragIsPreferred(t *testing.T) {
	t.Parallel()

	frag := ToolUsageFrag("use tools", contextfrag.Scope{})
	if frag.RetentionTier != contextfrag.RetentionPreferred {
		t.Fatalf("tool usage retention = %q, want preferred", frag.RetentionTier)
	}
}

func slotInMustKeepSlots(profile IntentProfile, slot contextfrag.Slot) bool {
	for _, candidate := range profile.MustKeepSlots {
		if candidate == slot {
			return true
		}
	}
	return false
}

func containsFragID(frags []contextfrag.ContextFrag, id string) bool {
	for _, frag := range frags {
		if frag.ID == id {
			return true
		}
	}
	return false
}
