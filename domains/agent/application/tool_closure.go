package application

import (
	"strings"

	sdk "github.com/memohai/twilight-ai/sdk"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/convert"
)

const syntheticToolClosureError = "tool execution interrupted before a response was recorded"

type pendingToolCall struct {
	ID       string
	ToolName string
}

func repairToolCallClosures(messages []agentdomain.ModelMessage, reason string) []agentdomain.ModelMessage {
	if len(messages) == 0 {
		return messages
	}
	if strings.TrimSpace(reason) == "" {
		reason = syntheticToolClosureError
	}

	repaired := make([]agentdomain.ModelMessage, 0, len(messages))
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
			filtered := filterToolMessageToPending(msg, pending)
			if filtered == nil {
				continue
			}
			repaired = append(repaired, *filtered)
			for _, result := range extractToolResultParts(*filtered) {
				delete(pending, strings.TrimSpace(result.ToolCallID))
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

func extractAssistantToolCallParts(msg agentdomain.ModelMessage) []sdk.ToolCallPart {
	sdkMsg := convert.ModelMessageToSDKMessage(msg)
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

func extractToolResultParts(msg agentdomain.ModelMessage) []sdk.ToolResultPart {
	sdkMsg := convert.ModelMessageToSDKMessage(msg)
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

func filterToolMessageToPending(msg agentdomain.ModelMessage, pending map[string]pendingToolCall) *agentdomain.ModelMessage {
	results := extractToolResultParts(msg)
	if len(results) == 0 {
		return nil
	}

	filtered := make([]sdk.ToolResultPart, 0, len(results))
	for _, result := range results {
		if _, ok := pending[strings.TrimSpace(result.ToolCallID)]; !ok {
			continue
		}
		filtered = append(filtered, result)
	}
	if len(filtered) == 0 {
		return nil
	}

	converted := convert.SDKMessagesToModelMessages([]sdk.Message{sdk.ToolMessage(filtered...)})
	if len(converted) == 0 {
		return nil
	}
	filteredMsg := converted[0]
	filteredMsg.Usage = msg.Usage
	return &filteredMsg
}

func syntheticToolResultMessage(toolCallID, toolName, reason string) agentdomain.ModelMessage {
	converted := convert.SDKMessagesToModelMessages([]sdk.Message{sdk.ToolMessage(sdk.ToolResultPart{
		ToolCallID: strings.TrimSpace(toolCallID),
		ToolName:   strings.TrimSpace(toolName),
		Result:     strings.TrimSpace(reason),
		IsError:    true,
	})})
	if len(converted) == 0 {
		return agentdomain.ModelMessage{
			Role:    "tool",
			Content: agentdomain.NewTextContent(strings.TrimSpace(reason)),
		}
	}
	return converted[0]
}
