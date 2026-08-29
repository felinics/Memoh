package contextfrag

import (
	"fmt"
	"strings"

	sdk "github.com/felinics/twilight/sdk"
)

const (
	// SourceToolClosureRepair attributes a synthetic tool-closure fragment to
	// the repair pass rather than to whatever collected the surrounding
	// history.
	SourceToolClosureRepair = "context_repair"
	// ToolClosureRepairText mirrors the legacy resolver repair wording so the
	// model sees the same interruption notice regardless of caller.
	ToolClosureRepairText = "tool execution interrupted before a response was recorded"
)

// RepairToolClosureFrags walks history message fragments and enforces tool
// closure integrity: every assistant tool call is answered before the next
// non-tool message (synthesizing an interrupted-result when missing) and tool
// results without a pending call are removed. Synthetic closure fragments are
// attributed to collector, so callers outside contextview (which has no
// per-request Collect() name to reuse) can still tag them consistently with
// the surrounding history fragments.
func RepairToolClosureFrags(frags []ContextFrag, scope Scope, collector string) []ContextFrag {
	repaired := make([]ContextFrag, 0, len(frags))
	pendingOrder := make([]string, 0, 2)
	pendingNames := make(map[string]string, 2)
	syntheticIndex := 0

	flush := func() {
		for _, callID := range pendingOrder {
			repaired = append(repaired, syntheticToolClosureFrag(callID, pendingNames[callID], syntheticIndex, scope, collector))
			syntheticIndex++
			delete(pendingNames, callID)
		}
		pendingOrder = pendingOrder[:0]
	}

	for _, frag := range frags {
		msg := FragMessage(frag)
		if msg == nil {
			flush()
			repaired = append(repaired, frag)
			continue
		}
		switch msg.Role {
		case sdk.MessageRoleAssistant:
			flush()
			repaired = append(repaired, frag)
			for _, part := range msg.Content {
				call, ok := part.(sdk.ToolCallPart)
				if !ok {
					continue
				}
				callID := strings.TrimSpace(call.ToolCallID)
				if callID == "" {
					continue
				}
				if _, exists := pendingNames[callID]; exists {
					continue
				}
				pendingNames[callID] = strings.TrimSpace(call.ToolName)
				pendingOrder = append(pendingOrder, callID)
			}
		case sdk.MessageRoleTool:
			kept := make([]sdk.MessagePart, 0, len(msg.Content))
			for _, part := range msg.Content {
				result, ok := part.(sdk.ToolResultPart)
				if !ok {
					continue
				}
				callID := strings.TrimSpace(result.ToolCallID)
				if _, pending := pendingNames[callID]; !pending {
					continue
				}
				kept = append(kept, result)
				delete(pendingNames, callID)
				for i, id := range pendingOrder {
					if id == callID {
						pendingOrder = append(pendingOrder[:i], pendingOrder[i+1:]...)
						break
					}
				}
			}
			if len(kept) == 0 {
				continue
			}
			if len(kept) != len(msg.Content) {
				frag = RebuildFragMessage(frag, sdk.Message{Role: sdk.MessageRoleTool, Content: kept})
			}
			repaired = append(repaired, frag)
		default:
			flush()
			repaired = append(repaired, frag)
		}
	}
	flush()
	return repaired
}

func syntheticToolClosureFrag(callID, toolName string, index int, scope Scope, collector string) ContextFrag {
	msg := sdk.Message{
		Role: sdk.MessageRoleTool,
		Content: []sdk.MessagePart{sdk.ToolResultPart{
			ToolCallID: callID,
			ToolName:   toolName,
			Result:     ToolClosureRepairText,
			IsError:    true,
		}},
	}
	return MessageFrag(MessageFragInput{
		ID:         fmt.Sprintf("history.tool_closure.%03d", index),
		Message:    msg,
		Kind:       KindConversationEvent,
		Slot:       SlotHistory,
		Priority:   PriorityForMessage(msg),
		CacheClass: CacheNever,
		Trust:      TrustWorkspace,
		Scope:      scope,
		Source:     SourceToolClosureRepair,
		SourceID:   "tool_closure." + callID,
		Collector:  collector,
		Index:      index,
	})
}

// FragMessage returns the first SDK message carried by frag's parts, or nil
// if frag does not wrap a message (e.g. a text or image fragment).
func FragMessage(frag ContextFrag) *sdk.Message {
	for _, part := range frag.Parts {
		if msg := partMessage(part); msg != nil {
			return msg
		}
	}
	return nil
}

func RebuildFragMessage(frag ContextFrag, msg sdk.Message) ContextFrag {
	rebuilt := frag
	cloned := cloneMessage(msg)
	rebuilt.Role = cloned.Role
	rebuilt.Parts = []Part{{
		Type:       PartSDKMessage,
		Message:    &cloned,
		SDKMessage: &cloned,
	}}
	rebuilt.TokenEstimate = 0

	ref := rebuilt.Ref
	ref.HashAlgo = ""
	ref.HashScope = ""
	ref.ContentHash = ""
	rebuilt.Ref = ContextRef{}
	return WithContextRef(rebuilt, ref)
}
