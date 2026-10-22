package contextview

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func TestConversationSummarySurvivesHistoryBudgetTrim(t *testing.T) {
	t.Parallel()

	selector := &FragmentSelector{}
	profile := selector.ProfileFor(contextfrag.IntentRunConfigPreProvider)

	frags := []contextfrag.ContextFrag{contextfrag.TextFrag(contextfrag.TextFragInput{
		ID: "summary", Kind: contextfrag.KindConversationSummary, Role: sdk.MessageRoleUser,
		Slot: contextfrag.SlotHistory, Text: strings.Repeat("s", 2_000),
		Trust: contextfrag.TrustExternal,
	})}
	for i := range 20 {
		frags = append(frags, contextfrag.TextFrag(contextfrag.TextFragInput{
			ID: fmt.Sprintf("raw-%d", i), Kind: contextfrag.KindConversationEvent,
			Role: sdk.MessageRoleUser, Slot: contextfrag.SlotHistory,
			Text: strings.Repeat("r", 2_000), Trust: contextfrag.TrustExternal,
		}))
	}

	result := selector.Select(frags, profile, BudgetEnvelope{
		Plan: &contextfrag.ContextBudgetPlan{SystemBudget: 6_000},
	})

	if result.FatalError != nil {
		t.Fatalf("Select() fatal = %v", result.FatalError)
	}
	if !containsFragID(result.Selected, "summary") {
		t.Fatalf("summary evicted under history-budget pressure: dropped=%v", fragIDs(result.Dropped))
	}
	if !containsFragID(result.Selected, "raw-19") {
		t.Fatalf("newest raw row missing: selected=%v", fragIDs(result.Selected))
	}
	if !containsFragID(result.Dropped, "raw-0") {
		t.Fatalf("oldest raw row must fund the trim: dropped=%v", fragIDs(result.Dropped))
	}
}

func selectedFragText(frags []contextfrag.ContextFrag, id string) string {
	for _, frag := range frags {
		if frag.ID != id {
			continue
		}
		for _, part := range frag.Parts {
			if part.Type == contextfrag.PartText {
				return part.Text
			}
			if msg := sdkMessagePart(part); msg != nil {
				for _, mp := range msg.Content {
					if text, ok := mp.(sdk.TextPart); ok {
						return text.Text
					}
				}
			}
		}
	}
	return ""
}

// Spec rule 3: persisted summaries are immutable. A summary that exceeds the
// history budget is never truncated to fit — the selection fails closed and
// the recovery path rewrites history through an epoch-recorded compaction.
func TestOversizedConversationSummaryFailsClosedWithoutTruncation(t *testing.T) {
	t.Parallel()

	selector := &FragmentSelector{}
	profile := selector.ProfileFor(contextfrag.IntentRunConfigPreProvider)

	frags := []contextfrag.ContextFrag{contextfrag.TextFrag(contextfrag.TextFragInput{
		ID: "summary", Kind: contextfrag.KindConversationSummary, Role: sdk.MessageRoleUser,
		Slot: contextfrag.SlotHistory, Text: strings.Repeat("s", 40_000),
		Trust: contextfrag.TrustExternal,
	})}
	for i := range 5 {
		frags = append(frags, contextfrag.TextFrag(contextfrag.TextFragInput{
			ID: fmt.Sprintf("raw-%d", i), Kind: contextfrag.KindConversationEvent,
			Role: sdk.MessageRoleUser, Slot: contextfrag.SlotHistory,
			Text: strings.Repeat("r", 2_000), Trust: contextfrag.TrustExternal,
		}))
	}

	result := selector.Select(frags, profile, BudgetEnvelope{
		Plan: &contextfrag.ContextBudgetPlan{SystemBudget: 6_000},
	})

	if !errors.Is(result.FatalError, contextfrag.ErrProtectedContextOverflow) {
		t.Fatalf("Select() fatal = %v, want fail-closed instead of truncating the summary", result.FatalError)
	}
}

func TestOversizedSummaryStillFailsClosedBelowMinimumBudget(t *testing.T) {
	t.Parallel()

	selector := &FragmentSelector{}
	profile := selector.ProfileFor(contextfrag.IntentRunConfigPreProvider)

	frags := []contextfrag.ContextFrag{contextfrag.TextFrag(contextfrag.TextFragInput{
		ID: "summary", Kind: contextfrag.KindConversationSummary, Role: sdk.MessageRoleUser,
		Slot: contextfrag.SlotHistory, Text: strings.Repeat("s", 40_000),
		Trust: contextfrag.TrustExternal,
	})}

	result := selector.Select(frags, profile, BudgetEnvelope{
		Plan: &contextfrag.ContextBudgetPlan{SystemBudget: 40},
	})

	if !errors.Is(result.FatalError, contextfrag.ErrProtectedContextOverflow) {
		t.Fatalf("Select() fatal = %v, want fail-closed below the minimum viable summary", result.FatalError)
	}
}

// A summary-only selection cannot drop anything, so the summary must fit
// whole or the selection fails closed — no partial rewrite ever ships.
func TestSummaryOnlySelectionFailsClosedWhenOverBudget(t *testing.T) {
	t.Parallel()

	selector := &FragmentSelector{}
	profile := selector.ProfileFor(contextfrag.IntentRunConfigPreProvider)

	fitting := contextfrag.TextFrag(contextfrag.TextFragInput{
		ID: "summary", Kind: contextfrag.KindConversationSummary, Role: sdk.MessageRoleUser,
		Slot: contextfrag.SlotHistory, Text: strings.Repeat("s", 200),
		Trust: contextfrag.TrustExternal,
	})
	result := selector.Select([]contextfrag.ContextFrag{fitting}, profile, BudgetEnvelope{
		Plan: &contextfrag.ContextBudgetPlan{SystemBudget: 100},
	})
	if result.FatalError != nil {
		t.Fatalf("fitting summary rejected: %v", result.FatalError)
	}
	if text := selectedFragText(result.Selected, "summary"); len(text) != 200 {
		t.Fatalf("summary must stay byte-identical, got len=%d", len(text))
	}

	oversized := contextfrag.TextFrag(contextfrag.TextFragInput{
		ID: "summary", Kind: contextfrag.KindConversationSummary, Role: sdk.MessageRoleUser,
		Slot: contextfrag.SlotHistory, Text: strings.Repeat("s", 40_000),
		Trust: contextfrag.TrustExternal,
	})
	result = selector.Select([]contextfrag.ContextFrag{oversized}, profile, BudgetEnvelope{
		Plan: &contextfrag.ContextBudgetPlan{SystemBudget: 100},
	})
	if !errors.Is(result.FatalError, contextfrag.ErrProtectedContextOverflow) {
		t.Fatalf("oversized summary must fail closed, got %v", result.FatalError)
	}
}
