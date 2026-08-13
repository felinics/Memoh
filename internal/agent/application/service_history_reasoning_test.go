package application

import (
	"strings"
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

// The newest turn keeps its reasoning even with no tool call to strip.
func TestStripToolMessagesKeepsLatestPlainTurnReasoning(t *testing.T) {
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

// A plain conversational turn carries no tool call, so it never reached the
// tool-stripping path — and its reasoning accumulated forever. Only the newest
// turn's blocks go back now.
func TestStripToolMessagesDropsReasoningFromPlainOlderTurns(t *testing.T) {
	messages := sdkMessagesToModelMessages([]sdk.Message{
		{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.TextPart{Text: "first"}}},
		{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{
			reasoningPart("old thinking", "SIG_OLD"),
			sdk.TextPart{Text: "old answer"},
		}},
		{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.TextPart{Text: "second"}}},
		{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{
			reasoningPart("new thinking", "SIG_NEW"),
			sdk.TextPart{Text: "new answer"},
		}},
	})

	stripped := stripToolMessages(messages)
	if len(stripped) != 4 {
		t.Fatalf("messages: got %d, want 4 — nothing should be removed outright", len(stripped))
	}

	if got := reasoningPartsIn(t, stripped[1]); len(got) != 0 {
		t.Errorf("older plain turn kept %d reasoning part(s); it should be dropped", len(got))
	}
	// The answer text has to stay: only reasoning is dropped.
	if text := strings.TrimSpace(stripped[1].TextContent()); text != "old answer" {
		t.Errorf("older turn text: got %q, want %q", text, "old answer")
	}

	latest := reasoningPartsIn(t, stripped[3])
	if len(latest) != 1 {
		t.Fatalf("latest turn reasoning: got %d, want 1", len(latest))
	}
	if sig := signatureOf(t, latest[0]); sig != "SIG_NEW" {
		t.Errorf("latest turn signature: got %q, want SIG_NEW", sig)
	}
}

// The replayed block count has to stop growing as a conversation goes on. That
// unbounded growth is what pushed requests past the provider timeout.
func TestStripToolMessagesBoundsReplayedReasoning(t *testing.T) {
	var messages []sdk.Message
	for turn := 0; turn < 8; turn++ {
		messages = append(messages,
			sdk.Message{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.TextPart{Text: "q"}}},
			sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{
				// Several redacted blocks per turn, as a real response returns.
				sdk.ReasoningPart{Format: sdk.ReasoningFormatAnthropic, ProviderMetadata: map[string]any{
					"anthropic": map[string]any{"redactedData": "BLOB_A"}}},
				sdk.ReasoningPart{Format: sdk.ReasoningFormatAnthropic, ProviderMetadata: map[string]any{
					"anthropic": map[string]any{"redactedData": "BLOB_B"}}},
				sdk.TextPart{Text: "a"},
			}},
		)
	}

	stripped := stripToolMessages(sdkMessagesToModelMessages(messages))
	total := 0
	for _, m := range stripped {
		total += len(reasoningPartsIn(t, m))
	}
	if total != 2 {
		t.Fatalf("replayed reasoning parts across 8 turns: got %d, want 2 (the newest turn only)", total)
	}
}

// An assistant turn whose only content is reasoning must not be emptied out —
// a contentless assistant message is not something a provider accepts.
func TestStripToolMessagesKeepsReasoningOnlyTurnIntact(t *testing.T) {
	messages := sdkMessagesToModelMessages([]sdk.Message{
		{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.TextPart{Text: "q"}}},
		{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{
			reasoningPart("only thinking", "SIG"),
		}},
		{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.TextPart{Text: "q2"}}},
		{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.TextPart{Text: "answer"}}},
	})

	stripped := stripToolMessages(messages)
	if len(stripped) != 4 {
		t.Fatalf("messages: got %d, want 4", len(stripped))
	}
	if got := reasoningPartsIn(t, stripped[1]); len(got) != 1 {
		t.Errorf("reasoning-only turn: got %d part(s), want it left intact", len(got))
	}
}
