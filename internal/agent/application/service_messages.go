package application

import (
	"encoding/json"
	"strings"

	sdk "github.com/memohai/twilight-ai/sdk"

	turnpkg "github.com/memohai/memoh/internal/agent/turn"
	"github.com/memohai/memoh/internal/messageconv"
)

// sdkMessagesToModelMessages converts SDK messages to the persistence/API format
// for resolver call sites using the shared conversion helper.
func sdkMessagesToModelMessages(msgs []sdk.Message) []ModelMessage {
	return messageconv.SDKMessagesToModelMessages(msgs)
}

// modelMessageToSDKMessage converts a persistence format message to SDK message
// at the resolver boundary using sdk.Message's native JSON deserialization.
func modelMessageToSDKMessage(mm ModelMessage) sdk.Message {
	return messageconv.ModelMessageToSDKMessage(mm)
}

// prependUserMessage prepends the user query as a ModelMessage to the output
// messages from the agent. The SDK only returns output messages (assistant + tool);
// user messages must be added back at the resolver boundary for persistence.
func prependUserMessage(query string, output []ModelMessage) []ModelMessage {
	return messageconv.PrependUserMessage(query, output)
}

func prependTurnUserMessage(req ChatRequest, output []ModelMessage) []ModelMessage {
	if strings.TrimSpace(req.Query) == "" && req.UserMessageKind != UserMessageKindSkillActivation {
		return output
	}
	round := make([]ModelMessage, 0, 1+len(output))
	round = append(round, ModelMessage{
		Role:    "user",
		Content: newTextContent(req.Query),
	})
	return append(round, output...)
}

func modelQueryText(req ChatRequest) string {
	if strings.TrimSpace(req.ModelQuery) != "" {
		return req.ModelQuery
	}
	return req.Query
}

// modelMessagesToSDKMessages converts a slice of persistence messages to SDK messages.
func modelMessagesToSDKMessages(msgs []ModelMessage) []sdk.Message {
	return messageconv.ModelMessagesToSDKMessages(msgs)
}

// projectModelMessageHeaders changes only the transient model projection. The
// canonical messages and their full metadata remain untouched in persistence.
func projectModelMessageHeaders(messages []ModelMessage, mode string) []ModelMessage {
	if mode == "" || mode == "full" || len(messages) == 0 {
		return messages
	}
	out := make([]ModelMessage, len(messages))
	copy(out, messages)
	for i := range out {
		if out[i].Role != "user" || len(out[i].Content) == 0 {
			continue
		}
		var text string
		if err := json.Unmarshal(out[i].Content, &text); err == nil {
			out[i].Content = turnpkg.NewTextContent(turnpkg.ProjectUserMessageHeader(text, mode))
			continue
		}
		var parts []turnpkg.ContentPart
		if err := json.Unmarshal(out[i].Content, &parts); err != nil {
			continue
		}
		changed := false
		for j := range parts {
			projected := turnpkg.ProjectUserMessageHeader(parts[j].Text, mode)
			if projected != parts[j].Text {
				parts[j].Text = projected
				changed = true
			}
		}
		if changed {
			if data, err := json.Marshal(parts); err == nil {
				out[i].Content = data
			}
		}
	}
	return out
}

// projectSDKMessageHeaders is the equivalent model-only projection for paths
// that already own SDK messages, notably the discuss-mode timeline pipeline.
func projectSDKMessageHeaders(messages []sdk.Message, mode string) []sdk.Message {
	if mode == "" || mode == "full" || len(messages) == 0 {
		return messages
	}
	out := make([]sdk.Message, len(messages))
	copy(out, messages)
	for i := range out {
		if out[i].Role != sdk.MessageRoleUser || len(out[i].Content) == 0 {
			continue
		}
		parts := make([]sdk.MessagePart, len(out[i].Content))
		copy(parts, out[i].Content)
		changed := false
		for j, part := range parts {
			switch textPart := part.(type) {
			case sdk.TextPart:
				projected := turnpkg.ProjectUserMessageHeader(textPart.Text, mode)
				if projected != textPart.Text {
					textPart.Text = projected
					parts[j] = textPart
					changed = true
				}
			case *sdk.TextPart:
				if textPart == nil {
					continue
				}
				projected := turnpkg.ProjectUserMessageHeader(textPart.Text, mode)
				if projected != textPart.Text {
					copyPart := *textPart
					copyPart.Text = projected
					parts[j] = &copyPart
					changed = true
				}
			}
		}
		if changed {
			out[i].Content = parts
		}
	}
	return out
}

// projectDiscussToolHistory keeps the canonical transcript untouched while
// presenting a smaller, audience-accurate history to the next discuss turn.
// All ordinary assistant text remains private. Successful
// current-conversation sends are replaced by the text/sticker that was
// actually delivered, and their
// protocol call/result pair is removed.
func projectDiscussToolHistory(messages []sdk.Message) []sdk.Message {
	if len(messages) == 0 {
		return messages
	}
	successful := make(map[string]sdk.ToolResultPart)
	for _, message := range messages {
		if message.Role != sdk.MessageRoleTool {
			continue
		}
		for _, part := range message.Content {
			result, ok := sdkToolResultPart(part)
			if !ok || result.IsError || !toolResultSucceeded(result.Result) {
				continue
			}
			successful[strings.TrimSpace(result.ToolCallID)] = result
		}
	}

	collapsed := make(map[string]struct{})
	out := make([]sdk.Message, 0, len(messages))
	for _, message := range messages {
		projected := message
		projected.Content = make([]sdk.MessagePart, 0, len(message.Content))
		switch message.Role {
		case sdk.MessageRoleAssistant:
			for _, part := range message.Content {
				call, ok := sdkToolCallPart(part)
				if !ok {
					switch part.(type) {
					case sdk.TextPart, *sdk.TextPart, sdk.ReasoningPart, *sdk.ReasoningPart:
						continue
					default:
						projected.Content = append(projected.Content, part)
					}
					continue
				}
				callID := strings.TrimSpace(call.ToolCallID)
				result, ok := successful[callID]
				if !ok {
					projected.Content = append(projected.Content, part)
					continue
				}
				if isStickerSearchTool(call.ToolName) {
					collapsed[callID] = struct{}{}
					continue
				}
				if !isVisibleDiscussSendTool(call.ToolName) ||
					(strings.TrimSpace(call.ToolName) == "send" && !toolResultDeliveredToCurrentConversation(result.Result)) {
					projected.Content = append(projected.Content, part)
					continue
				}
				collapsed[callID] = struct{}{}
				if visible := visibleDiscussSendSummary(call); visible != "" {
					projected.Content = append(projected.Content, sdk.TextPart{Text: visible})
				}
			}
		case sdk.MessageRoleTool:
			for _, part := range message.Content {
				result, ok := sdkToolResultPart(part)
				if ok {
					if _, skip := collapsed[strings.TrimSpace(result.ToolCallID)]; skip {
						continue
					}
				}
				projected.Content = append(projected.Content, part)
			}
		default:
			projected.Content = append(projected.Content, message.Content...)
		}
		if len(projected.Content) > 0 {
			out = append(out, projected)
		}
	}
	return out
}

func sdkToolCallPart(part sdk.MessagePart) (sdk.ToolCallPart, bool) {
	switch value := part.(type) {
	case sdk.ToolCallPart:
		return value, true
	case *sdk.ToolCallPart:
		if value != nil {
			return *value, true
		}
	}
	return sdk.ToolCallPart{}, false
}

func sdkToolResultPart(part sdk.MessagePart) (sdk.ToolResultPart, bool) {
	switch value := part.(type) {
	case sdk.ToolResultPart:
		return value, true
	case *sdk.ToolResultPart:
		if value != nil {
			return *value, true
		}
	}
	return sdk.ToolResultPart{}, false
}

func toolResultSucceeded(result any) bool {
	payload, ok := result.(map[string]any)
	if !ok {
		return true
	}
	if value, present := payload["ok"].(bool); present {
		return value
	}
	if structured, ok := payload["structuredContent"].(map[string]any); ok {
		if value, present := structured["ok"].(bool); present {
			return value
		}
	}
	return true
}

// Current-conversation sends deliberately return only {"ok":true}; sends to
// another route retain their routing fields and delivered=target. Preserve the
// latter protocol closure so a delivery elsewhere is never projected as text
// that the observed Telegram conversation saw.
func toolResultDeliveredToCurrentConversation(result any) bool {
	payload, ok := result.(map[string]any)
	if !ok {
		return true
	}
	if structured, ok := payload["structuredContent"].(map[string]any); ok {
		payload = structured
	}
	if delivered, present := payload["delivered"].(string); present {
		return strings.EqualFold(strings.TrimSpace(delivered), "current_conversation")
	}
	if _, present := payload["platform"]; present {
		return false
	}
	if _, present := payload["target"]; present {
		return false
	}
	return true
}

func isVisibleDiscussSendTool(name string) bool {
	name = strings.TrimSpace(name)
	return name == "send" || name == "send_telegram_sticker" || strings.HasSuffix(name, "_send_telegram_sticker")
}

func isStickerSearchTool(name string) bool {
	name = strings.TrimSpace(name)
	return name == "search_telegram_stickers" || strings.HasSuffix(name, "_search_telegram_stickers")
}

func visibleDiscussSendSummary(call sdk.ToolCallPart) string {
	input := normalizeToolInputMap(call.Input)
	text, _ := input["text"].(string)
	text = strings.TrimSpace(text)
	if text == "" {
		if message, ok := input["message"].(map[string]any); ok {
			text, _ = message["text"].(string)
			text = strings.TrimSpace(text)
		}
	}
	stickerID, _ := input["sticker_id"].(string)
	stickerID = strings.TrimSpace(stickerID)
	if stickerID != "" {
		marker := "[Sent Telegram sticker " + stickerID + "]"
		if text == "" {
			return marker
		}
		return text + "\n" + marker
	}
	if text != "" {
		return text
	}
	if _, ok := input["attachments"]; ok {
		return "[Sent attachment]"
	}
	return "[Sent message]"
}

func normalizeToolInputMap(input any) map[string]any {
	if payload, ok := input.(map[string]any); ok {
		return payload
	}
	data, err := json.Marshal(input)
	if err != nil {
		return map[string]any{}
	}
	var payload map[string]any
	if json.Unmarshal(data, &payload) != nil {
		return map[string]any{}
	}
	return payload
}
