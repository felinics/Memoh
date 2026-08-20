package messageconv

import (
	"encoding/json"
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/agent/turn"
)

func TestModelMessageToSDKMessageText(t *testing.T) {
	t.Parallel()

	got := ModelMessageToSDKMessage(turn.ModelMessage{
		Role:    "user",
		Content: turn.NewTextContent("hello"),
	})

	assertSameJSON(t, got, sdk.UserMessage("hello"))
}

func TestModelMessageToSDKMessageStructuredParts(t *testing.T) {
	t.Parallel()

	got := ModelMessageToSDKMessage(turn.ModelMessage{
		Role: "assistant",
		Content: mustJSON(t, []map[string]any{
			{"type": "text", "text": "checking"},
			{"type": "tool-call", "toolCallId": "call-1", "toolName": "lookup", "input": map[string]any{"q": "memoh"}},
		}),
	})

	assertSameJSON(t, got, sdk.Message{
		Role: sdk.MessageRoleAssistant,
		Content: []sdk.MessagePart{
			sdk.TextPart{Text: "checking"},
			sdk.ToolCallPart{ToolCallID: "call-1", ToolName: "lookup", Input: map[string]any{"q": "memoh"}},
		},
	})
}

func TestSDKMessagesToModelMessagesPreservesUsage(t *testing.T) {
	t.Parallel()

	got := SDKMessagesToModelMessages([]sdk.Message{{
		Role:    sdk.MessageRoleAssistant,
		Content: []sdk.MessagePart{sdk.TextPart{Text: "hi"}},
		Usage:   &sdk.Usage{InputTokens: 3, OutputTokens: 4, TotalTokens: 7},
	}})

	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1", len(got))
	}
	if got[0].Role != "assistant" {
		t.Fatalf("role = %q, want assistant", got[0].Role)
	}
	assertSameJSON(t, got[0].Content, json.RawMessage(`"hi"`))
	var usage sdk.Usage
	if err := json.Unmarshal(got[0].Usage, &usage); err != nil {
		t.Fatalf("unmarshal usage: %v", err)
	}
	if usage.InputTokens != 3 || usage.OutputTokens != 4 || usage.TotalTokens != 7 {
		t.Fatalf("usage = %#v, want input/output/total 3/4/7", usage)
	}
}

func TestSDKMessagesToModelMessagesPreservesMultipartContent(t *testing.T) {
	t.Parallel()

	want := sdk.Message{
		Role: sdk.MessageRoleAssistant,
		Content: []sdk.MessagePart{
			sdk.ReasoningPart{
				Text:             "thinking",
				ProviderMetadata: map[string]any{"provider": map[string]any{"signature": "sig"}},
			},
			sdk.TextPart{Text: "answer"},
			sdk.ImagePart{Image: "data:image/png;base64,abc", MediaType: "image/png"},
			sdk.FilePart{Data: "JVBERi0=", MediaType: "application/pdf", Filename: "report.pdf"},
			sdk.ToolCallPart{
				ToolCallID: "call-1",
				ToolName:   "lookup",
				Input:      map[string]any{"query": "memoh"},
			},
		},
	}

	model := SDKMessagesToModelMessages([]sdk.Message{want})
	if len(model) != 1 {
		t.Fatalf("got %d model messages, want 1", len(model))
	}
	got := ModelMessageToSDKMessage(model[0])
	assertSameJSON(t, got, want)
}

func TestModelMessageToSDKMessageKeepsLegacyEnvelopeFieldsOutOfSDKMessage(t *testing.T) {
	t.Parallel()

	model := turn.ModelMessage{
		Role:       "tool",
		Content:    mustJSON(t, []map[string]any{{"type": "tool-result", "toolCallId": "call-1", "toolName": "lookup", "result": "ok"}}),
		Usage:      mustJSON(t, map[string]int{"inputTokens": 9}),
		ToolCallID: "legacy-call-id",
		Name:       "legacy-tool",
		ToolCalls: []turn.ToolCall{{
			ID:   "legacy-call",
			Type: "function",
			Function: turn.ToolCallFunction{
				Name:      "legacy-tool",
				Arguments: `{"query":"memoh"}`,
			},
		}},
	}

	got := ModelMessageToSDKMessage(model)
	want := sdk.Message{
		Role: sdk.MessageRoleTool,
		Content: []sdk.MessagePart{sdk.ToolResultPart{
			ToolCallID: "call-1",
			ToolName:   "lookup",
			Result:     "ok",
		}},
	}
	assertSameJSON(t, got, want)
	if got.Usage != nil {
		t.Fatalf("usage = %#v, want nil: ModelMessage usage is not written into SDK messages", got.Usage)
	}
}

func TestModelMessageToSDKMessageInvalidLegacyContentKeepsRole(t *testing.T) {
	t.Parallel()

	got := ModelMessageToSDKMessage(turn.ModelMessage{
		Role:    "tool",
		Content: json.RawMessage(`{"not":"a valid sdk content shape"}`),
	})

	if got.Role != sdk.MessageRoleTool {
		t.Fatalf("role = %q, want tool", got.Role)
	}
	if len(got.Content) != 0 {
		t.Fatalf("content = %#v, want empty invalid fallback", got.Content)
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %T: %v", value, err)
	}
	return raw
}

func assertSameJSON(t *testing.T, got any, want any) {
	t.Helper()
	gotRaw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}
	wantRaw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	if string(gotRaw) != string(wantRaw) {
		t.Fatalf("json mismatch:\ngot  %s\nwant %s", gotRaw, wantRaw)
	}
}
