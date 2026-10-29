package application

import (
	"bytes"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/partmeta"
	"github.com/felinics/memoh/internal/agent/toolexec"
	turnpkg "github.com/felinics/memoh/internal/agent/turn"
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

// repairToolCallClosures closes every tool call left without a result and
// drops results no call is waiting for, so the transcript satisfies providers
// that enforce strict assistant-tool adjacency.
func repairToolCallClosures(messages []ModelMessage, reason string) []ModelMessage {
	if len(messages) == 0 {
		return messages
	}
	return contextModelMessages(repairContextToolCalls(newContextMessages(messages), reason))
}

// repairContextToolCalls is repairToolCallClosures over converted messages.
// A message it keeps as it is stays the same value; only a message it
// rewrites or synthesizes is converted again.
func repairContextToolCalls(messages []contextMessage, reason string) []contextMessage {
	if len(messages) == 0 {
		return messages
	}
	messages = normalizeDecisionProjectionResults(messages)
	if strings.TrimSpace(reason) == "" {
		reason = syntheticToolClosureError
	}

	repaired := make([]contextMessage, 0, len(messages))
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
			repaired = append(repaired, newContextMessage(syntheticToolResultMessage(call.ID, call.ToolName, reason)))
			delete(pending, callID)
		}
		pendingOrder = pendingOrder[:0]
	}

	for _, msg := range messages {
		role := strings.ToLower(strings.TrimSpace(msg.model.Role))
		switch role {
		case "assistant":
			if len(pending) > 0 {
				flushPending()
			}
			repaired = append(repaired, msg)
			for _, call := range toolCallParts(msg.sent) {
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
			for _, result := range toolResultParts(filtered.sent) {
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
	return toolCallParts(modelMessageToSDKMessage(msg))
}

func extractToolResultParts(msg ModelMessage) []sdk.ToolResultPart {
	return toolResultParts(modelMessageToSDKMessage(msg))
}

func toolCallParts(msg sdk.Message) []sdk.ToolCallPart {
	if len(msg.Content) == 0 {
		return nil
	}
	calls := make([]sdk.ToolCallPart, 0, len(msg.Content))
	for _, part := range msg.Content {
		call, ok := part.(sdk.ToolCallPart)
		if !ok {
			continue
		}
		calls = append(calls, call)
	}
	return calls
}

func toolResultParts(msg sdk.Message) []sdk.ToolResultPart {
	if len(msg.Content) == 0 {
		return nil
	}
	results := make([]sdk.ToolResultPart, 0, len(msg.Content))
	for _, part := range msg.Content {
		result, ok := part.(sdk.ToolResultPart)
		if !ok {
			continue
		}
		results = append(results, result)
	}
	return results
}

// filterToolMessageToPendingCalls keeps the results a pending call waits
// for, as a tool message in parts form. A message already in that form whose
// every part is such a result is kept as it is.
func filterToolMessageToPendingCalls(msg contextMessage, pending map[string]pendingToolCall) *contextMessage {
	results := toolResultParts(msg.sent)
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
	if len(filtered) == len(msg.sent.Content) && storedInPartsForm(msg.model) {
		return &msg
	}

	converted := sdkMessagesToModelMessages([]sdk.Message{sdk.ToolMessage(filtered...)})
	if len(converted) == 0 {
		return nil
	}
	filteredMsg := converted[0]
	filteredMsg.Usage = msg.model.Usage
	filteredMsg.ContextSource = msg.model.ContextSource
	out := newContextMessage(filteredMsg)
	return &out
}

// projectedResult is the result a decision projection takes, and the stored
// message it came from when it is the tool's real result.
type projectedResult struct {
	part   sdk.ToolResultPart
	source *turnpkg.ContextMessageSource
}

// normalizeDecisionProjectionResults repairs the one ordering exception made
// by External Agent decision persistence. The projection is stored while the turn is
// waiting, before earlier narration is flushed; move its real result directly
// behind the projection and drop duplicate legacy projection rows. IDs are
// scoped to one user turn so adapter-local counters can safely restart.
func normalizeDecisionProjectionResults(messages []contextMessage) []contextMessage {
	projected := map[projectedToolCallKey]struct{}{}
	results := map[projectedToolCallKey]projectedResult{}
	segment := 0
	for _, msg := range messages {
		switch strings.ToLower(strings.TrimSpace(msg.model.Role)) {
		case "assistant":
			for _, call := range toolCallParts(msg.sent) {
				if isDecisionProjection(call) {
					key := projectedToolCallKey{segment, strings.TrimSpace(call.ToolCallID)}
					projected[key] = struct{}{}
					if _, exists := results[key]; !exists {
						if result, ok := resolvedDecisionResult(call); ok {
							results[key] = projectedResult{part: result}
						}
					}
				}
			}
		case "tool":
			for _, result := range toolResultParts(msg.sent) {
				key := projectedToolCallKey{segment, strings.TrimSpace(result.ToolCallID)}
				if _, ok := projected[key]; ok {
					results[key] = projectedResult{part: result, source: msg.model.ContextSource}
				}
			}
		default:
			segment++
		}
	}
	if len(results) == 0 {
		return messages
	}

	out := make([]contextMessage, 0, len(messages))
	emitted := map[projectedToolCallKey]struct{}{}
	segment = 0
	for _, msg := range messages {
		parts := make([]sdk.MessagePart, 0, len(msg.sent.Content))
		var pulled []projectedResult
		switch strings.ToLower(strings.TrimSpace(msg.model.Role)) {
		case "assistant":
			for _, part := range msg.sent.Content {
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
			for _, part := range msg.sent.Content {
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
			parts = append(parts, msg.sent.Content...)
			segment++
		}
		if normalized := contextMessageWithParts(msg, parts); normalized != nil {
			out = append(out, *normalized)
		}
		if len(pulled) > 0 {
			out = append(out, pulledResultMessages(pulled)...)
		}
	}
	return out
}

// pulledResultMessages renders moved results as one tool message. It renders
// the stored result it moved, so it keeps that result's source.
func pulledResultMessages(pulled []projectedResult) []contextMessage {
	parts := make([]sdk.ToolResultPart, 0, len(pulled))
	var source *turnpkg.ContextMessageSource
	for _, result := range pulled {
		parts = append(parts, result.part)
		if source == nil {
			source = result.source
		}
	}
	converted := sdkMessagesToModelMessages([]sdk.Message{sdk.ToolMessage(parts...)})
	out := make([]contextMessage, 0, len(converted))
	for _, message := range converted {
		message.ContextSource = source
		out = append(out, newContextMessage(message))
	}
	return out
}

func isDecisionProjection(call sdk.ToolCallPart) bool {
	if strings.TrimSpace(call.ToolCallID) == "" || call.ProviderMetadata == nil {
		return false
	}
	return partmeta.Has(call.ProviderMetadata, partmeta.KeyUserInput) || partmeta.Has(call.ProviderMetadata, partmeta.KeyApproval)
}

func resolvedDecisionResult(call sdk.ToolCallPart) (sdk.ToolResultPart, bool) {
	metadata, ok := partmeta.Object(call.ProviderMetadata, partmeta.KeyUserInput)
	// A standalone permission card represents only the decision. Approval
	// metadata on a real tool call must still wait for that tool's result.
	if !ok && call.ToolName == "permission" {
		metadata, ok = partmeta.Object(call.ProviderMetadata, partmeta.KeyApproval)
	}
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
		Result:     toolexec.OutputFromValue(result),
		IsError:    status == userinput.StatusExpired || status == userinput.StatusFailed,
	}, true
}

// storedInPartsForm reports whether a message is already in the form its
// provider form converts back to: a parts array without the legacy
// tool_calls, tool_call_id and name envelope fields.
func storedInPartsForm(message ModelMessage) bool {
	if len(message.ToolCalls) > 0 || strings.TrimSpace(message.ToolCallID) != "" || strings.TrimSpace(message.Name) != "" {
		return false
	}
	content := bytes.TrimSpace(message.Content)
	return len(content) > 0 && content[0] == '['
}

// contextMessageWithParts is msg with its provider parts replaced, or nil
// when none remain. Unchanged parts keep msg as it is.
func contextMessageWithParts(msg contextMessage, parts []sdk.MessagePart) *contextMessage {
	if len(parts) == 0 {
		return nil
	}
	if len(parts) == len(msg.sent.Content) {
		return &msg
	}
	sent := msg.sent
	sent.Content = parts
	converted := sdkMessagesToModelMessages([]sdk.Message{sent})
	if len(converted) == 0 {
		return nil
	}
	converted[0].Usage = msg.model.Usage
	converted[0].ContextSource = msg.model.ContextSource
	out := newContextMessage(converted[0])
	return &out
}

func syntheticToolResultMessage(toolCallID, toolName, reason string) ModelMessage {
	converted := sdkMessagesToModelMessages([]sdk.Message{sdk.ToolMessage(sdk.ToolResultPart{
		ToolCallID: strings.TrimSpace(toolCallID),
		ToolName:   strings.TrimSpace(toolName),
		Result:     toolexec.OutputFromValue(strings.TrimSpace(reason)),
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
