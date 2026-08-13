package application

import (
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"
)

// Providers verify the thinking blocks of the latest assistant message and
// reject a sequence they did not produce, so that turn's reasoning has to
// survive context stripping. Older turns are filtered server-side, and dropping
// them here is what keeps encrypted reasoning from accumulating across a long
// conversation.

func reasoningPart(text, signature string) sdk.ReasoningPart {
	return sdk.ReasoningPart{
		Text:   text,
		Format: sdk.ReasoningFormatAnthropic,
		ProviderMetadata: map[string]any{
			"anthropic": map[string]any{"signature": signature},
		},
	}
}

func signatureOf(t *testing.T, part sdk.MessagePart) string {
	t.Helper()
	rp, ok := part.(sdk.ReasoningPart)
	if !ok {
		return ""
	}
	am, _ := rp.ProviderMetadata["anthropic"].(map[string]any)
	sig, _ := am["signature"].(string)
	return sig
}

func reasoningPartsIn(t *testing.T, msg ModelMessage) []sdk.MessagePart {
	t.Helper()
	var out []sdk.MessagePart
	for _, part := range modelMessageToSDKMessage(msg).Content {
		if _, ok := part.(sdk.ReasoningPart); ok {
			out = append(out, part)
		}
	}
	return out
}

// A conversation where an older assistant turn and the newest one both carry
// reasoning alongside a tool call.
func conversationWithTwoReasoningTurns() []ModelMessage {
	return sdkMessagesToModelMessages([]sdk.Message{
		{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.TextPart{Text: "first"}}},
		{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{
			reasoningPart("old thinking", "SIG_OLD"),
			sdk.TextPart{Text: "old answer"},
			sdk.ToolCallPart{ToolCallID: "c1", ToolName: "read_file", Input: map[string]any{}},
		}},
		{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.TextPart{Text: "second"}}},
		{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{
			reasoningPart("new thinking", "SIG_NEW"),
			sdk.TextPart{Text: "new answer"},
			sdk.ToolCallPart{ToolCallID: "c2", ToolName: "read_file", Input: map[string]any{}},
		}},
	})
}

func TestStripToolMessagesKeepsLatestTurnReasoning(t *testing.T) {
	stripped := stripToolMessages(conversationWithTwoReasoningTurns())

	var assistants []ModelMessage
	for _, m := range stripped {
		if m.Role == string(sdk.MessageRoleAssistant) {
			assistants = append(assistants, m)
		}
	}
	if len(assistants) != 2 {
		t.Fatalf("assistant messages: got %d, want 2", len(assistants))
	}

	if got := reasoningPartsIn(t, assistants[0]); len(got) != 0 {
		t.Errorf("older turn kept %d reasoning part(s); it should be stripped", len(got))
	}

	latest := reasoningPartsIn(t, assistants[1])
	if len(latest) != 1 {
		t.Fatalf("latest turn reasoning parts: got %d, want 1 — its signature is the one the provider verifies", len(latest))
	}
	if sig := signatureOf(t, latest[0]); sig != "SIG_NEW" {
		t.Errorf("latest turn signature: got %q, want SIG_NEW", sig)
	}
}

// A redacted thinking block carries its whole payload in metadata, so an
// empty-text part in the latest turn must not be mistaken for nothing.
func TestStripToolMessagesKeepsLatestTurnEmptyReasoning(t *testing.T) {
	messages := sdkMessagesToModelMessages([]sdk.Message{
		{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.TextPart{Text: "hi"}}},
		{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{
			sdk.ReasoningPart{
				Format: sdk.ReasoningFormatAnthropic,
				ProviderMetadata: map[string]any{
					"anthropic": map[string]any{"redactedData": "BLOB"},
				},
			},
			sdk.TextPart{Text: "answer"},
			sdk.ToolCallPart{ToolCallID: "c1", ToolName: "read_file", Input: map[string]any{}},
		}},
	})

	stripped := stripToolMessages(messages)
	var latest ModelMessage
	for _, m := range stripped {
		if m.Role == string(sdk.MessageRoleAssistant) {
			latest = m
		}
	}

	parts := reasoningPartsIn(t, latest)
	if len(parts) != 1 {
		t.Fatalf("reasoning parts: got %d, want 1 — an empty-text redacted block was dropped", len(parts))
	}
	rp := parts[0].(sdk.ReasoningPart)
	am, _ := rp.ProviderMetadata["anthropic"].(map[string]any)
	if data, _ := am["redactedData"].(string); data != "BLOB" {
		t.Errorf("redactedData: got %q, want BLOB", data)
	}
}

// Several reasoning blocks in the latest turn all belong to the sequence the
// provider checks, so none of them may be dropped or reordered.
func TestStripToolMessagesKeepsEveryBlockOfLatestTurn(t *testing.T) {
	messages := sdkMessagesToModelMessages([]sdk.Message{
		{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.TextPart{Text: "hi"}}},
		{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{
			reasoningPart("first", "SIG_1"),
			reasoningPart("second", "SIG_2"),
			reasoningPart("third", "SIG_3"),
			sdk.ToolCallPart{ToolCallID: "c1", ToolName: "read_file", Input: map[string]any{}},
		}},
	})

	stripped := stripToolMessages(messages)
	var latest ModelMessage
	for _, m := range stripped {
		if m.Role == string(sdk.MessageRoleAssistant) {
			latest = m
		}
	}

	parts := reasoningPartsIn(t, latest)
	if len(parts) != 3 {
		t.Fatalf("reasoning parts: got %d, want 3", len(parts))
	}
	for i, want := range []string{"SIG_1", "SIG_2", "SIG_3"} {
		if sig := signatureOf(t, parts[i]); sig != want {
			t.Errorf("part %d signature: got %q, want %q (order must hold)", i, sig, want)
		}
	}
}

// Stripping must not resurrect reasoning in a turn that has no tool call to
// strip — that path leaves the message untouched either way.
func TestStripToolMessagesLeavesPlainAssistantTurnsAlone(t *testing.T) {
	messages := sdkMessagesToModelMessages([]sdk.Message{
		{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.TextPart{Text: "hi"}}},
		{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{
			reasoningPart("thinking", "SIG"),
			sdk.TextPart{Text: "answer"},
		}},
	})

	stripped := stripToolMessages(messages)
	if len(stripped) != 2 {
		t.Fatalf("messages: got %d, want 2", len(stripped))
	}
	if got := reasoningPartsIn(t, stripped[1]); len(got) != 1 {
		t.Errorf("reasoning parts: got %d, want 1", len(got))
	}
}
