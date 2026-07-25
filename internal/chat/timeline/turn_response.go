package timeline

import (
	"encoding/json"
	"strings"

	"github.com/memohai/memoh/internal/agent/turn"
	messagepkg "github.com/memohai/memoh/internal/chat/message"
)

// DecodeTurnResponseEntry converts a persisted bot message into a TR entry for
// pipeline context composition.
//
// Tool calls and tool results are preserved as native structured content
// parts. They must not be rendered as assistant-visible pseudo-protocol text:
// models may imitate that text instead of emitting real provider tool calls.
func DecodeTurnResponseEntry(msg messagepkg.Message) (TurnResponseEntry, bool) {
	role := strings.TrimSpace(msg.Role)
	if role != "assistant" && role != "tool" {
		return TurnResponseEntry{}, false
	}

	var modelMsg turn.ModelMessage
	if err := json.Unmarshal(msg.Content, &modelMsg); err != nil {
		return TurnResponseEntry{}, false
	}

	var rawContent json.RawMessage
	switch role {
	case "tool":
		rawContent = nativeToolRoleContent(modelMsg)
	default:
		rawContent = nativeAssistantContent(modelMsg)
	}

	if len(rawContent) == 0 || strings.TrimSpace(string(rawContent)) == "" {
		return TurnResponseEntry{}, false
	}

	return TurnResponseEntry{
		RequestedAtMs:   msg.CreatedAt.UnixMilli(),
		Role:            role,
		Content:         debugContent(rawContent),
		RawContent:      rawContent,
		SourceMessageID: msg.ID,
	}, true
}

// turnResponsePart is a permissive view of a persisted content part. It
// purposefully uses json.RawMessage for tool input/output to avoid losing
// structure while keeping the type declaration local to this package.
type turnResponsePart struct {
	Type             string          `json:"type"`
	Text             string          `json:"text,omitempty"`
	ToolCallID       string          `json:"toolCallId,omitempty"`
	ToolName         string          `json:"toolName,omitempty"`
	Input            json.RawMessage `json:"input,omitempty"`
	Output           json.RawMessage `json:"output,omitempty"`
	Result           json.RawMessage `json:"result,omitempty"`
	ProviderMetadata json.RawMessage `json:"providerMetadata,omitempty"`
}

func nativeAssistantContent(msg turn.ModelMessage) json.RawMessage {
	var out []map[string]any
	// 1) Plain-string content (legacy format).
	if len(msg.Content) > 0 {
		var plain string
		if err := json.Unmarshal(msg.Content, &plain); err == nil {
			plain = strings.TrimSpace(plain)
			if plain != "" {
				out = append(out, map[string]any{
					"type": "text",
					"text": plain,
				})
			}
		}
	}

	// 2) Array-of-parts content (Vercel AI SDK uiMessage format).
	var parts []turnResponsePart
	if len(msg.Content) > 0 {
		_ = json.Unmarshal(msg.Content, &parts)
	}

	for _, p := range parts {
		switch strings.ToLower(strings.TrimSpace(p.Type)) {
		case "text":
			text := strings.TrimSpace(p.Text)
			if text == "" {
				continue
			}
			out = append(out, map[string]any{
				"type": "text",
				"text": text,
			})
		case "reasoning":
			// Intentionally omitted: reasoning is model-internal and must not
			// leak back into subsequent prompts verbatim.
			continue
		case "tool-call":
			out = append(out, nativeToolCallPart(p.ToolCallID, p.ToolName, p.Input, p.ProviderMetadata))
		case "tool-result":
			payload := p.Output
			if len(payload) == 0 {
				payload = p.Result
			}
			out = append(out, nativeToolResultPart(p.ToolCallID, p.ToolName, payload))
		}
	}

	// 3) Top-level ToolCalls field (older OpenAI-style wire format).
	for _, call := range msg.ToolCalls {
		id := strings.TrimSpace(call.ID)
		name := strings.TrimSpace(call.Function.Name)
		args := strings.TrimSpace(call.Function.Arguments)
		var input json.RawMessage
		if args != "" {
			// Arguments is a string containing JSON; try to keep it raw so
			// the downstream renderer doesn't double-escape.
			if json.Valid([]byte(args)) {
				input = json.RawMessage(args)
			} else {
				encoded, _ := json.Marshal(args)
				input = encoded
			}
		}
		out = append(out, nativeToolCallPart(id, name, input, nil))
	}

	return marshalParts(out)
}

func nativeToolRoleContent(msg turn.ModelMessage) json.RawMessage {
	// Two possible persistence shapes:
	//   a) Content is a JSON array of parts with type="tool-result".
	//   b) Content is the tool result itself, and ToolCallID is set on the
	//      ModelMessage envelope (older OpenAI-style format).
	var out []map[string]any

	var parts []turnResponsePart
	if len(msg.Content) > 0 {
		_ = json.Unmarshal(msg.Content, &parts)
	}
	for _, p := range parts {
		if strings.ToLower(strings.TrimSpace(p.Type)) != "tool-result" {
			continue
		}
		payload := p.Output
		if len(payload) == 0 {
			payload = p.Result
		}
		out = append(out, nativeToolResultPart(p.ToolCallID, p.ToolName, payload))
	}

	if len(out) > 0 {
		return marshalParts(out)
	}
	if strings.TrimSpace(msg.ToolCallID) != "" {
		out = append(out, nativeToolResultPart(msg.ToolCallID, msg.Name, msg.Content))
	}
	return marshalParts(out)
}

func nativeToolCallPart(id, name string, input, providerMetadata json.RawMessage) map[string]any {
	part := map[string]any{
		"type":       "tool-call",
		"toolCallId": strings.TrimSpace(id),
		"toolName":   strings.TrimSpace(name),
	}
	if len(input) > 0 && strings.TrimSpace(string(input)) != "" {
		part["input"] = input
	} else {
		part["input"] = map[string]any{}
	}
	if len(providerMetadata) > 0 && strings.TrimSpace(string(providerMetadata)) != "" {
		part["providerMetadata"] = providerMetadata
	}
	return part
}

func nativeToolResultPart(id, name string, payload json.RawMessage) map[string]any {
	part := map[string]any{
		"type":       "tool-result",
		"toolCallId": strings.TrimSpace(id),
		"toolName":   strings.TrimSpace(name),
	}
	if len(payload) > 0 && strings.TrimSpace(string(payload)) != "" {
		part["result"] = payload
	} else {
		part["result"] = nil
	}
	return part
}

func marshalParts(parts []map[string]any) json.RawMessage {
	if len(parts) == 0 {
		return nil
	}
	raw, err := json.Marshal(parts)
	if err != nil {
		return nil
	}
	return raw
}

func debugContent(raw json.RawMessage) string {
	var plain string
	if err := json.Unmarshal(raw, &plain); err == nil {
		return plain
	}
	var parts []turnResponsePart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return string(raw)
	}
	var texts []string
	for _, p := range parts {
		switch strings.ToLower(strings.TrimSpace(p.Type)) {
		case "text":
			if text := strings.TrimSpace(p.Text); text != "" {
				texts = append(texts, text)
			}
		case "tool-call":
			texts = append(texts, "[tool call: "+strings.TrimSpace(p.ToolName)+"]")
		case "tool-result":
			texts = append(texts, "[tool result: "+strings.TrimSpace(p.ToolName)+"]")
		}
	}
	return strings.Join(texts, "\n")
}
