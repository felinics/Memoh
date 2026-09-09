package application

import (
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
)

const syntheticToolClosureError = "tool execution interrupted before a response was recorded"

type pendingToolCall struct {
	ID       string
	ToolName string
}

type projectedToolCallKey struct {
	segment int
	callID  string
}

func repairToolCallClosures(messages []ModelMessage, reason string) []ModelMessage {
	if len(messages) == 0 {
		return messages
	}
	messages = normalizeDecisionProjectionResults(messages)
	if strings.TrimSpace(reason) == "" {
		reason = syntheticToolClosureError
	}

	repaired := make([]ModelMessage, 0, len(messages))
	pending := make(map[string]pendingToolCall)
	pendingOrder := make([]string, 0)

	flushPending := func() {
		if len(pendingOrder) == 0 {
			return
		}
		for _, callID := range pendingOrder {
			call, ok := pending[callID]
			if !ok {
				continue
			}
			repaired = append(repaired, syntheticToolResultMessage(call.ID, call.ToolName, reason))
			delete(pending, callID)
		}
		pendingOrder = pendingOrder[:0]
	}

	for _, msg := range messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		switch role {
		case "assistant":
			if len(pending) > 0 {
				flushPending()
			}
			repaired = append(repaired, msg)
			for _, call := range extractAssistantToolCallParts(msg) {
				callID := strings.TrimSpace(call.ToolCallID)
				if callID == "" {
					continue
				}
				if _, exists := pending[callID]; exists {
					continue
				}
				pending[callID] = pendingToolCall{
					ID:       callID,
					ToolName: strings.TrimSpace(call.ToolName),
				}
				pendingOrder = append(pendingOrder, callID)
			}

		case "tool":
			filtered := filterToolMessageToPendingCalls(msg, pending)
			if filtered == nil {
				continue
			}
			repaired = append(repaired, *filtered)
			for _, result := range extractToolResultParts(*filtered) {
				callID := strings.TrimSpace(result.ToolCallID)
				delete(pending, callID)
			}
			if len(pending) == 0 && len(pendingOrder) > 0 {
				pendingOrder = pendingOrder[:0]
				continue
			}
			if len(pendingOrder) > 0 {
				nextOrder := pendingOrder[:0]
				for _, callID := range pendingOrder {
					if _, ok := pending[callID]; ok {
						nextOrder = append(nextOrder, callID)
					}
				}
				pendingOrder = nextOrder
			}

		default:
			if len(pending) > 0 {
				flushPending()
			}
			repaired = append(repaired, msg)
		}
	}

	if len(pending) > 0 {
		flushPending()
	}
	return repaired
}

func extractAssistantToolCallParts(msg ModelMessage) []sdk.ToolCallPart {
	sdkMsg := modelMessageToSDKMessage(msg)
	if len(sdkMsg.Content) == 0 {
		return nil
	}
	calls := make([]sdk.ToolCallPart, 0, len(sdkMsg.Content))
	for _, part := range sdkMsg.Content {
		call, ok := part.(sdk.ToolCallPart)
		if !ok {
			continue
		}
		calls = append(calls, call)
	}
	return calls
}

func extractToolResultParts(msg ModelMessage) []sdk.ToolResultPart {
	sdkMsg := modelMessageToSDKMessage(msg)
	if len(sdkMsg.Content) == 0 {
		return nil
	}
	results := make([]sdk.ToolResultPart, 0, len(sdkMsg.Content))
	for _, part := range sdkMsg.Content {
		result, ok := part.(sdk.ToolResultPart)
		if !ok {
			continue
		}
		results = append(results, result)
	}
	return results
}

func filterToolMessageToPendingCalls(msg ModelMessage, pending map[string]pendingToolCall) *ModelMessage {
	results := extractToolResultParts(msg)
	if len(results) == 0 {
		return nil
	}

	filtered := make([]sdk.ToolResultPart, 0, len(results))
	for _, result := range results {
		callID := strings.TrimSpace(result.ToolCallID)
		if _, ok := pending[callID]; !ok {
			continue
		}
		filtered = append(filtered, result)
	}
	if len(filtered) == 0 {
		return nil
	}

	converted := sdkMessagesToModelMessages([]sdk.Message{sdk.ToolMessage(filtered...)})
	if len(converted) == 0 {
		return nil
	}
	filteredMsg := converted[0]
	filteredMsg.Usage = msg.Usage
	return &filteredMsg
}

// normalizeDecisionProjectionResults repairs the one ordering exception made
// by External Agent decision persistence. The projection is stored while the turn is
// waiting, before earlier narration is flushed; move its real result directly
// behind the projection and drop duplicate legacy projection rows. IDs are
// scoped to one user turn so adapter-local counters can safely restart.
func normalizeDecisionProjectionResults(messages []ModelMessage) []ModelMessage {
	projected := map[projectedToolCallKey]struct{}{}
	results := map[projectedToolCallKey]sdk.ToolResultPart{}
	segment := 0
	for _, msg := range messages {
		switch strings.ToLower(strings.TrimSpace(msg.Role)) {
		case "assistant":
			for _, call := range extractAssistantToolCallParts(msg) {
				if isDecisionProjection(call) {
					key := projectedToolCallKey{segment, strings.TrimSpace(call.ToolCallID)}
					projected[key] = struct{}{}
					if _, exists := results[key]; !exists {
						if result, ok := resolvedUserInputResult(call); ok {
							results[key] = result
						}
					}
				}
			}
		case "tool":
			for _, result := range extractToolResultParts(msg) {
				key := projectedToolCallKey{segment, strings.TrimSpace(result.ToolCallID)}
				if _, ok := projected[key]; ok {
					results[key] = result
				}
			}
		default:
			segment++
		}
	}
	if len(results) == 0 {
		return messages
	}

	out := make([]ModelMessage, 0, len(messages))
	emitted := map[projectedToolCallKey]struct{}{}
	segment = 0
	for _, msg := range messages {
		sdkMsg := modelMessageToSDKMessage(msg)
		parts := make([]sdk.MessagePart, 0, len(sdkMsg.Content))
		pulled := make([]sdk.ToolResultPart, 0, 1)
		switch strings.ToLower(strings.TrimSpace(msg.Role)) {
		case "assistant":
			for _, part := range sdkMsg.Content {
				call, ok := part.(sdk.ToolCallPart)
				if !ok || !isDecisionProjection(call) {
					parts = append(parts, part)
					continue
				}
				key := projectedToolCallKey{segment, strings.TrimSpace(call.ToolCallID)}
				result, hasResult := results[key]
				if !hasResult {
					parts = append(parts, part)
					continue
				}
				if _, duplicate := emitted[key]; duplicate {
					continue
				}
				emitted[key] = struct{}{}
				parts = append(parts, part)
				pulled = append(pulled, result)
			}
		case "tool":
			for _, part := range sdkMsg.Content {
				result, ok := part.(sdk.ToolResultPart)
				if ok {
					key := projectedToolCallKey{segment, strings.TrimSpace(result.ToolCallID)}
					if _, moved := results[key]; moved {
						continue
					}
				}
				parts = append(parts, part)
			}
		default:
			parts = append(parts, sdkMsg.Content...)
			segment++
		}
		if normalized := modelMessageWithParts(msg, sdkMsg, parts); normalized != nil {
			out = append(out, *normalized)
		}
		if len(pulled) > 0 {
			out = append(out, sdkMessagesToModelMessages([]sdk.Message{sdk.ToolMessage(pulled...)})...)
		}
	}
	return out
}

func isDecisionProjection(call sdk.ToolCallPart) bool {
	if strings.TrimSpace(call.ToolCallID) == "" || call.ProviderMetadata == nil {
		return false
	}
	_, userInput := call.ProviderMetadata["user_input"]
	_, approval := call.ProviderMetadata["approval"]
	return userInput || approval
}

func resolvedUserInputResult(call sdk.ToolCallPart) (sdk.ToolResultPart, bool) {
	metadata, ok := call.ProviderMetadata["user_input"].(map[string]any)
	if !ok {
		return sdk.ToolResultPart{}, false
	}
	status, _ := metadata["status"].(string)
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "" || status == userinput.StatusPending {
		return sdk.ToolResultPart{}, false
	}
	result := map[string]any{"status": status}
	if answers := metadata["answers"]; answers != nil {
		result["answers"] = answers
	}
	return sdk.ToolResultPart{
		ToolCallID: strings.TrimSpace(call.ToolCallID),
		ToolName:   strings.TrimSpace(call.ToolName),
		Result:     result,
		IsError:    status == userinput.StatusExpired || status == userinput.StatusFailed,
	}, true
}

func modelMessageWithParts(original ModelMessage, sdkMsg sdk.Message, parts []sdk.MessagePart) *ModelMessage {
	if len(parts) == 0 {
		return nil
	}
	sdkMsg.Content = parts
	converted := sdkMessagesToModelMessages([]sdk.Message{sdkMsg})
	if len(converted) == 0 {
		return nil
	}
	converted[0].Usage = original.Usage
	return &converted[0]
}

func syntheticToolResultMessage(toolCallID, toolName, reason string) ModelMessage {
	converted := sdkMessagesToModelMessages([]sdk.Message{sdk.ToolMessage(sdk.ToolResultPart{
		ToolCallID: strings.TrimSpace(toolCallID),
		ToolName:   strings.TrimSpace(toolName),
		Result:     strings.TrimSpace(reason),
		IsError:    true,
	})})
	if len(converted) == 0 {
		return ModelMessage{
			Role:    "tool",
			Content: newTextContent(strings.TrimSpace(reason)),
		}
	}
	return converted[0]
}
