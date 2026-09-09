package tools

import (
	"testing"

	sdk "github.com/felinics/twilight/sdk"
)

func TestMessageSnapshotIsImmutableAndProviderNeutral(t *testing.T) {
	original := []sdk.Message{{
		Role: sdk.MessageRoleAssistant,
		Content: []sdk.MessagePart{
			sdk.TextPart{
				Text:             "answer",
				CacheControl:     &sdk.CacheControl{Type: "ephemeral"},
				ProviderMetadata: map[string]any{"signature": "provider-only"},
			},
			sdk.ToolCallPart{
				ToolCallID:       "call-1",
				ToolName:         "read",
				Input:            map[string]any{"path": "a.txt"},
				ProviderMetadata: map[string]any{"opaque": "value"},
			},
		},
	}}
	snapshot := NewMessageSnapshot(original)
	original[0].Content[0] = sdk.TextPart{Text: "mutated"}

	messages, err := snapshot.Messages()
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	text := messages[0].Content[0].(sdk.TextPart)
	if text.Text != "answer" || text.CacheControl != nil || len(text.ProviderMetadata) != 0 {
		t.Fatalf("unexpected provider-neutral text part: %+v", text)
	}
	call := messages[0].Content[1].(sdk.ToolCallPart)
	if call.ToolCallID != "call-1" || len(call.ProviderMetadata) != 0 {
		t.Fatalf("unexpected provider-neutral tool call: %+v", call)
	}
}

func TestMessageSnapshotRetainsSourcesOnlyForUnchangedMessages(t *testing.T) {
	first := sdk.UserMessage("first")
	second := sdk.AssistantMessage("second")
	snapshot := NewMessageSnapshotWithSources(
		[]sdk.Message{first, second},
		[]string{"message-1", "message-2"},
	)

	secondWithProviderState := second
	secondWithProviderState.Content[0] = sdk.TextPart{
		Text:         "second",
		CacheControl: &sdk.CacheControl{Type: "ephemeral"},
	}
	if err := snapshot.Store([]sdk.Message{
		sdk.SystemMessage("runtime-only"),
		secondWithProviderState,
		sdk.UserMessage("new tool delta"),
	}); err != nil {
		t.Fatalf("Store: %v", err)
	}

	entries, err := snapshot.Entries()
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	if entries[0].SourceMessageID != "" || entries[1].SourceMessageID != "message-2" || entries[2].SourceMessageID != "" {
		t.Fatalf("unexpected retained sources: %+v", entries)
	}

	if err := snapshot.Store([]sdk.Message{sdk.AssistantMessage("second changed")}); err != nil {
		t.Fatalf("Store changed message: %v", err)
	}
	entries, err = snapshot.Entries()
	if err != nil {
		t.Fatalf("Entries after change: %v", err)
	}
	if len(entries) != 1 || entries[0].SourceMessageID != "" {
		t.Fatalf("changed message retained a source: %+v", entries)
	}
}
