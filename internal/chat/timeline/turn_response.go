package timeline

import (
	"encoding/json"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/turn"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/messageconv"
)

// DecodeTurnResponseEntries converts a chronological run of persisted bot
// messages into TR entries for pipeline context composition.
//
// Decoding the run as a whole is what lets an interrupted checkpoint's
// reasoning be projected only while it is still the unfinished frontier; a
// single message cannot tell whether a completed answer already followed it.
func DecodeTurnResponseEntries(msgs []messagepkg.Message) []TurnResponseEntry {
	checkpoint := messagepkg.LatestInterruptedCheckpoint(len(msgs), func(i int) (bool, bool) {
		return strings.EqualFold(strings.TrimSpace(msgs[i].Role), "assistant"),
			msgs[i].Metadata[messagepkg.AgentStepInterruptedMetadataKey] == true
	})
	entries := make([]TurnResponseEntry, 0, len(msgs))
	for i, msg := range msgs {
		if entry, ok := decodeTurnResponseEntry(msg, i == checkpoint); ok {
			entries = append(entries, entry)
		}
	}
	return entries
}

// DecodeTurnResponseEntry converts a single persisted bot message into a TR
// entry. Reasoning is always dropped: an interrupted checkpoint's reasoning
// needs the surrounding history to know whether it is still live, so callers
// composing context use DecodeTurnResponseEntries instead.
func DecodeTurnResponseEntry(msg messagepkg.Message) (TurnResponseEntry, bool) {
	return decodeTurnResponseEntry(msg, false)
}

// Tool calls and tool results are preserved as native structured content
// parts. They must not be rendered as assistant-visible pseudo-protocol text:
// models may imitate that text instead of emitting real provider tool calls.
func decodeTurnResponseEntry(msg messagepkg.Message, liveCheckpoint bool) (TurnResponseEntry, bool) {
	role := strings.ToLower(strings.TrimSpace(msg.Role))
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
		if liveCheckpoint {
			modelMsg = ProjectInterruptedReasoning(modelMsg)
		}
		rawContent = nativeAssistantContent(modelMsg, liveCheckpoint)
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

// ProjectInterruptedReasoning exposes an unfinished reasoning part as text so
// every provider can continue it on the next turn. Other message fields and
// content parts remain unchanged.
func ProjectInterruptedReasoning(modelMsg turn.ModelMessage) turn.ModelMessage {
	message := messageconv.ModelMessageToSDKMessage(modelMsg)
	var reasoning strings.Builder
	parts := make([]sdk.MessagePart, 0, len(message.Content))
	for _, part := range message.Content {
		switch p := part.(type) {
		case sdk.ReasoningPart:
			reasoning.WriteString(p.Text)
		case *sdk.ReasoningPart:
			reasoning.WriteString(p.Text)
		default:
			parts = append(parts, part)
		}
	}
	if strings.TrimSpace(reasoning.String()) == "" {
		return modelMsg
	}

	checkpoint := messagepkg.AgentStepInterruptedReasoningPrefix + reasoning.String()
	for i, part := range parts {
		switch p := part.(type) {
		case sdk.TextPart:
			p.Text = checkpoint + "\n\n" + p.Text
			parts[i], checkpoint = p, ""
		case *sdk.TextPart:
			clone := *p
			clone.Text = checkpoint + "\n\n" + clone.Text
			parts[i], checkpoint = clone, ""
		}
		if checkpoint == "" {
			break
		}
	}
	if checkpoint != "" {
		parts = append([]sdk.MessagePart{sdk.TextPart{Text: checkpoint}}, parts...)
	}
	message.Content = parts
	projected := messageconv.SDKMessagesToModelMessages([]sdk.Message{message})[0]
	projected.Usage = modelMsg.Usage
	projected.ToolCalls = modelMsg.ToolCalls
	projected.ToolCallID = modelMsg.ToolCallID
	projected.Name = modelMsg.Name
	return projected
}

// turnResponsePart is a permissive view of a persisted content part. It
// purposefully uses json.RawMessage for tool input/output to avoid losing
// structure while keeping the type declaration local to this package.
type turnResponsePart struct {
	Type             string          `json:"type"`
	ID               string          `json:"id,omitempty"`
	Text             string          `json:"text,omitempty"`
	Format           string          `json:"format,omitempty"`
	Model            string          `json:"model,omitempty"`
	ToolCallID       string          `json:"toolCallId,omitempty"`
	ToolName         string          `json:"toolName,omitempty"`
	Input            json.RawMessage `json:"input,omitempty"`
	Output           json.RawMessage `json:"output,omitempty"`
	Result           json.RawMessage `json:"result,omitempty"`
	ProviderMetadata json.RawMessage `json:"providerMetadata,omitempty"`
}

func nativeAssistantContent(msg turn.ModelMessage, liveCheckpoint bool) json.RawMessage {
	var out []map[string]any
	modernToolCallIDs := make(map[string]struct{})
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
			hasMetadata := hasJSONPayload(p.ProviderMetadata)
			text := strings.TrimSpace(p.Text)
			if text == "" && !hasMetadata {
				continue
			}
			if hasMetadata {
				// Provider metadata can be bound to the exact text block. Keep
				// its whitespace unchanged when carrying that metadata forward.
				text = p.Text
			}
			textPart := map[string]any{
				"type": "text",
				"text": text,
			}
			if hasMetadata {
				textPart["providerMetadata"] = p.ProviderMetadata
			}
			out = append(out, textPart)
		case "reasoning":
			if !liveCheckpoint || !hasReplayableReasoningPayload(p) {
				continue
			}
			reasoning := map[string]any{
				"type": "reasoning",
				"text": p.Text,
			}
			if strings.TrimSpace(p.ID) != "" {
				reasoning["id"] = p.ID
			}
			if strings.TrimSpace(p.Format) != "" {
				reasoning["format"] = p.Format
			}
			if strings.TrimSpace(p.Model) != "" {
				reasoning["model"] = p.Model
			}
			if hasJSONPayload(p.ProviderMetadata) {
				reasoning["providerMetadata"] = p.ProviderMetadata
			}
			out = append(out, reasoning)
		case "tool-call":
			if id := strings.TrimSpace(p.ToolCallID); id != "" {
				modernToolCallIDs[id] = struct{}{}
			}
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
		if _, alreadyProjected := modernToolCallIDs[id]; id != "" && alreadyProjected {
			continue
		}
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

func hasReplayableReasoningPayload(part turnResponsePart) bool {
	return strings.TrimSpace(part.Text) != "" || hasJSONPayload(part.ProviderMetadata)
}

func hasJSONPayload(raw json.RawMessage) bool {
	switch strings.TrimSpace(string(raw)) {
	case "", "null", "{}":
		return false
	default:
		return true
	}
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
