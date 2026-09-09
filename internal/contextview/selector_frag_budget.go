package contextview

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/textutil"
)

const fragBudgetTokenByteFactor = contextfrag.EstimateBytesPerToken

type fragBudgetDrop struct {
	frag   contextfrag.ContextFrag
	reason string
}

type fragBudgetEdit struct {
	trace  contextfrag.ContextEditTrace
	fragID string
	reason string
}

func enforceFragBudgets(
	frags []contextfrag.ContextFrag,
	profile IntentProfile,
	systemPlanActive bool,
) (kept []contextfrag.ContextFrag, dropped []fragBudgetDrop, edits []fragBudgetEdit, warnings []contextfrag.ValidationWarning) {
	kept = make([]contextfrag.ContextFrag, 0, len(frags))
	for _, frag := range frags {
		reason, exceeded := fragBudgetExceeded(frag)
		if !exceeded || frag.Budget.Overflow == contextfrag.OverflowKeep {
			kept = append(kept, frag)
			continue
		}
		switch frag.Budget.Overflow {
		case contextfrag.OverflowDrop:
			switch {
			case isToolExchangeFrag(frag):
				kept = append(kept, frag)
				warnings = append(warnings, contextfrag.ValidationWarning{Code: "frag_budget_drop_blocked_tool_closure", Ref: frag.Ref})
			case isMustKeepFrag(frag, profile) && (!systemPlanActive || !droppableSystemBudgetFrag(frag)):
				kept = append(kept, frag)
				warnings = append(warnings, contextfrag.ValidationWarning{Code: "frag_budget_drop_blocked_must_keep", Ref: frag.Ref})
			default:
				dropped = append(dropped, fragBudgetDrop{frag: frag, reason: reason})
			}
		case contextfrag.OverflowTrim:
			trimmed, ok := trimFragText(frag)
			if !ok {
				kept = append(kept, frag)
				warnings = append(warnings, contextfrag.ValidationWarning{Code: "overflow_trim_unsupported", Ref: frag.Ref})
				continue
			}
			kept = append(kept, trimmed)
			edits = append(edits, fragBudgetEdit{
				trace:  fragBudgetTrimEdit(trimmed),
				fragID: frag.ID,
				reason: reason,
			})
		case contextfrag.OverflowSummarize:
			kept = append(kept, frag)
			warnings = append(warnings, contextfrag.ValidationWarning{Code: "overflow_summarize_unsupported", Ref: frag.Ref})
		default:
			kept = append(kept, frag)
		}
	}
	return kept, dropped, edits, warnings
}

func droppableSystemBudgetFrag(frag contextfrag.ContextFrag) bool {
	return frag.Slot == contextfrag.SlotSystem &&
		(frag.RetentionTier == contextfrag.RetentionOptional ||
			frag.RetentionTier == contextfrag.RetentionPreferred)
}

func fragBudgetExceeded(frag contextfrag.ContextFrag) (string, bool) {
	if frag.Budget.MaxTokens > 0 && contextfrag.ResolveFragTokens(frag) > frag.Budget.MaxTokens {
		return "frag_budget:max_tokens", true
	}
	if frag.Budget.MaxChars > 0 && fragCharCount(frag) > frag.Budget.MaxChars {
		return "frag_budget:max_chars", true
	}
	return "", false
}

func fragCharCount(frag contextfrag.ContextFrag) int {
	total := 0
	for _, part := range frag.Parts {
		switch part.Type {
		case contextfrag.PartText:
			total += utf8.RuneCountInString(part.Text)
		case contextfrag.PartSDKMessage:
			msg := contextfrag.FragMessage(contextfrag.ContextFrag{Parts: []contextfrag.Part{part}})
			if msg == nil {
				continue
			}
			for _, messagePart := range msg.Content {
				switch typed := messagePart.(type) {
				case sdk.TextPart:
					total += utf8.RuneCountInString(typed.Text)
				case sdk.ReasoningPart:
					total += utf8.RuneCountInString(typed.Text)
				case sdk.ImagePart:
				default:
					if data, err := json.Marshal(messagePart); err == nil {
						total += utf8.RuneCountInString(string(data))
					}
				}
			}
		}
	}
	return total
}

func trimFragText(frag contextfrag.ContextFrag) (contextfrag.ContextFrag, bool) {
	if len(frag.Parts) == 0 {
		return frag, false
	}
	for _, part := range frag.Parts {
		if part.Type != contextfrag.PartText {
			return frag, false
		}
	}
	charLimit, tokenByteLimit := fragTrimLimit(frag.Budget)
	parts := frag.Parts
	changed := false
	if charLimit > 0 {
		if next, ok := trimPartsToLimit(parts, charLimit, false); ok {
			parts, changed = next, true
		}
	}
	if tokenByteLimit > 0 {
		if next, ok := trimPartsToLimit(parts, tokenByteLimit, true); ok {
			parts, changed = next, true
		}
	}
	if !changed {
		return frag, false
	}
	frag.Parts = parts
	frag.TokenEstimate = 0
	frag.Ref.HashAlgo = ""
	frag.Ref.HashScope = ""
	frag.Ref.ContentHash = ""
	return contextfrag.WithContextRef(frag, frag.Ref), true
}

func fragTrimLimit(budget contextfrag.BudgetPolicy) (charLimit, tokenByteLimit int) {
	if budget.MaxChars > 0 {
		charLimit = budget.MaxChars
	}
	if budget.MaxTokens > 0 {
		tokenByteLimit = budget.MaxTokens * fragBudgetTokenByteFactor
	}
	return charLimit, tokenByteLimit
}

func trimPartsToLimit(parts []contextfrag.Part, limit int, byBytes bool) ([]contextfrag.Part, bool) {
	originalBytes := 0
	for _, part := range parts {
		originalBytes += len(part.Text)
	}
	marker := fmt.Sprintf("[trimmed from %d bytes]", originalBytes)
	contentLimit := limit
	appendMarker := limit > len(marker)
	if appendMarker {
		contentLimit -= len(marker)
	}
	next := append([]contextfrag.Part(nil), parts...)
	remaining := contentLimit
	changed := false
	lastChanged := -1
	for i, part := range next {
		kept := textutil.TruncateRunes(part.Text, remaining)
		if byBytes {
			kept = safeUTF8BytePrefix(part.Text, remaining)
		}
		if kept != part.Text {
			changed = true
			lastChanged = i
		}
		if byBytes {
			remaining -= len(kept)
		} else {
			remaining -= utf8.RuneCountInString(kept)
		}
		next[i].Text = kept
	}
	if !changed {
		return parts, false
	}
	if appendMarker {
		next[lastChanged].Text += marker
	}
	return next, true
}

func fragBudgetTrimEdit(frag contextfrag.ContextFrag) contextfrag.ContextEditTrace {
	return contextfrag.ContextEditTrace{
		EditID: "frag_budget.trim." + frag.ID,
		Op:     contextfrag.EditReplace,
		Slot:   frag.Slot,
		Refs:   []contextfrag.ContextRef{frag.Ref},
	}
}

func safeUTF8BytePrefix(value string, maxBytes int) string {
	if maxBytes <= 0 || value == "" {
		return ""
	}
	if maxBytes >= len(value) {
		return value
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}
