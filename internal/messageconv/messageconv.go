package messageconv

import (
	"encoding/json"
	"log/slog"
	"strings"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/agent/turn"
)

func SDKMessagesToModelMessages(msgs []sdk.Message) []turn.ModelMessage {
	return SDKMessagesToModelMessagesWithLogger(nil, msgs)
}

func SDKMessagesToModelMessagesWithLogger(log *slog.Logger, msgs []sdk.Message) []turn.ModelMessage {
	result := make([]turn.ModelMessage, 0, len(msgs))
	for _, msg := range msgs {
		data, err := json.Marshal(msg)
		if err != nil {
			if log != nil {
				log.Warn("messageconv: sdk message marshal failed", slog.String("role", string(msg.Role)), slog.Any("error", err))
			}
			continue
		}
		var envelope struct {
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			if log != nil {
				log.Warn("messageconv: sdk message content extract failed", slog.String("role", string(msg.Role)), slog.Any("error", err))
			}
			continue
		}
		var usage json.RawMessage
		if msg.Usage != nil {
			usage, _ = json.Marshal(msg.Usage)
		}
		result = append(result, turn.ModelMessage{
			Role:    string(msg.Role),
			Content: envelope.Content,
			Usage:   usage,
		})
	}
	return result
}

func ModelMessageToSDKMessage(mm turn.ModelMessage) sdk.Message {
	var s string
	if err := json.Unmarshal(mm.Content, &s); err == nil {
		msg := sdk.Message{
			Role:    sdk.MessageRole(mm.Role),
			Content: []sdk.MessagePart{sdk.TextPart{Text: s}},
		}
		return restoreLegacyFields(mm, msg)
	}

	envelope, _ := json.Marshal(struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}{
		Role:    mm.Role,
		Content: mm.Content,
	})
	var msg sdk.Message
	if err := json.Unmarshal(envelope, &msg); err == nil {
		return restoreLegacyFields(mm, msg)
	}

	return restoreLegacyFields(mm, sdk.Message{Role: sdk.MessageRole(mm.Role)})
}

// restoreLegacyFields upgrades the OpenAI-style envelope fields that existed
// in older history rows into Twilight message parts. New rows already carry
// these values in Content, so matching parts are left untouched.
func restoreLegacyFields(mm turn.ModelMessage, msg sdk.Message) sdk.Message {
	if strings.EqualFold(strings.TrimSpace(mm.Role), "assistant") && len(mm.ToolCalls) > 0 {
		if len(msg.Content) == 1 {
			if text, ok := msg.Content[0].(sdk.TextPart); ok && strings.TrimSpace(text.Text) == "" {
				msg.Content = nil
			}
		}
		for _, call := range mm.ToolCalls {
			if hasToolCallPart(msg.Content, call.ID) {
				continue
			}
			msg.Content = append(msg.Content, sdk.ToolCallPart{
				ToolCallID: call.ID,
				ToolName:   call.Function.Name,
				Input:      decodeLegacyJSON(call.Function.Arguments),
			})
		}
	}

	if strings.EqualFold(strings.TrimSpace(mm.Role), "tool") && strings.TrimSpace(mm.ToolCallID) != "" && !hasAnyToolResultPart(msg.Content) {
		msg.Content = []sdk.MessagePart{sdk.ToolResultPart{
			ToolCallID: mm.ToolCallID,
			ToolName:   mm.Name,
			Result:     decodeLegacyRaw(mm.Content),
		}}
	}

	return msg
}

func hasToolCallPart(parts []sdk.MessagePart, id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	for _, part := range parts {
		call, ok := part.(sdk.ToolCallPart)
		if ok && strings.TrimSpace(call.ToolCallID) == id {
			return true
		}
	}
	return false
}

func hasAnyToolResultPart(parts []sdk.MessagePart) bool {
	for _, part := range parts {
		if _, ok := part.(sdk.ToolResultPart); ok {
			return true
		}
	}
	return false
}

func decodeLegacyJSON(raw string) any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}
	}
	var value any
	if json.Unmarshal([]byte(trimmed), &value) == nil {
		return value
	}
	return trimmed
}

func decodeLegacyRaw(raw json.RawMessage) any {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil
	}
	var value any
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	return trimmed
}

func ModelMessagesToSDKMessages(msgs []turn.ModelMessage) []sdk.Message {
	result := make([]sdk.Message, 0, len(msgs))
	for _, mm := range msgs {
		result = append(result, ModelMessageToSDKMessage(mm))
	}
	return result
}

func PrependUserMessage(query string, output []turn.ModelMessage) []turn.ModelMessage {
	if strings.TrimSpace(query) == "" {
		return output
	}
	round := make([]turn.ModelMessage, 0, 1+len(output))
	round = append(round, turn.ModelMessage{
		Role:    "user",
		Content: turn.NewTextContent(query),
	})
	return append(round, output...)
}
