package contextview

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

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

func TestOversizedConversationSummaryTruncatesInsteadOfFailingClosed(t *testing.T) {
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

	if result.FatalError != nil {
		t.Fatalf("Select() fatal = %v, want the summary truncated instead", result.FatalError)
	}
	if !containsFragID(result.Selected, "summary") {
		t.Fatalf("summary missing after truncation: selected=%v", fragIDs(result.Selected))
	}
	text := selectedFragText(result.Selected, "summary")
	if len(text) >= 40_000 || !strings.Contains(text, "truncated to fit") {
		t.Fatalf("summary text len=%d, want a shortened body carrying the truncation notice", len(text))
	}
	if !strings.HasPrefix(text, strings.Repeat("s", 100)) {
		t.Fatalf("truncation must preserve the summary head, got %.40q", text)
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

func TestSummaryOnlySelectionSkipsTheNoticeReserve(t *testing.T) {
	t.Parallel()

	selector := &FragmentSelector{}
	profile := selector.ProfileFor(contextfrag.IntentRunConfigPreProvider)

	frags := []contextfrag.ContextFrag{contextfrag.TextFrag(contextfrag.TextFragInput{
		ID: "summary", Kind: contextfrag.KindConversationSummary, Role: sdk.MessageRoleUser,
		Slot: contextfrag.SlotHistory, Text: strings.Repeat("s", 40_000),
		Trust: contextfrag.TrustExternal,
	})}

	result := selector.Select(frags, profile, BudgetEnvelope{
		Plan: &contextfrag.ContextBudgetPlan{SystemBudget: 100},
	})

	if result.FatalError != nil {
		t.Fatalf("Select() fatal = %v; nothing can drop, so no trim notice is ever emitted and the full budget belongs to the summary", result.FatalError)
	}
	text := selectedFragText(result.Selected, "summary")
	if !strings.Contains(text, "truncated to fit") {
		t.Fatalf("summary must be truncated into the budget, got len=%d", len(text))
	}
}

func TestSummaryShrinkRedistributesUnusedShares(t *testing.T) {
	t.Parallel()

	selector := &FragmentSelector{}
	profile := selector.ProfileFor(contextfrag.IntentRunConfigPreProvider)

	frags := []contextfrag.ContextFrag{
		contextfrag.TextFrag(contextfrag.TextFragInput{
			ID: "summary-small", Kind: contextfrag.KindConversationSummary, Role: sdk.MessageRoleUser,
			Slot: contextfrag.SlotHistory, Text: strings.Repeat("s", 32),
			Trust: contextfrag.TrustExternal,
		}),
		contextfrag.TextFrag(contextfrag.TextFragInput{
			ID: "summary-big", Kind: contextfrag.KindConversationSummary, Role: sdk.MessageRoleUser,
			Slot: contextfrag.SlotHistory, Text: strings.Repeat("s", 40_000),
			Trust: contextfrag.TrustExternal,
		}),
	}

	result := selector.Select(frags, profile, BudgetEnvelope{
		Plan: &contextfrag.ContextBudgetPlan{SystemBudget: 150},
	})

	if result.FatalError != nil {
		t.Fatalf("Select() fatal = %v; the small summary's unused share must fund the big one", result.FatalError)
	}
	if small := selectedFragText(result.Selected, "summary-small"); small != strings.Repeat("s", 32) {
		t.Fatalf("under-target summary must stay untouched, got %q", small)
	}
	if big := selectedFragText(result.Selected, "summary-big"); !strings.Contains(big, "truncated to fit") {
		t.Fatalf("oversized summary must be truncated, got len=%d", len(big))
	}
}

func TestSummaryYieldsSpaceForTheTrimNotice(t *testing.T) {
	t.Parallel()

	selector := &FragmentSelector{}
	profile := selector.ProfileFor(contextfrag.IntentRunConfigPreProvider)

	frags := []contextfrag.ContextFrag{contextfrag.TextFrag(contextfrag.TextFragInput{
		ID: "summary", Kind: contextfrag.KindConversationSummary, Role: sdk.MessageRoleUser,
		Slot: contextfrag.SlotHistory, Text: strings.Repeat("s", 2_720),
		Trust: contextfrag.TrustExternal,
	})}
	for i := range 2 {
		frags = append(frags, contextfrag.TextFrag(contextfrag.TextFragInput{
			ID: fmt.Sprintf("raw-%d", i), Kind: contextfrag.KindConversationEvent,
			Role: sdk.MessageRoleUser, Slot: contextfrag.SlotHistory,
			Text: strings.Repeat("r", 400), Trust: contextfrag.TrustExternal,
		}))
	}

	result := selector.Select(frags, profile, BudgetEnvelope{
		Plan: &contextfrag.ContextBudgetPlan{SystemBudget: 1_000},
	})

	if result.FatalError != nil {
		t.Fatalf("Select() fatal = %v; the summary must yield enough space to fund the trim notice", result.FatalError)
	}
	if !strings.Contains(selectedFragText(result.Selected, "summary"), "truncated to fit") {
		t.Fatal("summary must be shaved to make room for the trim notice")
	}
	if !containsFragID(result.Selected, "raw-1") || !containsFragID(result.Dropped, "raw-0") {
		t.Fatalf("newest raw row must survive and the oldest fund the trim: selected=%v dropped=%v", fragIDs(result.Selected), fragIDs(result.Dropped))
	}
}

func TestSummaryTruncateNoticeStaysUnderTheFloor(t *testing.T) {
	t.Parallel()

	longest := summaryTruncateNotice + "\n</summary>"
	if utf8.RuneCountInString(longest) >= minSummaryKeepRunes {
		t.Fatalf("notice (%d runes) must stay below the %d-rune floor or TruncateRunesWithSuffix silently drops it",
			utf8.RuneCountInString(longest), minSummaryKeepRunes)
	}
}

func TestSummaryShrinkProbesTheFloorBeforeFailing(t *testing.T) {
	t.Parallel()

	frag := contextfrag.TextFrag(contextfrag.TextFragInput{
		ID: "summary", Kind: contextfrag.KindConversationSummary, Role: sdk.MessageRoleUser,
		Slot:  contextfrag.SlotHistory,
		Text:  strings.Repeat("夏", 5_000) + strings.Repeat("a", 25_000),
		Trust: contextfrag.TrustExternal,
	})

	shrunk, tokens, ok := shrinkSummaryFragToTokens(frag, 150)
	if !ok {
		t.Fatal("a floor-sized summary fits the target, shrink must not fail closed")
	}
	if tokens > 150 {
		t.Fatalf("shrunk tokens = %d, want <= 150", tokens)
	}
	text := selectedFragText([]contextfrag.ContextFrag{shrunk}, "summary")
	if !strings.HasPrefix(text, "夏夏夏") || !strings.Contains(text, "truncated to fit") {
		t.Fatalf("floor probe must keep the head and carry the notice, got %.60q", text)
	}
}

func TestSummaryTruncationClosesTheSummaryTag(t *testing.T) {
	t.Parallel()

	selector := &FragmentSelector{}
	profile := selector.ProfileFor(contextfrag.IntentRunConfigPreProvider)

	frags := []contextfrag.ContextFrag{contextfrag.MessageFrag(contextfrag.MessageFragInput{
		ID: "summary", Kind: contextfrag.KindConversationSummary,
		Slot: contextfrag.SlotHistory, Trust: contextfrag.TrustExternal,
		Message: sdk.Message{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{
			sdk.TextPart{Text: "<summary>\n" + strings.Repeat("s", 40_000) + "\n</summary>"},
		}},
	})}

	result := selector.Select(frags, profile, BudgetEnvelope{
		Plan: &contextfrag.ContextBudgetPlan{SystemBudget: 2_000},
	})

	if result.FatalError != nil {
		t.Fatalf("Select() fatal = %v", result.FatalError)
	}
	text := selectedFragText(result.Selected, "summary")
	if !strings.Contains(text, "truncated to fit") {
		t.Fatalf("summary must carry the truncation notice, got %.60q", text)
	}
	if !strings.HasSuffix(strings.TrimSpace(text), "</summary>") {
		t.Fatalf("truncation must re-close the summary tag, got tail %.80q", text[max(0, len(text)-80):])
	}
}

func TestOversizedMessageShapedSummaryTruncates(t *testing.T) {
	t.Parallel()

	selector := &FragmentSelector{}
	profile := selector.ProfileFor(contextfrag.IntentRunConfigPreProvider)

	frags := []contextfrag.ContextFrag{contextfrag.MessageFrag(contextfrag.MessageFragInput{
		ID: "summary", Kind: contextfrag.KindConversationSummary,
		Slot: contextfrag.SlotHistory, Trust: contextfrag.TrustExternal,
		Message: sdk.Message{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{
			sdk.TextPart{Text: strings.Repeat("s", 40_000)},
		}},
	})}

	result := selector.Select(frags, profile, BudgetEnvelope{
		Plan: &contextfrag.ContextBudgetPlan{SystemBudget: 2_000},
	})

	if result.FatalError != nil {
		t.Fatalf("Select() fatal = %v, want the message-shaped summary truncated", result.FatalError)
	}
	text := selectedFragText(result.Selected, "summary")
	if len(text) >= 40_000 || !strings.Contains(text, "truncated to fit") {
		t.Fatalf("summary text len=%d, want a shortened body carrying the truncation notice", len(text))
	}
	if !strings.HasPrefix(text, strings.Repeat("s", 100)) {
		t.Fatalf("truncation must preserve the summary head, got %.40q", text)
	}
}
