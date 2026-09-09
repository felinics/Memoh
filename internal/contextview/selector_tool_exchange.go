package contextview

import (
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
)

const toolExchangeDropReason = "tool_exchange:stripped"

func applyToolExchangePolicy(frags []contextfrag.ContextFrag, policy *contextfrag.ToolExchangePolicy) (kept, dropped []contextfrag.ContextFrag, edits []contextfrag.ContextEditTrace) {
	if policy == nil || policy.MinMessages > 0 && countMessageFrags(frags) <= policy.MinMessages {
		return frags, nil, nil
	}
	kept = make([]contextfrag.ContextFrag, 0, len(frags))
	openTurnStart := openTurnTailStart(frags)
	for i, frag := range frags {
		msg := contextfrag.FragMessage(frag)
		if msg == nil || frag.Slot != contextfrag.SlotHistory || (openTurnStart >= 0 && i > openTurnStart) {
			kept = append(kept, frag)
			continue
		}
		switch msg.Role {
		case sdk.MessageRoleTool:
			results := askUserToolResults(msg.Content)
			if len(results) == 0 {
				dropped = append(dropped, frag)
				continue
			}
			if len(results) != len(msg.Content) {
				frag = rebuildMessageFrag(frag, sdk.Message{Role: sdk.MessageRoleTool, Content: results})
				edits = append(edits, toolExchangeEdit(frag))
			}
			kept = append(kept, frag)
		case sdk.MessageRoleAssistant:
			parts, changed := stripAssistantToolParts(msg.Content)
			if !changed {
				kept = append(kept, frag)
				continue
			}
			if len(parts) == 0 {
				dropped = append(dropped, frag)
				continue
			}
			frag = rebuildMessageFrag(frag, sdk.Message{Role: sdk.MessageRoleAssistant, Content: parts})
			edits = append(edits, toolExchangeEdit(frag))
			kept = append(kept, frag)
		default:
			kept = append(kept, frag)
		}
	}
	return kept, dropped, edits
}

// openTurnTailStart reports the slice index of the last history user message
// when the history ends in a turn that is still being answered, or -1.
//
// A continuation resumed after a deferred tool call (tool approval, ask_user)
// carries no current user message: the request replays the persisted history
// and ends with the parked step's tool call and the result it produced. Those
// messages are the live turn, not tool noise from an earlier exchange. Stripping
// them leaves the model a trailing assistant text with no tool call, no result
// and no reasoning, so it cannot see what the tool returned and re-issues the
// call; DeepSeek thinking mode additionally rejects the request because that
// trailing assistant message carries no reasoning_content. The tail is exempt
// only when no current user frag exists and the last history message is a tool
// result or an assistant message that still holds tool calls; a turn that
// concluded with plain assistant text is finished history and strips as usual.
func openTurnTailStart(frags []contextfrag.ContextFrag) int {
	lastUser := -1
	lastHistory := -1
	for i, frag := range frags {
		if frag.Slot == contextfrag.SlotCurrentUser || frag.Kind == contextfrag.KindCurrentUserMessage {
			return -1
		}
		msg := contextfrag.FragMessage(frag)
		if msg == nil || frag.Slot != contextfrag.SlotHistory {
			continue
		}
		lastHistory = i
		if msg.Role == sdk.MessageRoleUser {
			lastUser = i
		}
	}
	if lastUser < 0 || lastHistory <= lastUser {
		return -1
	}
	last := contextfrag.FragMessage(frags[lastHistory])
	if last.Role == sdk.MessageRoleTool {
		return lastUser
	}
	if last.Role == sdk.MessageRoleAssistant {
		for _, part := range last.Content {
			if _, ok := part.(sdk.ToolCallPart); ok {
				return lastUser
			}
		}
	}
	return -1
}

func countMessageFrags(frags []contextfrag.ContextFrag) int {
	count := 0
	for _, frag := range frags {
		if contextfrag.FragMessage(frag) != nil {
			count++
		}
	}
	return count
}

func askUserToolResults(parts []sdk.MessagePart) []sdk.MessagePart {
	kept := make([]sdk.MessagePart, 0, len(parts))
	for _, part := range parts {
		result, ok := part.(sdk.ToolResultPart)
		if ok && strings.EqualFold(strings.TrimSpace(result.ToolName), userinput.ToolNameAskUser) {
			kept = append(kept, result)
		}
	}
	return kept
}

func stripAssistantToolParts(parts []sdk.MessagePart) ([]sdk.MessagePart, bool) {
	kept := make([]sdk.MessagePart, 0, len(parts))
	changed := false
	for _, part := range parts {
		switch typed := part.(type) {
		case sdk.ToolCallPart:
			if strings.EqualFold(strings.TrimSpace(typed.ToolName), userinput.ToolNameAskUser) {
				kept = append(kept, typed)
			} else {
				changed = true
			}
		case sdk.ToolResultPart, sdk.ReasoningPart:
			changed = true
		case sdk.TextPart:
			if strings.TrimSpace(typed.Text) == "" {
				changed = true
			} else {
				kept = append(kept, typed)
			}
		default:
			kept = append(kept, part)
		}
	}
	return kept, changed
}

func rebuildMessageFrag(frag contextfrag.ContextFrag, msg sdk.Message) contextfrag.ContextFrag {
	conflictKey := frag.ConflictKey
	frag = contextfrag.RebuildFragMessage(frag, msg)
	frag.ConflictKey = conflictKey
	frag.TokenEstimate = 0
	frag.Ref.HashAlgo = ""
	frag.Ref.HashScope = ""
	frag.Ref.ContentHash = ""
	return contextfrag.WithContextRef(frag, frag.Ref)
}

func toolExchangeEdit(frag contextfrag.ContextFrag) contextfrag.ContextEditTrace {
	return contextfrag.ContextEditTrace{
		EditID: "tool_exchange.strip." + frag.ID,
		Op:     contextfrag.EditReplace,
		Slot:   frag.Slot,
		Refs:   []contextfrag.ContextRef{frag.Ref},
	}
}

func isToolExchangeFrag(frag contextfrag.ContextFrag) bool {
	msg := contextfrag.FragMessage(frag)
	if msg == nil {
		return false
	}
	if msg.Role == sdk.MessageRoleTool {
		return true
	}
	for _, part := range msg.Content {
		switch part.(type) {
		case sdk.ToolCallPart, sdk.ToolResultPart:
			return true
		}
	}
	return false
}
