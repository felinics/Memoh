package application

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	agentdomain "github.com/memohai/memoh/domains/agent"
)

func TestPersistUserTurnSkillActivationWithoutPromptDoesNotStoreModelMarker(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	resolver := &Service{
		messageService: messages,
		logger:         slog.New(slog.DiscardHandler),
	}
	req := ChatRequest{
		BotID:           "bot-1",
		ThreadID:        "session-1",
		ModelQuery:      "The user activated the following skill for this turn without an additional prompt: alpha.",
		RawQuery:        "",
		UserMessageKind: agentdomain.UserMessageKindSkillActivation,
		SkillActivation: &agentdomain.SkillActivation{
			Skills: []agentdomain.SkillActivationSkill{{Name: "alpha", DisplayName: "Alpha", State: "effective"}},
		},
	}

	if _, err := resolver.persistUserTurn(context.Background(), req); err != nil {
		t.Fatalf("persistUserTurn() error = %v", err)
	}
	if len(messages.persisted) != 1 {
		t.Fatalf("persisted messages = %d, want 1", len(messages.persisted))
	}
	got := persistedTextContent(t, messages.persisted[0].Content)
	if got != "" {
		t.Fatalf("persisted content = %q, want empty prompt only", got)
	}
	if messages.persisted[0].DisplayText != "" {
		t.Fatalf("display text = %q, want empty", messages.persisted[0].DisplayText)
	}
	if messages.persisted[0].Metadata["user_message_kind"] != agentdomain.UserMessageKindSkillActivation {
		t.Fatalf("metadata kind = %#v, want skill_activation", messages.persisted[0].Metadata["user_message_kind"])
	}
}

func TestPersistUserTurnSkillActivationWithPromptStoresPromptOnly(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	resolver := &Service{
		messageService: messages,
		logger:         slog.New(slog.DiscardHandler),
	}
	req := ChatRequest{
		BotID:           "bot-1",
		ThreadID:        "session-1",
		Query:           "Plan the widget implementation",
		RawQuery:        "Plan the widget implementation",
		UserVisibleText: "Plan the widget implementation",
		DisplayName:     " Alice ",
		AvatarURL:       " https://example.com/alice.png ",
		UserMessageKind: agentdomain.UserMessageKindSkillActivation,
		SkillActivation: &agentdomain.SkillActivation{
			Prompt: "Plan the widget implementation",
			Skills: []agentdomain.SkillActivationSkill{{Name: "alpha", DisplayName: "Alpha", State: "effective"}},
		},
	}

	if _, err := resolver.persistUserTurn(context.Background(), req); err != nil {
		t.Fatalf("persistUserTurn() error = %v", err)
	}
	if len(messages.persisted) != 1 {
		t.Fatalf("persisted messages = %d, want 1", len(messages.persisted))
	}
	got := persistedTextContent(t, messages.persisted[0].Content)
	if got != "Plan the widget implementation" {
		t.Fatalf("persisted content = %q, want prompt only", got)
	}
	if messages.persisted[0].DisplayText != "Plan the widget implementation" {
		t.Fatalf("display text = %q, want prompt", messages.persisted[0].DisplayText)
	}
	if messages.persisted[0].SenderDisplayName != "Alice" ||
		messages.persisted[0].SenderAvatarURL != "https://example.com/alice.png" {
		t.Fatalf(
			"sender snapshot = %q/%q",
			messages.persisted[0].SenderDisplayName,
			messages.persisted[0].SenderAvatarURL,
		)
	}
}

func persistedTextContent(t *testing.T, content json.RawMessage) string {
	t.Helper()
	var msg agentdomain.ModelMessage
	if err := json.Unmarshal(content, &msg); err != nil {
		t.Fatalf("unmarshal persisted content: %v", err)
	}
	return msg.TextContent()
}
