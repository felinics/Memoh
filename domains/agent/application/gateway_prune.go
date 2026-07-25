package application

import (
	"encoding/json"
	"strings"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/context/history"
	textprune "github.com/memohai/memoh/domains/agent/chat/text/prune"
)

const (
	// Prune long tool payloads per message to keep gateway requests within provider limits,
	// while preserving as much surrounding context as possible.
	gatewayToolPayloadMaxBytes = textprune.DefaultMaxBytes
	gatewayToolPayloadMaxLines = textprune.DefaultMaxLines

	gatewayToolResultHeadBytes = 6 * 1024
	gatewayToolResultTailBytes = 2 * 1024
	gatewayToolResultHeadLines = 180
	gatewayToolResultTailLines = 50

	gatewayToolArgsHeadBytes = 4 * 1024
	gatewayToolArgsTailBytes = 2 * 1024
	gatewayToolArgsHeadLines = 180
	gatewayToolArgsTailLines = 50

	gatewayToolPayloadPrunedMarker = textprune.DefaultMarker
)

func pruneHistoryForGateway(messages []history.HistoryRecord) []history.HistoryRecord {
	if len(messages) == 0 {
		return messages
	}
	out := make([]history.HistoryRecord, 0, len(messages))
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

func pruneMessagesForGateway(messages []agentdomain.ModelMessage) []agentdomain.ModelMessage {
	if len(messages) == 0 {
		return messages
	}
	out := make([]agentdomain.ModelMessage, 0, len(messages))
	for _, msg := range messages {
		pruned, _ := pruneMessageForGateway(msg)
		out = append(out, pruned)
	}
	return out
}

func pruneMessageForGateway(msg agentdomain.ModelMessage) (agentdomain.ModelMessage, bool) {
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

func pruneToolCalls(calls []agentdomain.ToolCall) ([]agentdomain.ToolCall, bool) {
	changed := false
	out := make([]agentdomain.ToolCall, len(calls))
	for i, call := range calls {
		out[i] = call
		args := call.Function.Arguments
		if args == "" || !exceedsTextBudget(args) {
			continue
		}
		out[i].Function.Arguments = pruneStringEdges(
			args,
			gatewayToolArgsHeadBytes,
			gatewayToolArgsTailBytes,
			gatewayToolArgsHeadLines,
			gatewayToolArgsTailLines,
			"tool arguments",
		)
		changed = true
	}
	return out, changed
}

func pruneToolMessage(msg agentdomain.ModelMessage) (agentdomain.ModelMessage, bool) {
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
	msg.Content = agentdomain.NewTextContent(pruneStringEdges(
		text,
		gatewayToolResultHeadBytes,
		gatewayToolResultTailBytes,
		gatewayToolResultHeadLines,
		gatewayToolResultTailLines,
		"tool result",
	))
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
		s = pruneStringEdges(
			s,
			gatewayToolResultHeadBytes,
			gatewayToolResultTailBytes,
			gatewayToolResultHeadLines,
			gatewayToolResultTailLines,
			"tool result",
		)
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
		pruned := pruneStringEdges(
			string(valueRaw),
			gatewayToolResultHeadBytes,
			gatewayToolResultTailBytes,
			gatewayToolResultHeadLines,
			gatewayToolResultTailLines,
			"tool result (json)",
		)
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
			items[i]["text"] = pruneStringEdges(
				text,
				gatewayToolResultHeadBytes,
				gatewayToolResultTailBytes,
				gatewayToolResultHeadLines,
				gatewayToolResultTailLines,
				"tool result (content)",
			)
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

func pruneStringEdges(s string, headBytes, tailBytes, headLines, tailLines int, label string) string {
	return textprune.PruneWithEdges(s, label, textprune.Config{
		MaxBytes:  gatewayToolPayloadMaxBytes,
		MaxLines:  gatewayToolPayloadMaxLines,
		HeadBytes: headBytes,
		TailBytes: tailBytes,
		HeadLines: headLines,
		TailLines: tailLines,
		Marker:    gatewayToolPayloadPrunedMarker,
	})
}

func exceedsTextBudget(s string) bool {
	return textprune.Exceeds(s, gatewayToolPayloadMaxBytes, gatewayToolPayloadMaxLines)
}
