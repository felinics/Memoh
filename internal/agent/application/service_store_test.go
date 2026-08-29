package application

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	turnpkg "github.com/felinics/memoh/internal/agent/turn"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/settings"
)

func TestBuildInteractionMetadataIncludesForwardConversation(t *testing.T) {
	t.Parallel()

	meta := buildInteractionMetadata(ChatRequest{
		SourceReplyToMessageID:    "reply-1",
		ReplySender:               "Original Sender",
		ReplyPreview:              "quoted text",
		ForwardMessageID:          "forward-1",
		ForwardFromUserID:         "source-user",
		ForwardFromConversationID: "source-conversation",
		ForwardSender:             "Source Channel",
		ForwardDate:               1710000000,
	})

	reply, ok := meta["reply"].(map[string]any)
	if !ok || reply["message_id"] != "reply-1" || reply["sender"] != "Original Sender" || reply["preview"] != "quoted text" {
		t.Fatalf("unexpected reply metadata: %#v", meta["reply"])
	}
	forward, ok := meta["forward"].(map[string]any)
	if !ok {
		t.Fatalf("expected forward metadata: %#v", meta)
	}
	if forward["message_id"] != "forward-1" ||
		forward["from_user_id"] != "source-user" ||
		forward["from_conversation_id"] != "source-conversation" ||
		forward["sender"] != "Source Channel" ||
		forward["date"] != int64(1710000000) {
		t.Fatalf("unexpected forward metadata: %#v", forward)
	}
}

func TestBuildInteractionMetadataIncludesRequestedSkills(t *testing.T) {
	t.Parallel()

	meta := buildInteractionMetadata(ChatRequest{
		RequestedSkills: []RequestedSkillContext{
			{
				Name:           "writer",
				SourceKind:     "managed",
				OpaqueSourceID: "src-1",
				Content:        "raw content must not be persisted in metadata",
				ContentHash:    "hash-must-not-leak",
				Identity:       "managed:src-1:writer",
			},
			{
				Name:           "writer",
				SourceKind:     "managed",
				OpaqueSourceID: "src-1",
				Identity:       "managed:src-1:writer",
			},
			{
				Name:       "reviewer",
				SourceKind: "registry",
			},
		},
	})

	raw, ok := meta["model_requested_skills"].([]map[string]any)
	if !ok {
		t.Fatalf("expected requested skill metadata: %#v", meta["model_requested_skills"])
	}
	if len(raw) != 2 {
		t.Fatalf("expected deduped requested skills, got %#v", raw)
	}
	if raw[0]["name"] != "writer" || raw[0]["source_kind"] != "managed" {
		t.Fatalf("unexpected first skill metadata: %#v", raw[0])
	}
	if _, ok := raw[0]["opaque_source_id"]; ok {
		t.Fatalf("requested skill metadata leaked opaque source id: %#v", raw[0])
	}
	if _, ok := raw[0]["content"]; ok {
		t.Fatalf("requested skill metadata leaked content: %#v", raw[0])
	}
	if _, ok := raw[0]["content_hash"]; ok {
		t.Fatalf("requested skill metadata leaked content_hash: %#v", raw[0])
	}
	if _, ok := raw[0]["ref"]; ok {
		t.Fatalf("requested skill metadata leaked ref: %#v", raw[0])
	}
	if raw[1]["name"] != "reviewer" || raw[1]["source_kind"] != "registry" {
		t.Fatalf("unexpected second skill metadata: %#v", raw[1])
	}
}

func TestBuildInteractionMetadataIncludesPublicSkillActivation(t *testing.T) {
	t.Parallel()

	meta := buildInteractionMetadata(ChatRequest{
		UserMessageKind: UserMessageKindSkillActivation,
		SkillActivation: &SkillActivation{
			Prompt: "do it",
			Skills: []SkillActivationSkill{{
				Name:        "writer",
				DisplayName: "Writer",
				Description: "short safe description",
				SourceKind:  "managed",
				State:       "effective",
			}},
		},
		RequestedSkills: []RequestedSkillContext{{
			Name:           "writer",
			SourceKind:     "managed",
			OpaqueSourceID: "opaque-source",
			Content:        "raw content must not leak",
			ContentHash:    "hash-must-not-leak",
			Identity:       "writer|opaque-source|hash",
		}},
	})

	if meta["user_message_kind"] != UserMessageKindSkillActivation {
		t.Fatalf("user_message_kind = %#v", meta["user_message_kind"])
	}
	public, ok := meta["skill_activation"].(map[string]any)
	if !ok {
		t.Fatalf("expected public skill activation metadata: %#v", meta["skill_activation"])
	}
	if public["prompt"] != "do it" {
		t.Fatalf("prompt = %#v, want do it", public["prompt"])
	}
	skills, ok := public["skills"].([]map[string]any)
	if !ok || len(skills) != 1 {
		t.Fatalf("public skills = %#v, want one", public["skills"])
	}
	if skills[0]["name"] != "writer" || skills[0]["display_name"] != "Writer" {
		t.Fatalf("unexpected public skill: %#v", skills[0])
	}
	for _, key := range []string{"opaque_source_id", "content", "content_hash", "ref"} {
		if _, ok := skills[0][key]; ok {
			t.Fatalf("public skill leaked %s: %#v", key, skills[0])
		}
	}
	if _, ok := meta["audit_requested_skills"]; ok {
		t.Fatalf("audit metadata leaked into message metadata: %#v", meta["audit_requested_skills"])
	}
}

type batchRecordingMessageService struct {
	recordingMessageService
	batchInputs []messagepkg.PersistInput
}

type lifecycleRecordingMessageService struct {
	recordingMessageService
	messageIDs []string
}

func (s *lifecycleRecordingMessageService) Persist(_ context.Context, input messagepkg.PersistInput) (messagepkg.Message, error) {
	s.persisted = append(s.persisted, input)
	return messagepkg.Message{
		ID:       s.messageIDs[len(s.persisted)-1],
		Role:     input.Role,
		Metadata: input.Metadata,
	}, nil
}

func (s *batchRecordingMessageService) PersistToolTailRound(_ context.Context, inputs []messagepkg.PersistInput) ([]messagepkg.Message, bool, error) {
	s.batchInputs = append(s.batchInputs, inputs...)
	return recordedMessages(inputs), true, nil
}

func TestStoreMessagesUsesToolTailBatch(t *testing.T) {
	t.Parallel()

	messages := &batchRecordingMessageService{}
	resolver := &Service{
		messageService: messages,
		logger:         slog.New(slog.DiscardHandler),
	}

	persisted := resolver.storeMessages(context.Background(), ChatRequest{
		BotID:       storeRoundBotID,
		ThreadID:    "33333333-3333-3333-3333-333333333333",
		Query:       "hello",
		SessionType: "chat",
		RuntimeType: "model",
	}, []ModelMessage{
		{Role: "user", Content: newTextContent("hello")},
		{Role: "assistant", Content: newTextContent("call tool")},
		{Role: "tool", Content: newTextContent("tool result")},
		{Role: "assistant", Content: newTextContent("done")},
	}, "", storeRoundOptions{})

	if len(messages.batchInputs) != 4 {
		t.Fatalf("batch inputs = %d, want 4", len(messages.batchInputs))
	}
	if len(messages.persisted) != 0 {
		t.Fatalf("fallback Persist called %d times, want 0", len(messages.persisted))
	}
	if len(persisted) != 4 {
		t.Fatalf("persisted messages = %d, want 4", len(persisted))
	}
}

func TestBuildPersistInputsKeepsAttachmentOnlyUserDisplayTextEmpty(t *testing.T) {
	t.Parallel()

	service := &Service{
		settingsService: settings.NewService(slog.New(slog.DiscardHandler), &storeRoundSettingsQueries{}, nil, nil),
		logger:          slog.New(slog.DiscardHandler),
	}
	// resolve() replaces ChatRequest.Query with the headerified envelope before
	// the round is stored; an image with no caption leaves RawQuery empty.
	headerified := turnpkg.FormatUserHeader(turnpkg.UserMessageHeaderInput{
		DisplayName:      "User",
		Channel:          "web",
		ConversationType: "private",
		AttachmentPaths:  []string{"/data/.memoh/media/b2/b2edf40e.png"},
		Time:             time.Date(2026, 8, 20, 17, 37, 14, 0, time.UTC),
	}, "")

	inputs, err := service.buildPersistInputs(context.Background(), ChatRequest{
		BotID:    storeRoundBotID,
		ThreadID: "33333333-3333-3333-3333-333333333333",
		Query:    headerified,
	}, []ModelMessage{
		{Role: "user", Content: newTextContent(headerified)},
		{Role: "assistant", Content: newTextContent("nice picture")},
	}, "", storeRoundOptions{})
	if err != nil {
		t.Fatalf("buildPersistInputs() error = %v", err)
	}
	if len(inputs) != 2 {
		t.Fatalf("persist inputs = %d, want 2", len(inputs))
	}
	if got := inputs[0].DisplayText; got != "" {
		t.Fatalf("attachment-only user display text = %q, want empty", got)
	}
	if !bytes.Contains(inputs[0].Content, []byte("attachment path=")) {
		t.Fatalf("model-facing content lost its envelope: %s", inputs[0].Content)
	}
}

func TestStoreRoundPersistsLifecycleMetadataOnLastAssistant(t *testing.T) {
	t.Parallel()

	holder := contextfrag.NewLifecycleHolder()
	holder.SetManifest(contextfrag.Manifest{
		View: contextfrag.ViewRunConfigPreProvider,
		Counts: contextfrag.ManifestCounts{
			Fragments: 3,
			Messages:  2,
			TextBytes: 96,
		},
		Items: []contextfrag.ManifestItem{{ID: "private-content-marker"}},
	})
	messages := &lifecycleRecordingMessageService{
		messageIDs: []string{"user-id", "first-assistant-id", " final-assistant-id "},
	}
	service := &Service{
		messageService: messages,
		logger:         slog.New(slog.DiscardHandler),
	}

	err := service.storeRoundWithOptions(context.Background(), ChatRequest{
		BotID:    "bot-1",
		ThreadID: "session-1",
		Query:    "hello",
	}, []ModelMessage{
		{Role: "user", Content: newTextContent("hello")},
		{Role: "assistant", Content: newTextContent("first")},
		{Role: "assistant", Content: newTextContent("final")},
	}, "model-1", storeRoundOptions{
		SkipMemory:       true,
		ContextLifecycle: holder,
		MessageMetadataByIndex: map[int]map[string]any{
			2: {"existing": "kept"},
		},
	})
	if err != nil {
		t.Fatalf("storeRoundWithOptions() error = %v", err)
	}
	if len(messages.persisted) != 3 {
		t.Fatalf("persisted messages = %d, want 3", len(messages.persisted))
	}
	if _, ok := messages.persisted[1].Metadata[contextfrag.MetadataContextLifecycleKey]; ok {
		t.Fatalf("first assistant received lifecycle metadata: %#v", messages.persisted[1].Metadata)
	}
	finalMeta := messages.persisted[2].Metadata
	if finalMeta["existing"] != "kept" {
		t.Fatalf("existing metadata was not preserved: %#v", finalMeta)
	}
	metadataSnapshot, ok := finalMeta[contextfrag.MetadataContextLifecycleKey].(contextfrag.LifecycleSnapshot)
	if !ok {
		t.Fatalf("lifecycle metadata = %#v, want LifecycleSnapshot", finalMeta[contextfrag.MetadataContextLifecycleKey])
	}
	if metadataSnapshot.Version != 1 || metadataSnapshot.Counts.Messages != 2 {
		t.Fatalf("lifecycle metadata snapshot = %#v", metadataSnapshot)
	}
	raw, err := json.Marshal(metadataSnapshot)
	if err != nil {
		t.Fatalf("marshal lifecycle metadata: %v", err)
	}
	if strings.Contains(string(raw), "private-content-marker") || strings.Contains(string(raw), `"items"`) {
		t.Fatalf("lifecycle metadata leaked manifest items: %s", raw)
	}

	snapshot, ok := holder.Snapshot()
	if !ok {
		t.Fatal("expected lifecycle snapshot after persistence")
	}
	if snapshot.AssistantMessageID != "final-assistant-id" {
		t.Fatalf("assistant message ID = %q, want final-assistant-id", snapshot.AssistantMessageID)
	}
}

func TestStoreRoundLogsSkippedLifecycleMetadataReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		holder     *contextfrag.LifecycleHolder
		messages   []ModelMessage
		wantReason string
	}{
		{
			name:       "missing lifecycle",
			messages:   []ModelMessage{{Role: "assistant", Content: newTextContent("done")}},
			wantReason: "missing_lifecycle",
		},
		{
			name:       "missing snapshot",
			holder:     contextfrag.NewLifecycleHolder(),
			messages:   []ModelMessage{{Role: "assistant", Content: newTextContent("done")}},
			wantReason: "missing_snapshot",
		},
		{
			name:       "missing assistant",
			holder:     lifecycleHolderWithManifest(),
			messages:   []ModelMessage{{Role: "user", Content: newTextContent("hello")}},
			wantReason: "missing_assistant",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			messages := &recordingMessageService{}
			service := &Service{
				messageService: messages,
				logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{
					Level: slog.LevelDebug,
				})),
			}

			err := service.storeRoundWithOptions(context.Background(), ChatRequest{
				BotID:    "bot-1",
				ThreadID: "session-1",
			}, tt.messages, "model-1", storeRoundOptions{
				SkipMemory:       true,
				ContextLifecycle: tt.holder,
			})
			if err != nil {
				t.Fatalf("storeRoundWithOptions() error = %v", err)
			}
			if got := logs.String(); !strings.Contains(got, "context lifecycle metadata not persisted") || !strings.Contains(got, tt.wantReason) {
				t.Fatalf("debug log = %q, want reason %q", got, tt.wantReason)
			}
		})
	}
}

func lifecycleHolderWithManifest() *contextfrag.LifecycleHolder {
	holder := contextfrag.NewLifecycleHolder()
	holder.SetManifest(contextfrag.Manifest{View: contextfrag.ViewRunConfigPreProvider})
	return holder
}

func TestFindAssistantMessageForToolCall(t *testing.T) {
	t.Parallel()

	msgs := []messagepkg.Message{
		{ID: "m3", Role: "assistant", Content: []byte(`{"role":"assistant","content":[{"type":"text","text":"done, see call-1 above"}]}`)},
		{ID: "m2", Role: "tool", Content: []byte(`{"role":"tool","content":[{"type":"tool-result","toolCallId":"call-1"}]}`)},
		{ID: "m1", Role: "assistant", Content: []byte(`{"role":"assistant","content":[{"type":"tool-call","toolCallId":"call-1","toolName":"generate_image"}]}`)},
	}

	if got := findAssistantMessageForToolCall(msgs, "call-1"); got != "m1" {
		t.Fatalf("findAssistantMessageForToolCall = %q, want m1 (assistant tool-call row, not tool row or echoed text)", got)
	}
	if got := findAssistantMessageForToolCall(msgs, "call-404"); got != "" {
		t.Fatalf("findAssistantMessageForToolCall unknown id = %q, want empty", got)
	}
}
