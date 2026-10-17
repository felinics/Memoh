package application

import (
	"context"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/step"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/runtimefence"
)

// recordingStepPersister records the history boundary shared by native and
// subagent step tests, with an optional persistence failure.
type recordingStepPersister struct {
	*recordingMessageService
	steps              []messagepkg.AgentStep
	stepErr            error
	replacementRequest *messagepkg.Message
}

func TestAgentStepCommitterBindsContinuationToRequestMessage(t *testing.T) {
	service, handle := newDeferredSteerTestService(t)
	ctx := runtimefence.WithContext(context.Background(), runtimefence.Fence{
		BotID:     handle.BotID,
		SessionID: handle.SessionID,
		Token:     handle.FencingToken,
	})
	committer := service.newAgentStepCommitter(ctx, ChatRequest{
		BotID:                  handle.BotID,
		ThreadID:               handle.SessionID,
		RunID:                  handle.RunID,
		RunHandle:              handle,
		UserMessagePersisted:   true,
		PersistedUserMessageID: "request-message-id",
	}, resolvedContext{runConfig: native.RunConfig{ContextLifecycle: contextfrag.NewLifecycleHolder()}})
	if committer == nil {
		t.Fatal("missing agent step committer")
	}
	if _, err := committer.commit(ctx, 0, &step.Record{
		Messages: []sdk.Message{sdk.AssistantMessage("continuation response")},
	}); err != nil {
		t.Fatalf("commit continuation step: %v", err)
	}

	store := service.messageService.(*recordingStepPersister)
	if len(store.steps) != 1 || len(store.steps[0].Messages) != 1 {
		t.Fatalf("persisted steps = %#v, want one message", store.steps)
	}
	if got := store.steps[0].Messages[0].TurnRequestMessageID; got != "request-message-id" {
		t.Fatalf("turn request message ID = %q, want %q", got, "request-message-id")
	}
}

func (s *recordingStepPersister) PersistAgentStep(_ context.Context, step messagepkg.AgentStep) ([]messagepkg.Message, error) {
	if s.stepErr != nil {
		return nil, s.stepErr
	}
	s.steps = append(s.steps, step)
	result := make([]messagepkg.Message, 0, len(step.Messages)+1)
	if step.Replacement != nil && step.Replacement.RequestMessageID != "" {
		request := messagepkg.Message{ID: "replacement-request", Role: "user", TurnID: step.Replacement.ReplacementTurnID}
		if s.replacementRequest != nil {
			request = *s.replacementRequest
		}
		result = append(result, request)
	}
	for _, input := range step.Messages {
		result = append(result, messagepkg.Message{ID: "committed", Role: input.Role, BotID: input.BotID, SessionID: input.SessionID, Metadata: input.Metadata, Content: input.Content})
	}
	return result, nil
}
