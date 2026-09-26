package toolexec

import (
	"encoding/json"

	sdk "github.com/felinics/twilight/sdk"
)

// BuildStepMessages assembles the messages produced by one agent step: an
// assistant message carrying reasoning parts, text, and tool calls (with usage
// attached for output tracking), followed by a tool message when results are
// present.
//
// Reasoning blocks lead the assistant message, one part each, in provider
// emission order and are never filtered on empty text: a redacted thinking
// block carries its payload entirely in metadata.
func BuildStepMessages(text string, textMeta sdk.ProviderMetadata, reasoningParts []sdk.ReasoningPart, toolCalls []sdk.ToolCall, toolResults []sdk.ToolResultPart, usage *sdk.Usage) []sdk.Message {
	var assistantParts []sdk.MessagePart
	for i := range reasoningParts {
		assistantParts = append(assistantParts, reasoningParts[i])
	}
	if text != "" {
		assistantParts = append(assistantParts, sdk.TextPart{Text: text, ProviderMetadata: textMeta})
	}
	for _, tc := range toolCalls {
		assistantParts = append(assistantParts, sdk.ToolCallPart{
			ToolCallID:       tc.ToolCallID,
			ToolName:         tc.ToolName,
			Input:            replayArguments(tc.Input),
			ProviderMetadata: tc.ProviderMetadata,
		})
	}

	msgs := []sdk.Message{{Role: sdk.MessageRoleAssistant, Content: assistantParts, Usage: usage}}
	if len(toolResults) > 0 {
		msgs = append(msgs, sdk.ToolMessage(toolResults...))
	}
	return msgs
}

// replayArguments is the argument document a persisted tool call carries.
// Arguments that were not a JSON document are replayed as the empty object:
// the OpenAI-family providers put the argument text on the wire verbatim, and
// a backend that parses historical arguments rejects the whole request, every
// round, once such a step is in the history. The text the model sent survives
// in the call's error result.
func replayArguments(input sdk.ToolArguments) sdk.ToolArguments {
	if input.Valid() {
		return input
	}
	return sdk.ToolArguments{JSON: json.RawMessage(`{}`)}
}
