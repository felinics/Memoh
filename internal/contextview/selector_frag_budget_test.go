package contextview

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func TestFragBudgetDropsOversizedFragEvenWithoutEnvelopeBudget(t *testing.T) {
	t.Parallel()
	selector := &FragmentSelector{}
	oversized := messageFrag("oversized", sdk.AssistantMessage(strings.Repeat("x", 400)))
	oversized.Budget = contextfrag.BudgetPolicy{MaxTokens: 1, Overflow: contextfrag.OverflowDrop}
	result := selector.Select([]contextfrag.ContextFrag{
		messageFrag("plain", sdk.AssistantMessage("plain content")),
		oversized,
	}, selector.ProfileFor(contextfrag.IntentRunConfigPreProvider), BudgetEnvelope{})
	if len(result.Selected) != 1 || result.Selected[0].ID != "plain" || len(result.Dropped) != 1 || result.Summary.DropReasons[0].Reason != "frag_budget:max_tokens" {
		t.Fatalf("result = %#v", result)
	}
}

func TestFragBudgetTrimsPureTextFragOverMaxChars(t *testing.T) {
	t.Parallel()
	selector := &FragmentSelector{}
	frag := textFrag("long", contextfrag.SlotHistory, contextfrag.KindConversationEvent, sdk.MessageRoleAssistant, strings.Repeat("a", 50))
	frag.Budget = contextfrag.BudgetPolicy{MaxChars: 30, Overflow: contextfrag.OverflowTrim}
	result := selector.Select([]contextfrag.ContextFrag{frag}, selector.ProfileFor(contextfrag.IntentRunConfigPreProvider), BudgetEnvelope{})
	if len(result.Selected) != 1 || len(result.Edited) != 1 {
		t.Fatalf("result = %#v", result)
	}
	text := result.Selected[0].Parts[0].Text
	if !strings.HasPrefix(text, strings.Repeat("a", 7)) || !strings.Contains(text, "[trimmed from 50 bytes]") || utf8.RuneCountInString(text) > frag.Budget.MaxChars {
		t.Fatalf("trimmed text = %q", text)
	}
}

func TestFragBudgetTrimRefreshesAccountingAndHash(t *testing.T) {
	t.Parallel()
	selector := &FragmentSelector{}
	frag := textFrag("trimmed", contextfrag.SlotHistory, contextfrag.KindConversationEvent, sdk.MessageRoleAssistant, strings.Repeat("z", 80))
	frag.Budget = contextfrag.BudgetPolicy{MaxChars: 30, Overflow: contextfrag.OverflowTrim}
	frag.TokenEstimate = 999
	frag = contextfrag.WithContextRef(frag, frag.Ref)
	originalHash := frag.Ref.ContentHash
	result := selector.Select([]contextfrag.ContextFrag{frag}, selector.ProfileFor(contextfrag.IntentRunConfigPreProvider), BudgetEnvelope{})
	if len(result.Selected) != 1 || len(result.Edited) != 1 {
		t.Fatalf("result = %#v", result)
	}
	trimmed := result.Selected[0]
	if trimmed.TokenEstimate != 0 {
		t.Fatalf("token estimate = %d, want recomputation", trimmed.TokenEstimate)
	}
	if trimmed.Ref.ContentHash == "" || trimmed.Ref.ContentHash == originalHash {
		t.Fatalf("content hash = %q, original = %q", trimmed.Ref.ContentHash, originalHash)
	}
	if len(trimmed.Parts[0].Text) > frag.Budget.MaxChars {
		t.Fatalf("trimmed text = %q", trimmed.Parts[0].Text)
	}
}

func TestFragBudgetKeepsMustKeepSlotAndWarns(t *testing.T) {
	t.Parallel()
	selector := &FragmentSelector{}
	frag := textFrag("current", contextfrag.SlotCurrentUser, contextfrag.KindCurrentUserMessage, sdk.MessageRoleUser, strings.Repeat("u", 40))
	frag.Budget = contextfrag.BudgetPolicy{MaxChars: 1, Overflow: contextfrag.OverflowDrop}
	result := selector.Select([]contextfrag.ContextFrag{frag}, selector.ProfileFor(contextfrag.IntentRunConfigPreProvider), BudgetEnvelope{})
	if len(result.Selected) != 1 || len(result.Warnings) != 1 || result.Warnings[0].Code != "frag_budget_drop_blocked_must_keep" {
		t.Fatalf("result = %#v", result)
	}
}

func TestFragBudgetTrimRespectsMaxTokensIncludingMarker(t *testing.T) {
	t.Parallel()
	selector := &FragmentSelector{}
	frag := textFrag("long", contextfrag.SlotHistory, contextfrag.KindConversationEvent, sdk.MessageRoleAssistant, strings.Repeat("z", 200))
	frag.Budget = contextfrag.BudgetPolicy{MaxTokens: 10, Overflow: contextfrag.OverflowTrim}
	result := selector.Select([]contextfrag.ContextFrag{frag}, selector.ProfileFor(contextfrag.IntentRunConfigPreProvider), BudgetEnvelope{})
	if len(result.Selected) != 1 {
		t.Fatalf("selected = %d", len(result.Selected))
	}
	text := result.Selected[0].Parts[0].Text
	if !strings.Contains(text, "[trimmed from 200 bytes]") || len(text) > frag.Budget.MaxTokens*fragBudgetTokenByteFactor {
		t.Fatalf("trimmed text = %q", text)
	}
}

func TestFragBudgetSummarizeKeepsAndWarns(t *testing.T) {
	t.Parallel()
	selector := &FragmentSelector{}
	frag := textFrag("summary", contextfrag.SlotHistory, contextfrag.KindConversationEvent, sdk.MessageRoleAssistant, strings.Repeat("s", 50))
	frag.Budget = contextfrag.BudgetPolicy{MaxChars: 5, Overflow: contextfrag.OverflowSummarize}
	result := selector.Select([]contextfrag.ContextFrag{frag}, selector.ProfileFor(contextfrag.IntentRunConfigPreProvider), BudgetEnvelope{})
	if len(result.Selected) != 1 || result.Selected[0].Parts[0].Text != strings.Repeat("s", 50) ||
		len(result.Warnings) != 1 || result.Warnings[0].Code != "overflow_summarize_unsupported" {
		t.Fatalf("result = %#v", result)
	}
}

func TestFragBudgetUnderLimitIsNoop(t *testing.T) {
	t.Parallel()
	selector := &FragmentSelector{}
	within := textFrag("within", contextfrag.SlotHistory, contextfrag.KindConversationEvent, sdk.MessageRoleAssistant, "short")
	within.Budget = contextfrag.BudgetPolicy{MaxChars: 100, Overflow: contextfrag.OverflowDrop}
	noLimit := textFrag("no-limit", contextfrag.SlotHistory, contextfrag.KindConversationEvent, sdk.MessageRoleAssistant, strings.Repeat("n", 500))
	result := selector.Select([]contextfrag.ContextFrag{within, noLimit}, selector.ProfileFor(contextfrag.IntentRunConfigPreProvider), BudgetEnvelope{})
	if len(result.Selected) != 2 || len(result.Dropped) != 0 || len(result.Edited) != 0 || len(result.Warnings) != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestFragBudgetDropProtectsToolClosure(t *testing.T) {
	t.Parallel()
	selector := &FragmentSelector{}
	resultFrag := historyMessageFrag("result", toolResultMessage("call-1", "calc", strings.Repeat("r", 400)))
	resultFrag.Budget = contextfrag.BudgetPolicy{MaxTokens: 1, Overflow: contextfrag.OverflowDrop}
	result := selector.Select([]contextfrag.ContextFrag{
		historyMessageFrag("call", assistantToolCallMessage("call-1", "calc", "")),
		resultFrag,
	}, selector.ProfileFor(contextfrag.IntentRunConfigPreProvider), BudgetEnvelope{})
	if len(result.Selected) != 2 || len(result.Warnings) != 1 || result.Warnings[0].Code != "frag_budget_drop_blocked_tool_closure" {
		t.Fatalf("result = %#v", result)
	}
}

func TestFragBudgetCountsNonTextToolResultForMaxChars(t *testing.T) {
	t.Parallel()
	selector := &FragmentSelector{}
	resultFrag := historyMessageFrag("result", toolResultMessage("call-1", "calc", strings.Repeat("r", 400)))
	resultFrag.Budget = contextfrag.BudgetPolicy{MaxChars: 5, Overflow: contextfrag.OverflowDrop}
	result := selector.Select([]contextfrag.ContextFrag{
		historyMessageFrag("call", assistantToolCallMessage("call-1", "calc", "")),
		resultFrag,
	}, selector.ProfileFor(contextfrag.IntentRunConfigPreProvider), BudgetEnvelope{})
	if len(result.Warnings) != 1 || result.Warnings[0].Code != "frag_budget_drop_blocked_tool_closure" {
		t.Fatalf("result = %#v", result)
	}
}

func TestFragBudgetUnsupportedMessageTrimKeepsAndWarns(t *testing.T) {
	t.Parallel()
	selector := &FragmentSelector{}
	frag := messageFrag("message", sdk.AssistantMessage(strings.Repeat("m", 50)))
	frag.Budget = contextfrag.BudgetPolicy{MaxChars: 5, Overflow: contextfrag.OverflowTrim}
	result := selector.Select([]contextfrag.ContextFrag{frag}, selector.ProfileFor(contextfrag.IntentRunConfigPreProvider), BudgetEnvelope{})
	if len(result.Selected) != 1 || len(result.Warnings) != 1 || result.Warnings[0].Code != "overflow_trim_unsupported" {
		t.Fatalf("result = %#v", result)
	}
}

func TestFragBudgetEnforcesBothCharacterAndTokenDimensions(t *testing.T) {
	t.Parallel()
	selector := &FragmentSelector{}
	original := strings.Repeat("m", 100)
	frag := textFrag("both", contextfrag.SlotHistory, contextfrag.KindConversationEvent, sdk.MessageRoleAssistant, original)
	frag.Budget = contextfrag.BudgetPolicy{MaxChars: 1000, MaxTokens: 10, Overflow: contextfrag.OverflowTrim}
	result := selector.Select([]contextfrag.ContextFrag{frag}, selector.ProfileFor(contextfrag.IntentRunConfigPreProvider), BudgetEnvelope{})
	text := result.Selected[0].Parts[0].Text
	if len(text) > frag.Budget.MaxTokens*fragBudgetTokenByteFactor || utf8.RuneCountInString(text) > frag.Budget.MaxChars {
		t.Fatalf("trimmed text = %q", text)
	}
	if text == original {
		t.Fatal("MaxTokens did not trim while MaxChars remained under limit")
	}
}

func TestFragBudgetDropsOptionalSystemBeforeTierPass(t *testing.T) {
	t.Parallel()

	selector := &FragmentSelector{}
	optional := textFrag(
		"system.optional",
		contextfrag.SlotSystem,
		contextfrag.KindSystemPrompt,
		sdk.MessageRoleSystem,
		"oversized optional section",
	)
	optional.Trust = contextfrag.TrustSystem
	optional.RetentionTier = contextfrag.RetentionOptional
	optional.Budget = contextfrag.BudgetPolicy{MaxTokens: 1, Overflow: contextfrag.OverflowDrop}
	plan := &contextfrag.ContextBudgetPlan{SystemBudget: 500}

	result := selector.Select(
		[]contextfrag.ContextFrag{optional},
		selector.ProfileFor(contextfrag.IntentRunConfigPreProvider),
		BudgetEnvelope{Plan: plan},
	)

	if got := fragIDs(result.Selected); !reflect.DeepEqual(got, []string{systemBudgetMarkerID}) {
		t.Fatalf("selected = %v, want exactly one budget-accounted system omission marker", got)
	}
	if len(result.Dropped) != 1 {
		t.Fatalf("dropped = %#v, want optional oversized system fragment dropped", result.Dropped)
	}
	if len(result.Summary.DropReasons) != 1 ||
		result.Summary.DropReasons[0].Reason != "frag_budget:max_tokens" {
		t.Fatalf("drop reasons = %#v, want per-fragment budget reason", result.Summary.DropReasons)
	}
	if plan.ActualSystemCost != systemFragCost(result.Selected) {
		t.Fatalf("actual system cost = %d, want rendered marker cost %d", plan.ActualSystemCost, systemFragCost(result.Selected))
	}
}

func TestFragBudgetPlanDisabledKeepsOptionalSystemByteEquivalent(t *testing.T) {
	t.Parallel()

	selector := &FragmentSelector{}
	optional := textFrag(
		"system.optional",
		contextfrag.SlotSystem,
		contextfrag.KindSystemPrompt,
		sdk.MessageRoleSystem,
		"oversized optional section",
	)
	optional.Trust = contextfrag.TrustSystem
	optional.RetentionTier = contextfrag.RetentionOptional
	optional.Budget = contextfrag.BudgetPolicy{MaxTokens: 1, Overflow: contextfrag.OverflowDrop}

	result := selector.Select(
		[]contextfrag.ContextFrag{optional},
		selector.ProfileFor(contextfrag.IntentRunConfigPreProvider),
		BudgetEnvelope{},
	)

	if got := fragIDs(result.Selected); !reflect.DeepEqual(got, []string{"system.optional"}) {
		t.Fatalf("selected = %v, want the original system fragment when the plan is disabled", got)
	}
	if len(result.Dropped) != 0 {
		t.Fatalf("dropped = %#v, want no plan-disabled system drop", result.Dropped)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Code != "frag_budget_drop_blocked_must_keep" {
		t.Fatalf("warnings = %#v, want one must-keep warning", result.Warnings)
	}
}

func TestFragBudgetOverflowKeepIgnoresConfiguredLimits(t *testing.T) {
	t.Parallel()
	selector := &FragmentSelector{}
	want := strings.Repeat("k", 50)
	frag := textFrag("pinned", contextfrag.SlotHistory, contextfrag.KindConversationEvent, sdk.MessageRoleAssistant, want)
	frag.Budget = contextfrag.BudgetPolicy{MaxChars: 1, MaxTokens: 1, Overflow: contextfrag.OverflowKeep}
	result := selector.Select([]contextfrag.ContextFrag{frag}, selector.ProfileFor(contextfrag.IntentRunConfigPreProvider), BudgetEnvelope{})
	if len(result.Selected) != 1 || result.Selected[0].Parts[0].Text != want || len(result.Warnings) != 0 || len(result.Edited) != 0 {
		t.Fatalf("result = %#v", result)
	}
}
