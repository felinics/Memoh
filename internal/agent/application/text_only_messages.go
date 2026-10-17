package application

import (
	"strings"

	sdk "github.com/felinics/twilight/sdk"
)

// textOnlyMessages rewrites tool calls and tool results as plain text so a
// turn that runs without tools can still replay a history that used them.
// Providers such as the Anthropic Messages API reject tool_use and
// tool_result blocks when the request defines no tools. The number of
// messages is kept, so context indexes and per-message token estimates stay
// aligned; a tool message becomes a user message, which is how providers
// carry tool results anyway. The input is returned as is when nothing has
// to change.
func textOnlyMessages(messages []sdk.Message) []sdk.Message {
	changed := false
	for i := range messages {
		if messages[i].Role == sdk.MessageRoleTool || hasToolParts(messages[i].Content) {
			changed = true
			break
		}
	}
	if !changed {
		return messages
	}
	out := make([]sdk.Message, len(messages))
	for i, msg := range messages {
		out[i] = msg
		if msg.Role == sdk.MessageRoleTool {
			out[i].Role = sdk.MessageRoleUser
		}
		if !hasToolParts(msg.Content) {
			continue
		}
		parts := make([]sdk.MessagePart, 0, len(msg.Content))
		for _, part := range msg.Content {
			switch p := part.(type) {
			case sdk.ToolCallPart:
				parts = append(parts, sdk.TextPart{Text: "[Tool call: " + strings.TrimSpace(p.ToolName) + "]\n" + strings.TrimSpace(p.Input.String())})
			case sdk.ToolResultPart:
				label := "[Tool result: " + strings.TrimSpace(p.ToolName) + "]"
				if p.IsError {
					label = "[Tool error: " + strings.TrimSpace(p.ToolName) + "]"
				}
				parts = append(parts, sdk.TextPart{Text: label + "\n" + strings.TrimSpace(p.Result.String())})
			default:
				parts = append(parts, part)
			}
		}
		out[i].Content = parts
	}
	return out
}

func hasToolParts(parts []sdk.MessagePart) bool {
	for _, part := range parts {
		switch part.(type) {
		case sdk.ToolCallPart, sdk.ToolResultPart:
			return true
		}
	}
	return false
}
