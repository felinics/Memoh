package contextview

import (
	"strings"
	"unicode/utf8"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/textutil"
)

// summaryTruncateNotice is appended to a summary shortened under protected
// budget pressure. The text is model-visible; change it only deliberately.
const summaryTruncateNotice = "\n\n[The rest of this summary was truncated to fit the context window.]"

// minSummaryKeepRunes is the smallest truncated summary worth keeping,
// notice included: below this a truncated summary carries no recall value,
// so selection fails closed instead of shipping a stub.
const minSummaryKeepRunes = 200

const summaryBudgetEditReason = "summary_budget_truncate"

// shrinkOversizedProtectedSummaries shortens must-keep conversation summaries
// in place until the protected set fits ceiling, so a summary larger than the
// history slot degrades to a truncated summary instead of failing the whole
// selection. Under-target summaries settle at their actual cost and their
// unused share funds the oversized ones. It reports the applied edits; an
// empty result means nothing could be shrunk.
func shrinkOversizedProtectedSummaries(tagged []TaggedFrag, protectedCost, ceiling int) []fragBudgetEdit {
	var summaries []int
	summaryCost := 0
	for i := range tagged {
		if tagged[i].Frag.Kind == contextfrag.KindConversationSummary && tagged[i].HasTag(TagMustKeep) {
			summaries = append(summaries, i)
			summaryCost += tagged[i].Tokens
		}
	}
	if len(summaries) == 0 {
		return nil
	}
	available := ceiling - (protectedCost - summaryCost)
	if available <= 0 {
		return nil
	}
	pending := summaries
	target := available / len(pending)
	for range summaries {
		settledSome := false
		var oversized []int
		for _, idx := range pending {
			if tagged[idx].Tokens <= target {
				available -= tagged[idx].Tokens
				settledSome = true
			} else {
				oversized = append(oversized, idx)
			}
		}
		pending = oversized
		if len(pending) == 0 || available <= 0 {
			return nil
		}
		target = available / len(pending)
		if !settledSome {
			break
		}
	}

	var edits []fragBudgetEdit
	for _, idx := range pending {
		entry := &tagged[idx]
		next, tokens, ok := shrinkSummaryFragToTokens(entry.Frag, target)
		if !ok {
			continue
		}
		entry.Frag = next
		entry.Tokens = tokens
		edits = append(edits, fragBudgetEdit{
			trace: contextfrag.ContextEditTrace{
				EditID: "summary.budget_truncate." + next.ID,
				Op:     contextfrag.EditReplace,
				Slot:   next.Slot,
				Refs:   []contextfrag.ContextRef{next.Ref},
			},
			fragID: next.ID,
			reason: summaryBudgetEditReason,
		})
	}
	return edits
}

func shrinkSummaryFragToTokens(frag contextfrag.ContextFrag, target int) (contextfrag.ContextFrag, int, bool) {
	text, rebuild, ok := summaryFragText(frag)
	if !ok || target <= 0 {
		return frag, 0, false
	}
	current := contextfrag.ResolveProviderBudgetFragTokens(frag)
	if current <= 0 {
		return frag, 0, false
	}
	suffix := summaryTruncateNotice
	if strings.HasSuffix(strings.TrimRight(text, " \t\n"), "</summary>") {
		suffix += "\n</summary>"
	}
	keepRunes := utf8.RuneCountInString(text) * target / current
	for attempt := 0; attempt < 8 && keepRunes >= minSummaryKeepRunes; attempt++ {
		next := rebuild(textutil.TruncateRunesWithSuffix(text, keepRunes, suffix))
		tokens := contextfrag.ResolveProviderBudgetFragTokens(next)
		if tokens <= target {
			return next, tokens, true
		}
		keepRunes = keepRunes * 4 / 5
	}
	// The geometric descent can overshoot straight past the floor on
	// mixed-density text, so probe exactly the floor before failing closed:
	// cost is monotonic in kept runes, which makes this probe decisive.
	next := rebuild(textutil.TruncateRunesWithSuffix(text, minSummaryKeepRunes, suffix))
	if tokens := contextfrag.ResolveProviderBudgetFragTokens(next); tokens <= target {
		return next, tokens, true
	}
	return frag, 0, false
}

// summaryFragText extracts the single text body of a summary fragment and a
// rebuild closure that re-normalizes refs and estimates around a replacement
// body. Fragments with any other part shape are not shrinkable.
func summaryFragText(frag contextfrag.ContextFrag) (string, func(string) contextfrag.ContextFrag, bool) {
	if len(frag.Parts) != 1 {
		return "", nil, false
	}
	part := frag.Parts[0]
	if part.Type == contextfrag.PartText {
		return part.Text, func(text string) contextfrag.ContextFrag {
			next := frag
			nextPart := part
			nextPart.Text = text
			next.Parts = []contextfrag.Part{nextPart}
			conflictKey := next.ConflictKey
			next.TokenEstimate = 0
			next.Ref.HashAlgo = ""
			next.Ref.HashScope = ""
			next.Ref.ContentHash = ""
			next = contextfrag.WithContextRef(next, next.Ref)
			next.ConflictKey = conflictKey
			return next
		}, true
	}
	msg := sdkMessagePart(part)
	if msg == nil || len(msg.Content) != 1 {
		return "", nil, false
	}
	textPart, ok := msg.Content[0].(sdk.TextPart)
	if !ok {
		return "", nil, false
	}
	return textPart.Text, func(text string) contextfrag.ContextFrag {
		return rebuildMessageFrag(frag, sdk.Message{
			Role:    msg.Role,
			Content: []sdk.MessagePart{sdk.TextPart{Text: text}},
		})
	}, true
}
