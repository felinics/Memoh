package messageconv

import (
	"encoding/json"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/turn"
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

func TestModelMessageToSDKMessageDoesNotInterpretLegacyEnvelopeFields(t *testing.T) {
	t.Parallel()

	got := ModelMessageToSDKMessage(turn.ModelMessage{
		Role:    "assistant",
		Content: json.RawMessage(`""`),
		ToolCalls: []turn.ToolCall{{
			ID: "legacy-call", Function: turn.ToolCallFunction{Name: "lookup", Arguments: `{"q":"memoh"}`},
		}},
	})
	if len(got.Content) != 1 {
		t.Fatalf("content parts = %d, want only the encoded content: %#v", len(got.Content), got.Content)
	}
	if _, ok := got.Content[0].(sdk.TextPart); !ok {
		t.Fatalf("content part = %T, want TextPart", got.Content[0])
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
