package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	acpagent "github.com/felinics/memoh/internal/agent/runtime/external/acp"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/apperror"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	memprovider "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/settings"
)

func TestAgentStreamFailureConversion(t *testing.T) {
	t.Parallel()

	overloaded := &sdk.APIError{Provider: "anthropic-messages", StatusCode: 529, Kind: sdk.KindServerError, Message: "SECRET overloaded"}
	for _, tc := range []struct {
		name  string
		event native.StreamEvent
		code  apperror.Code
	}{
		{
			name:  "catalogued code",
			event: native.StreamEvent{Type: native.EventError, Code: string(apperror.CodeContextBudgetUnsatisfied), Cause: overloaded},
			code:  apperror.CodeContextBudgetUnsatisfied,
		},
		{
			name:  "uncatalogued code",
			event: native.StreamEvent{Type: native.EventError, Code: "not.a.code", Cause: overloaded},
			code:  apperror.CodeAgentProviderOverloaded,
		},
		{
			name:  "provider answer",
			event: native.StreamEvent{Type: native.EventError, Cause: overloaded},
			code:  apperror.CodeAgentProviderOverloaded,
		},
		{
			name:  "protected context overflow",
			event: native.StreamEvent{Type: native.EventError, Cause: fmt.Errorf("prepare context view: %w", contextfrag.ErrProtectedContextOverflow)},
			code:  apperror.CodeContextProtectedOverflow,
		},
		{
			name:  "budget unsatisfied",
			event: native.StreamEvent{Type: native.EventError, Cause: fmt.Errorf("prepare context view: %w", contextfrag.ErrBudgetUnsatisfied)},
			code:  apperror.CodeContextBudgetUnsatisfied,
		},
		{
			// Only compacting or another model helps, whatever else the chain holds.
			name:  "context overflow over a provider answer",
			event: native.StreamEvent{Type: native.EventError, Cause: fmt.Errorf("%w: %w", contextfrag.ErrProtectedContextOverflow, overloaded)},
			code:  apperror.CodeContextProtectedOverflow,
		},
		{
			name:  "runtime failure",
			event: native.StreamEvent{Type: native.EventError, Cause: errors.New("checkpoint steered model call: SECRET")},
			code:  apperror.CodeAgentResponseInterrupted,
		},
		{
			name:  "no cause",
			event: native.StreamEvent{Type: native.EventError},
			code:  apperror.CodeAgentResponseInterrupted,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			failure := agentStreamFailure(tc.event)
			if got := apperror.CodeOf(failure); got != tc.code {
				t.Fatalf("code = %q, want %q", got, tc.code)
			}
			if cause := apperror.CauseOf(failure); !errors.Is(cause, tc.event.Cause) {
				t.Fatalf("cause = %v, want the event's cause %v", cause, tc.event.Cause)
			}
		})
	}
	if err := agentStreamFailure(native.StreamEvent{Type: native.EventTextDelta}); err != nil {
		t.Fatalf("agentStreamFailure(text delta) = %v, want nil", err)
	}
}

// The cause of a failure never leaves the application: the published event
// carries the code and its catalog detail, and nothing of the cause.
func TestPublicAgentStreamEventRedactsPrivateFailure(t *testing.T) {
	for _, cause := range []error{
		errors.New("SECRET provider payload"),
		&sdk.APIError{Provider: "openai-completions", StatusCode: 401, Kind: sdk.KindAuthentication, Message: "Incorrect API key provided: SECRET", RequestID: "req_SECRET"},
		fmt.Errorf("SECRET window: %w", contextfrag.ErrBudgetUnsatisfied),
	} {
		event := publicAgentStreamEvent(native.StreamEvent{Type: native.EventError, Cause: cause})
		definition, ok := apperror.Lookup(apperror.Code(event.Code))
		if !ok || event.Error != definition.Detail || event.Cause != nil {
			t.Fatalf("public event = %#v, want a catalogued code with its detail", event)
		}
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal public event: %v", err)
		}
		if strings.Contains(string(data), "SECRET") {
			t.Fatalf("private detail leaked: %s", data)
		}
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
	persisted       []messagepkg.PersistInput
	persistErr      error
	roundPersistErr error
	roundOptions    []messagepkg.RoundPersistenceOptions
	deleted         [][]string
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
	if options.Replacement != nil && options.Replacement.RequestMessageID != "" {
		persisted = append(persisted, messagepkg.Message{ID: "cloned-request", Role: "user"})
	}
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

func (*recordingMessageService) ListLatest(context.Context, string, int32) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*recordingMessageService) ListBySession(context.Context, string) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*recordingMessageService) ListSinceBySession(context.Context, string, time.Time) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*recordingMessageService) ListHistoryWithinBytes(context.Context, string, messagepkg.HistoryWindow) (messagepkg.HistoryLoad, error) {
	return messagepkg.HistoryLoad{}, nil
}

func (*recordingMessageService) ListHistoryByIDs(context.Context, string, []string) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*recordingMessageService) HistoryPositionByID(context.Context, string, string) (messagepkg.HistoryPosition, error) {
	return messagepkg.HistoryPosition{}, nil
}

func (*recordingMessageService) ListTurnResponseSourcesWithinBytes(context.Context, string, messagepkg.HistoryPosition, int64) (messagepkg.HistoryLoad, error) {
	return messagepkg.HistoryLoad{}, nil
}

func (*recordingMessageService) ListTurnResponseSourcesByIDs(context.Context, string, []string) ([]messagepkg.Message, error) {
	return nil, nil
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
	resolver.SetRuntimeRegistry(testRuntimes(t, acpagent.NewDriver(&recordingACPPrompter{})))
	preflightCalled := false
	postPersistCalled := false

	_, _, err := resolver.streamChatWSResultWithHooks(
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
	if got := apperror.CodeOf(err); got != apperror.CodeExternalAgentTurnReplacementUnsupported {
		t.Fatalf("error code = %q, want %q", got, apperror.CodeExternalAgentTurnReplacementUnsupported)
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
		nil,
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

func TestPersistPreflightFailureTurnUsesAdmittedIdentityAndAtomicRound(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	service := &Service{messageService: messages, logger: slog.New(slog.DiscardHandler)}
	position := int64(7)
	err := service.persistPreflightFailureTurn(context.Background(), ChatRequest{
		BotID:        "bot-1",
		ThreadID:     "session-1",
		RunID:        "run-1",
		TurnID:       "turn-1",
		TurnPosition: &position,
		Query:        "hello",
	}, apperror.CodeHookUserMessageFailed)
	if err != nil {
		t.Fatalf("persistPreflightFailureTurn: %v", err)
	}
	if len(messages.persisted) != 2 {
		t.Fatalf("persisted %d messages, want user + assistant", len(messages.persisted))
	}
	for i, input := range messages.persisted {
		if i == 0 && (input.TurnID != "turn-1" || input.TurnPosition == nil || *input.TurnPosition != position) {
			t.Fatalf("user identity = (%q, %v), want (turn-1, %d)", input.TurnID, input.TurnPosition, position)
		}
		if input.Metadata[messagepkg.HistoryFailureOriginMetadataKey] != messagepkg.HistoryFailureOriginUserMessageHook {
			t.Fatalf("message %d metadata = %#v, want Hook failure origin", i, input.Metadata)
		}
		if i == 1 && input.Metadata[messagepkg.HistoryErrorCodeMetadataKey] != string(apperror.CodeHookUserMessageFailed) {
			t.Fatalf("assistant metadata = %#v, want Hook failure code", input.Metadata)
		}
	}
	if len(messages.roundOptions) != 1 {
		t.Fatalf("round options = %d, want one atomic write", len(messages.roundOptions))
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

func TestPersistTurnFailureStartsRetryReplacementAtomically(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	resolver := &Service{
		messageService: messages,
		logger:         slog.New(slog.DiscardHandler),
	}

	persisted, err := resolver.persistTurnFailure(
		context.Background(),
		ChatRequest{
			BotID: "bot-1", ThreadID: "session-1", Query: "hello",
			ReusePersistedUserMessage: true, PersistedUserMessageID: "old-request",
			TurnReplacement: &messagepkg.TurnReplacement{OldTurnID: "old-turn", ReplacementTurnID: "new-turn", ReplacementTurnPosition: int64Pointer(2), RequestMessageID: "old-request"},
		},
		resolvedContext{},
		apperror.CodeAgentResponseTimeout,
	)
	if err != nil {
		t.Fatalf("persistTurnFailure returned error: %v", err)
	}
	if len(persisted) != 2 || len(messages.persisted) != 1 || len(messages.roundOptions) != 1 || messages.roundOptions[0].Replacement == nil {
		t.Fatalf("retry failure was not persisted atomically with its cloned user: %#v %#v", persisted, messages.roundOptions)
	}
	if got := messages.persisted[0]; got.SkipHistoryTurn || got.Metadata[messagepkg.HistoryErrorCodeMetadataKey] != string(apperror.CodeAgentResponseTimeout) {
		t.Fatalf("retry failure lost binding or stable code: %#v", got)
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
		nil,
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
