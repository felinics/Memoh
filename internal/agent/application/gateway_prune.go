package application

import (
	"encoding/json"
	"strings"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	contextlimit "github.com/felinics/memoh/internal/agent/context/limit"
	textprune "github.com/felinics/memoh/internal/prune"
)

const (
	// Prune long tool payloads per message to keep gateway requests within provider limits,
	// while preserving as much surrounding context as possible.
	gatewayToolPayloadMaxBytes = textprune.DefaultMaxBytes
	gatewayToolPayloadMaxLines = textprune.DefaultMaxLines

	gatewayToolPayloadPrunedMarker = textprune.DefaultMarker
)

func pruneHistoryForGateway(messages []historyfrag.HistoryRecord) []historyfrag.HistoryRecord {
	if len(messages) == 0 {
		return messages
	}
	out := make([]historyfrag.HistoryRecord, 0, len(messages))
	staleUsage := false
	for _, item := range messages {
		msg, changed := pruneMessageForGateway(item.ModelMessage)
		if changed {
			item.ModelMessage = msg
			staleUsage = true
		}
		if staleUsage {
			item.UsageInputTokens = nil
		}
		out = append(out, item)
	}
	return out
}

func pruneMessagesForGateway(messages []ModelMessage) []ModelMessage {
	if len(messages) == 0 {
		return messages
	}
	out := make([]ModelMessage, 0, len(messages))
	for _, msg := range messages {
		pruned, _ := pruneMessageForGateway(msg)
		out = append(out, pruned)
	}
	return out
}

func pruneMessageForGateway(msg ModelMessage) (ModelMessage, bool) {
	changed := false
	if strings.EqualFold(strings.TrimSpace(msg.Role), "tool") {
		msg2, did := pruneToolMessage(msg)
		if did {
			msg = msg2
			changed = true
		}
	}
	if len(msg.ToolCalls) > 0 {
		calls, did := pruneToolCalls(msg.ToolCalls)
		if did {
			msg.ToolCalls = calls
			changed = true
		}
	}
	return msg, changed
}

func pruneToolCalls(calls []ToolCall) ([]ToolCall, bool) {
	changed := false
	out := make([]ToolCall, len(calls))
	for i, call := range calls {
		out[i] = call
		args := call.Function.Arguments
		if args == "" || !exceedsTextBudget(args) {
			continue
		}
		out[i].Function.Arguments = contextlimit.GatewayArgsTier.LimitString(args, "tool arguments")
		changed = true
	}
	return out, changed
}

func pruneToolMessage(msg ModelMessage) (ModelMessage, bool) {
	// Vercel AI SDK schema requires tool messages to carry an array of tool-result parts.
	// Prune outputs inside those parts (preserving shape) so the gateway prompt remains valid.
	if pruned, ok := pruneToolResultParts(msg.Content); ok {
		msg.Content = pruned
		return msg, true
	}

	// Backward-compat: tool messages may have been persisted as plain strings.
	text := msg.TextContent()
	if !exceedsTextBudget(text) {
		return msg, false
	}
	msg.Content = newTextContent(contextlimit.GatewayResultTier.LimitString(text, "tool result"))
	return msg, true
}

func pruneToolResultParts(content json.RawMessage) (json.RawMessage, bool) {
	if len(content) == 0 {
		return nil, false
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(content, &parts); err != nil || len(parts) == 0 {
		return nil, false
	}

	changed := false
	out := make([]json.RawMessage, 0, len(parts))
	for _, raw := range parts {
		var part map[string]json.RawMessage
		if err := json.Unmarshal(raw, &part); err != nil {
			out = append(out, raw)
			continue
		}

		partTypeRaw, ok := part["type"]
		if !ok {
			out = append(out, raw)
			continue
		}
		var partType string
		if err := json.Unmarshal(partTypeRaw, &partType); err != nil || partType != "tool-result" {
			out = append(out, raw)
			continue
		}

		outputRaw, ok := part["output"]
		if !ok {
			out = append(out, raw)
			continue
		}
		pruned, didPrune := pruneToolOutput(outputRaw)
		if !didPrune {
			out = append(out, raw)
			continue
		}

		part["output"] = pruned
		rebuilt, err := json.Marshal(part)
		if err != nil {
			out = append(out, raw)
			continue
		}
		out = append(out, json.RawMessage(rebuilt))
		changed = true
	}

	if !changed {
		return nil, false
	}
	rebuilt, err := json.Marshal(out)
	if err != nil {
		return nil, false
	}
	return json.RawMessage(rebuilt), true
}

func pruneToolOutput(raw json.RawMessage) (json.RawMessage, bool) {
	var output map[string]json.RawMessage
	if err := json.Unmarshal(raw, &output); err != nil {
		return nil, false
	}
	outputTypeRaw, ok := output["type"]
	if !ok {
		return nil, false
	}
	var outputType string
	if err := json.Unmarshal(outputTypeRaw, &outputType); err != nil {
		return nil, false
	}
	valueRaw, hasValue := output["value"]

	switch outputType {
	case "text", "error-text":
		if !hasValue {
			return nil, false
		}
		var s string
		if err := json.Unmarshal(valueRaw, &s); err != nil || !exceedsTextBudget(s) {
			return nil, false
		}
		s = contextlimit.GatewayResultTier.LimitString(s, "tool result")
		data, err := json.Marshal(s)
		if err != nil {
			return nil, false
		}
		output["value"] = data
		rebuilt, err := json.Marshal(output)
		if err != nil {
			return nil, false
		}
		return json.RawMessage(rebuilt), true

	case "json", "error-json":
		if !hasValue || !exceedsTextBudget(string(valueRaw)) {
			return nil, false
		}
		pruned := contextlimit.GatewayResultTier.LimitString(string(valueRaw), "tool result (json)")
		data, err := json.Marshal(pruned)
		if err != nil {
			return nil, false
		}
		output["value"] = data
		rebuilt, err := json.Marshal(output)
		if err != nil {
			return nil, false
		}
		return json.RawMessage(rebuilt), true

	case "content":
		// Best-effort: prune any large text items inside the content array.
		// If parsing fails, keep the original output to avoid breaking schema.
		if !hasValue {
			return nil, false
		}
		var items []map[string]any
		if err := json.Unmarshal(valueRaw, &items); err != nil {
			return nil, false
		}
		didPrune := false
		for i := range items {
			if items[i]["type"] != "text" {
				continue
			}
			textAny, ok := items[i]["text"]
			if !ok {
				continue
			}
			text, ok := textAny.(string)
			if !ok || !exceedsTextBudget(text) {
				continue
			}
			items[i]["text"] = contextlimit.GatewayResultTier.LimitString(text, "tool result (content)")
			didPrune = true
		}
		if !didPrune {
			return nil, false
		}
		data, err := json.Marshal(items)
		if err != nil {
			return nil, false
		}
		output["value"] = data
		rebuilt, err := json.Marshal(output)
		if err != nil {
			return nil, false
		}
		return json.RawMessage(rebuilt), true

	default:
		return nil, false
	}
}

func exceedsTextBudget(s string) bool {
	return textprune.Exceeds(s, gatewayToolPayloadMaxBytes, gatewayToolPayloadMaxLines)
}
