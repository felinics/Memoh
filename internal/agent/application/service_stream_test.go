package application

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/apperror"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	memprovider "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/settings"
)

func TestAgentStreamEventErrorConversion(t *testing.T) {
	t.Parallel()

	t.Run("non-error event", func(t *testing.T) {
		if err := agentStreamEventError(native.StreamEvent{Type: native.EventTextDelta}); err != nil {
			t.Fatalf("agentStreamEventError() = %v, want nil", err)
		}
	})
	t.Run("stable application code", func(t *testing.T) {
		event := native.StreamEvent{
			Type: native.EventError, Code: string(apperror.CodeContextBudgetUnsatisfied),
			Error: "untrusted backend fallback",
		}
		err := agentStreamEventError(event)
		if got := apperror.CodeOf(err); got != apperror.CodeContextBudgetUnsatisfied {
			t.Fatalf("error code = %q, want %q", got, apperror.CodeContextBudgetUnsatisfied)
		}
		if err.Error() != string(apperror.CodeContextBudgetUnsatisfied) {
			t.Fatalf("coded stream identity = %q", err)
		}
	})
	t.Run("legacy detail", func(t *testing.T) {
		err := agentStreamEventError(native.StreamEvent{Type: native.EventError, Error: " provider stopped "})
		if err == nil || err.Error() != "provider stopped" || apperror.CodeOf(err) != "" {
			t.Fatalf("agentStreamEventError() = %v", err)
		}
		lifecycleErr := agentStreamLifecycleError(native.StreamEvent{Type: native.EventError, Error: " provider stopped "})
		if apperror.CodeOf(lifecycleErr) != apperror.CodeAgentResponseInterrupted {
			t.Fatalf("lifecycle code = %q", apperror.CodeOf(lifecycleErr))
		}
		if cause := apperror.CauseOf(lifecycleErr); cause == nil || cause.Error() != "provider stopped" {
			t.Fatalf("private diagnostic cause = %v", cause)
		}
	})
	t.Run("empty legacy detail", func(t *testing.T) {
		err := agentStreamEventError(native.StreamEvent{Type: native.EventError})
		if err == nil || err.Error() != "agent stream failed" || apperror.CodeOf(err) != "" {
			t.Fatalf("agentStreamEventError() = %v", err)
		}
	})
}

func TestPublicAgentStreamEventRedactsPrivateFailure(t *testing.T) {
	event := publicAgentStreamEvent(native.StreamEvent{
		Type: native.EventError, Error: "SECRET provider payload",
	})
	if event.Code != string(apperror.CodeAgentResponseInterrupted) {
		t.Fatalf("public code = %q", event.Code)
	}
	if strings.Contains(event.Error, "SECRET") {
		t.Fatalf("private detail leaked: %q", event.Error)
	}
}

func TestAgentFailureStreamEventExposesOnlyStablePublicContract(t *testing.T) {
	t.Parallel()

	t.Run("timeout", func(t *testing.T) {
		cause := apperror.Wrap(apperror.CodeAgentResponseTimeout, errors.New("SECRET provider timeout"), nil)
		event := agentFailureStreamEvent(cause)
		if event.Type != native.EventError || event.Code != string(apperror.CodeAgentResponseTimeout) {
			t.Fatalf("timeout event = %#v", event)
		}
		definition, _ := apperror.Lookup(apperror.CodeAgentResponseTimeout)
		if event.Error != definition.Detail {
			t.Fatalf("timeout detail = %q, want %q", event.Error, definition.Detail)
		}
		if strings.Contains(event.Error, "SECRET") {
			t.Fatalf("private timeout cause leaked: %q", event.Error)
		}
	})

	t.Run("uncatalogued failure", func(t *testing.T) {
		event := agentFailureStreamEvent(errors.New("SECRET provider response"))
		if event.Code != string(apperror.CodeAgentResponseInterrupted) {
			t.Fatalf("fallback code = %q", event.Code)
		}
		if strings.Contains(event.Error, "SECRET") {
			t.Fatalf("private failure cause leaked: %q", event.Error)
		}
	})
}

type recordingMessageService struct {
	persisted               []messagepkg.PersistInput
	persistErr              error
	roundPersistErr         error
	roundOptions            []messagepkg.RoundPersistenceOptions
	replaced                int
	replacementTurnID       string
	replacementTurnPosition *int64
	deleted                 [][]string
}

func (s *recordingMessageService) Persist(_ context.Context, input messagepkg.PersistInput) (messagepkg.Message, error) {
	if s.persistErr != nil {
		return messagepkg.Message{}, s.persistErr
	}
	s.persisted = append(s.persisted, input)
	return messagepkg.Message{ID: "message-id", SessionID: input.SessionID, Role: input.Role, Content: input.Content, DisplayContent: input.DisplayText}, nil
}

func (s *recordingMessageService) PersistRound(_ context.Context, inputs []messagepkg.PersistInput, options messagepkg.RoundPersistenceOptions) ([]messagepkg.Message, bool, error) {
	s.roundOptions = append(s.roundOptions, options)
	if s.roundPersistErr != nil {
		return nil, true, s.roundPersistErr
	}
	if s.persistErr != nil {
		return nil, true, s.persistErr
	}
	persisted := make([]messagepkg.Message, 0, len(inputs))
	for _, input := range inputs {
		s.persisted = append(s.persisted, input)
		persisted = append(persisted, messagepkg.Message{
			ID:             "message-id",
			SessionID:      input.SessionID,
			Role:           input.Role,
			Content:        input.Content,
			DisplayContent: input.DisplayText,
		})
	}
	return persisted, true, nil
}

func (*recordingMessageService) List(context.Context, string) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*recordingMessageService) ListSince(context.Context, string, time.Time) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*recordingMessageService) ListActiveSince(context.Context, string, time.Time) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*recordingMessageService) ListLatest(context.Context, string, int32) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*recordingMessageService) ListBefore(context.Context, string, time.Time, int32) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*recordingMessageService) ListBySession(context.Context, string) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*recordingMessageService) ListSinceBySession(context.Context, string, time.Time) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*recordingMessageService) ListActiveSinceBySession(context.Context, string, time.Time) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*recordingMessageService) ListActiveSinceBySessionWithinBytes(context.Context, string, time.Time, int64) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*recordingMessageService) ListActiveSinceWithinBytes(context.Context, string, time.Time, int64) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*recordingMessageService) MeasureActiveBySession(context.Context, string, time.Time) (messagepkg.ActiveMessagesMeasure, error) {
	return messagepkg.ActiveMessagesMeasure{}, nil
}

func (*recordingMessageService) ListLatestBySession(context.Context, string, int32) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*recordingMessageService) ListBeforeBySession(context.Context, string, time.Time, int32) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*recordingMessageService) ListBeforeMessageBySession(context.Context, string, string, int32) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*recordingMessageService) LocateByExternalIDBySession(context.Context, string, string, int32, int32) (messagepkg.LocateResult, error) {
	return messagepkg.LocateResult{}, nil
}

func (s *recordingMessageService) GetByIDBySession(_ context.Context, sessionID, messageID string) (messagepkg.Message, error) {
	for _, input := range s.persisted {
		if input.SessionID == sessionID && input.Role == "user" {
			return messagepkg.Message{
				ID: messageID, SessionID: sessionID, Role: input.Role,
				Content: input.Content, DisplayContent: input.DisplayText,
			}, nil
		}
	}
	return messagepkg.Message{}, errors.New("message not found")
}

func (*recordingMessageService) ListVisibleFromBySession(context.Context, string, string) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*recordingMessageService) GetVisibleTurnByMessage(context.Context, string, string) (messagepkg.HistoryTurn, error) {
	return messagepkg.HistoryTurn{}, nil
}

func (*recordingMessageService) GetLatestVisibleTurnBySession(context.Context, string) (messagepkg.HistoryTurn, error) {
	return messagepkg.HistoryTurn{}, nil
}

func (s *recordingMessageService) ReplaceTurn(_ context.Context, _, _, replacementTurnID string, replacementTurnPosition *int64, _, _, _ string) (messagepkg.HistoryTurn, error) {
	s.replaced++
	s.replacementTurnID = replacementTurnID
	s.replacementTurnPosition = replacementTurnPosition
	return messagepkg.HistoryTurn{}, nil
}

func (s *recordingMessageService) DeleteByIDs(_ context.Context, ids []string) error {
	s.deleted = append(s.deleted, append([]string(nil), ids...))
	return nil
}

func (*recordingMessageService) DeleteByBot(context.Context, string) error {
	return nil
}

func (*recordingMessageService) DeleteBySession(context.Context, string) error {
	return nil
}

func (*recordingMessageService) LinkAssets(context.Context, string, []messagepkg.AssetRef) error {
	return nil
}

func TestStreamChatWSResultRejectsTurnReplacementForACP(t *testing.T) {
	t.Parallel()

	resolver := &Service{
		sessionService: acpRuntimeSessionServiceForTest("user-1"),
		logger:         slog.New(slog.DiscardHandler),
	}
	preflightCalled := false
	postPersistCalled := false

	_, err := resolver.streamChatWSResultWithHooks(
		context.Background(),
		ChatRequest{BotID: "bot-1", ThreadID: "session-1"},
		make(chan WSStreamEvent, 1),
		make(chan struct{}),
		func(context.Context) error {
			preflightCalled = true
			return nil
		},
		func(context.Context, []messagepkg.Message) error {
			postPersistCalled = true
			return nil
		},
	)
	if got := apperror.CodeOf(err); got != apperror.CodeACPTurnReplacementUnsupported {
		t.Fatalf("error code = %q, want %q", got, apperror.CodeACPTurnReplacementUnsupported)
	}
	if preflightCalled || postPersistCalled {
		t.Fatalf("replacement hooks ran for ACP: preflight=%v postPersist=%v", preflightCalled, postPersistCalled)
	}
}

func TestPersistPartialResultDoesNotStoreUserOnlyFailure(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	resolver := &Service{
		messageService: messages,
		logger:         slog.New(slog.DiscardHandler),
	}

	resolver.persistPartialResult(
		context.Background(),
		ChatRequest{
			BotID:    "bot-1",
			ThreadID: "session-1",
			Query:    "hello",
		},
		resolvedContext{},
		nil,
		nil,
		0,
		false,
		true,
		"",
	)

	if len(messages.persisted) != 0 {
		t.Fatalf("expected failed stream not to persist user-only history, got %#v", messages.persisted)
	}
}

func TestPersistTerminalSnapshotSkipsUserOnlySnapshot(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	resolver := &Service{
		messageService: messages,
		logger:         slog.New(slog.DiscardHandler),
	}

	if err := resolver.persistTerminalSnapshot(
		context.Background(),
		ChatRequest{
			BotID:    "bot-1",
			ThreadID: "session-1",
			Query:    "hello",
		},
		resolvedContext{},
		terminalSnapshot{
			sdkMessages: []sdk.Message{sdk.UserMessage("hello")},
		},
	); err != nil {
		t.Fatalf("persistTerminalSnapshot returned error: %v", err)
	}

	if len(messages.persisted) != 0 {
		t.Fatalf("expected user-only terminal snapshot not to persist, got %#v", messages.persisted)
	}
}

func TestPersistTerminalSnapshotSkipsEmptyAssistantSnapshot(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	resolver := &Service{
		messageService: messages,
		logger:         slog.New(slog.DiscardHandler),
	}

	if err := resolver.persistTerminalSnapshot(
		context.Background(),
		ChatRequest{
			BotID:    "bot-1",
			ThreadID: "session-1",
			Query:    "hello",
		},
		resolvedContext{},
		terminalSnapshot{
			sdkMessages: []sdk.Message{sdk.AssistantMessage("")},
		},
	); err != nil {
		t.Fatalf("persistTerminalSnapshot returned error: %v", err)
	}

	if len(messages.persisted) != 0 {
		t.Fatalf("expected empty assistant terminal snapshot not to persist, got %#v", messages.persisted)
	}
}

func TestPersistTerminalSnapshotSkipsAbortedSnapshotBeforeVisibleOutput(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	resolver := &Service{
		messageService: messages,
		logger:         slog.New(slog.DiscardHandler),
	}

	if err := resolver.persistTerminalSnapshot(
		context.Background(),
		ChatRequest{
			BotID:    "bot-1",
			ThreadID: "session-1",
			Query:    "hello",
		},
		resolvedContext{},
		terminalSnapshot{
			sdkMessages: []sdk.Message{sdk.AssistantMessage("partial answer")},
			aborted:     true,
		},
	); err != nil {
		t.Fatalf("persistTerminalSnapshot returned error: %v", err)
	}

	if len(messages.persisted) != 0 {
		t.Fatalf("expected pre-output abort not to persist, got %#v", messages.persisted)
	}
}

func TestPersistTerminalSnapshotStoresTimeoutBeforeVisibleOutput(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	resolver := &Service{
		messageService: messages,
		logger:         slog.New(slog.DiscardHandler),
	}

	if err := resolver.persistTerminalSnapshot(
		context.Background(),
		ChatRequest{
			BotID:    "bot-1",
			ThreadID: "session-1",
			Query:    "hello",
		},
		resolvedContext{},
		terminalSnapshot{
			aborted:     true,
			failureCode: apperror.CodeAgentResponseTimeout,
		},
	); err != nil {
		t.Fatalf("persistTerminalSnapshot returned error: %v", err)
	}

	if len(messages.persisted) != 2 {
		t.Fatalf("expected user + timeout assistant, got %#v", messages.persisted)
	}
	if messages.persisted[0].Role != "user" || messages.persisted[1].Role != "assistant" {
		t.Fatalf("persisted roles = %s/%s", messages.persisted[0].Role, messages.persisted[1].Role)
	}
	if got, _ := messages.persisted[1].Metadata[messagepkg.HistoryErrorCodeMetadataKey].(string); got != string(apperror.CodeAgentResponseTimeout) {
		t.Fatalf("error_code = %q, want %q", got, apperror.CodeAgentResponseTimeout)
	}
	if messages.persisted[1].Metadata[messagepkg.AgentStepInterruptedMetadataKey] != true {
		t.Fatalf("expected interrupted metadata on timeout assistant")
	}
}

func TestPersistTurnFailureSkipsRetryReplacement(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	resolver := &Service{
		messageService: messages,
		logger:         slog.New(slog.DiscardHandler),
	}

	persisted, err := resolver.persistTurnFailure(
		context.Background(),
		ChatRequest{
			BotID:           "bot-1",
			ThreadID:        "session-1",
			Query:           "hello",
			SkipHistoryTurn: true,
		},
		resolvedContext{},
		apperror.CodeAgentResponseTimeout,
	)
	if err != nil {
		t.Fatalf("persistTurnFailure returned error: %v", err)
	}
	if persisted != nil || len(messages.persisted) != 0 {
		t.Fatalf("expected retry replacement not to persist a failure turn, got %#v", messages.persisted)
	}
}

func TestPersistPartialResultStoresTimeoutWithoutSnapshot(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	resolver := &Service{
		messageService: messages,
		logger:         slog.New(slog.DiscardHandler),
	}

	persisted := resolver.persistPartialResult(
		context.Background(),
		ChatRequest{
			BotID:    "bot-1",
			ThreadID: "session-1",
			Query:    "hello",
		},
		resolvedContext{},
		nil,
		nil,
		0,
		true,
		false,
		apperror.CodeAgentResponseTimeout,
	)
	if len(persisted) == 0 || len(messages.persisted) != 2 {
		t.Fatalf("expected timeout without snapshot to persist a turn failure, got %#v", messages.persisted)
	}
}

func TestPersistTerminalSnapshotStoresAssistantOutput(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	resolver := &Service{
		messageService: messages,
		logger:         slog.New(slog.DiscardHandler),
	}

	if err := resolver.persistTerminalSnapshot(
		context.Background(),
		ChatRequest{
			BotID:    "bot-1",
			ThreadID: "session-1",
			Query:    "hello",
		},
		resolvedContext{},
		terminalSnapshot{
			sdkMessages: []sdk.Message{sdk.AssistantMessage("partial answer")},
		},
	); err != nil {
		t.Fatalf("persistTerminalSnapshot returned error: %v", err)
	}

	if len(messages.persisted) != 2 {
		t.Fatalf("expected user and assistant messages to persist, got %#v", messages.persisted)
	}
	if messages.persisted[0].Role != "user" || messages.persisted[1].Role != "assistant" {
		t.Fatalf("unexpected persisted roles: %q, %q", messages.persisted[0].Role, messages.persisted[1].Role)
	}
}

func TestHasVisibleAgentStreamOutputIgnoresLifecycleOnlyEvents(t *testing.T) {
	t.Parallel()

	cases := []native.StreamEvent{
		{Type: native.EventTextStart},
		{Type: native.EventTextDelta, Delta: "  \n\t"},
		{Type: native.EventTextEnd},
		{Type: native.EventReasoningStart},
		{Type: native.EventReasoningDelta, Delta: ""},
		{Type: native.EventReasoningEnd},
		{Type: native.EventAttachment},
		{Type: native.EventAgentAbort},
	}
	for _, event := range cases {
		if hasVisibleAgentStreamOutput(event) {
			t.Fatalf("event %q unexpectedly counted as visible", event.Type)
		}
	}

	visible := []native.StreamEvent{
		{Type: native.EventTextDelta, Delta: "hello"},
		{Type: native.EventReasoningDelta, Delta: "thinking"},
		{Type: native.EventAttachment, Attachments: []native.FileAttachment{{Path: "/tmp/a.png"}}},
		{Type: native.EventToolCallStart},
		{Type: native.EventUserInputRequest},
	}
	for _, event := range visible {
		if !hasVisibleAgentStreamOutput(event) {
			t.Fatalf("event %q unexpectedly counted as invisible", event.Type)
		}
	}
}

func TestPersistTerminalSnapshotPersistsUserWhenPipelineContextContainsCurrentMessage(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	resolver := &Service{
		messageService: messages,
		logger:         slog.New(slog.DiscardHandler),
	}

	if err := resolver.persistTerminalSnapshot(
		context.Background(),
		ChatRequest{
			BotID:    "bot-1",
			ThreadID: "session-1",
			Query:    "---\nmessage-id: tg-1\nchannel: telegram\n---\n@memoh1bot ping",
		},
		resolvedContext{
			userMessageAlreadyInContext: true,
		},
		terminalSnapshot{
			sdkMessages: []sdk.Message{sdk.AssistantMessage("pong")},
		},
	); err != nil {
		t.Fatalf("persistTerminalSnapshot returned error: %v", err)
	}

	if len(messages.persisted) != 2 {
		t.Fatalf("expected user and assistant output to persist, got %#v", messages.persisted)
	}
	if messages.persisted[0].Role != "user" {
		t.Fatalf("unexpected first persisted role: %q", messages.persisted[0].Role)
	}
	if messages.persisted[1].Role != "assistant" {
		t.Fatalf("unexpected second persisted role: %q", messages.persisted[1].Role)
	}
}

func TestPersistTerminalSnapshotHonorsSkipMemoryExtraction(t *testing.T) {
	t.Parallel()

	memory := &storeRoundMemoryProvider{afterChat: make(chan memprovider.AfterChatRequest, 2)}
	registry := memprovider.NewRegistry(slog.New(slog.DiscardHandler))
	registry.Register(storeRoundMemoryProviderID, memory)
	resolver := &Service{
		messageService:  &recordingMessageService{},
		memoryRegistry:  registry,
		settingsService: settings.NewService(slog.New(slog.DiscardHandler), &storeRoundSettingsQueries{}, nil, nil),
		logger:          slog.New(slog.DiscardHandler),
	}

	req := ChatRequest{
		BotID:    storeRoundBotID,
		ThreadID: "session-1",
		Query:    "hello",
	}
	if err := resolver.persistTerminalSnapshot(
		context.Background(),
		req,
		resolvedContext{},
		terminalSnapshot{
			sdkMessages:   []sdk.Message{sdk.AssistantMessage("pong")},
			visibleOutput: true,
		},
	); err != nil {
		t.Fatalf("persistTerminalSnapshot returned error: %v", err)
	}
	select {
	case <-memory.afterChat:
	case <-time.After(time.Second):
		t.Fatal("expected ordinary terminal snapshot to write memory")
	}

	req.SkipMemoryExtraction = true
	if err := resolver.persistTerminalSnapshot(
		context.Background(),
		req,
		resolvedContext{},
		terminalSnapshot{
			sdkMessages:   []sdk.Message{sdk.AssistantMessage("done")},
			visibleOutput: true,
		},
	); err != nil {
		t.Fatalf("persistTerminalSnapshot returned error with skip memory: %v", err)
	}
	select {
	case got := <-memory.afterChat:
		t.Fatalf("expected skip memory extraction to suppress memory write, got %#v", got)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestPersistTerminalSnapshotSkillActivationWithoutPromptDoesNotStoreModelMarker(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	resolver := &Service{
		messageService: messages,
		logger:         slog.New(slog.DiscardHandler),
	}
	activation := &SkillActivation{
		Skills: []SkillActivationSkill{{Name: "alpha", DisplayName: "Alpha", State: "effective"}},
	}
	req := ChatRequest{
		BotID:                "bot-1",
		ThreadID:             "session-1",
		ModelQuery:           skillActivationModelQuery(activation),
		UserMessageKind:      UserMessageKindSkillActivation,
		SkillActivation:      activation,
		SkipMemoryExtraction: true,
	}

	if err := resolver.persistTerminalSnapshot(
		context.Background(),
		req,
		resolvedContext{},
		terminalSnapshot{
			sdkMessages:   []sdk.Message{sdk.AssistantMessage("done")},
			visibleOutput: true,
		},
	); err != nil {
		t.Fatalf("persistTerminalSnapshot returned error: %v", err)
	}

	if len(messages.persisted) != 2 {
		t.Fatalf("persisted messages = %d, want user + assistant", len(messages.persisted))
	}
	user := messages.persisted[0]
	if user.Role != "user" {
		t.Fatalf("first persisted role = %q, want user", user.Role)
	}
	if got := persistedTextContent(t, user.Content); got != "" {
		t.Fatalf("persisted user content = %q, want empty", got)
	}
	if user.DisplayText != "" {
		t.Fatalf("display text = %q, want empty", user.DisplayText)
	}
	if user.Metadata["user_message_kind"] != UserMessageKindSkillActivation {
		t.Fatalf("metadata kind = %#v, want skill_activation", user.Metadata["user_message_kind"])
	}
}

func TestReplacePersistedTurnErrorsWhenNoReplacementPersisted(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	resolver := &Service{
		messageService: messages,
		logger:         slog.New(slog.DiscardHandler),
	}

	err := resolver.replacePersistedTurn(
		context.Background(),
		ChatRequest{ThreadID: "session-1"},
		"turn-1",
		"request-1",
		"retry",
		nil,
	)
	if err == nil {
		t.Fatal("expected replacement error, got nil")
	}
	if messages.replaced != 0 {
		t.Fatalf("ReplaceTurn called %d times, want 0", messages.replaced)
	}
}
