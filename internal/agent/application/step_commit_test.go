package application

import (
	"context"
	"testing"

	"github.com/google/uuid"
	sdk "github.com/memohai/twilight-ai/sdk"

	messagepkg "github.com/memohai/memoh/internal/chat/message"
	"github.com/memohai/memoh/internal/models"
	"github.com/memohai/memoh/internal/runtimefence"
)

type recordingStepPersister struct {
	*recordingMessageService
	steps []messagepkg.AgentStep
}

func (s *recordingStepPersister) PersistAgentStep(_ context.Context, step messagepkg.AgentStep) ([]messagepkg.Message, error) {
	s.steps = append(s.steps, step)
	result := make([]messagepkg.Message, len(step.Messages))
	for i, input := range step.Messages {
		result[i] = messagepkg.Message{ID: "committed", Role: input.Role}
	}
	return result, nil
}

func TestAgentStepCommitterPersistsOnlyStepDelta(t *testing.T) {
	botID, sessionID, runID, turnID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	position := int64(4)
	store := &recordingStepPersister{recordingMessageService: &recordingMessageService{}}
	service := &Service{messageService: store}
	req := ChatRequest{BotID: botID, ThreadID: sessionID, RunID: runID, TurnID: turnID, TurnPosition: &position, Query: "hello", SkipMemoryExtraction: true}
	ctx := runtimefence.WithContext(context.Background(), runtimefence.Fence{BotID: botID, SessionID: sessionID, Token: 7})
	committer := service.newAgentStepCommitter(ctx, req, resolvedContext{model: models.GetResponse{ID: uuid.NewString()}})
	if committer == nil {
		t.Fatal("step committer was not enabled for an admitted fenced turn")
	}
	for i, text := range []string{"first", "second"} {
		if err := committer.commit(ctx, i, &sdk.StepResult{Messages: []sdk.Message{sdk.AssistantMessage(text)}}); err != nil {
			t.Fatalf("commit step %d: %v", i, err)
		}
	}
	partial := sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.ReasoningPart{Text: "partial reasoning"}}}
	if err := committer.interrupt(ctx, 2, &sdk.StepResult{Messages: []sdk.Message{partial}}); err != nil {
		t.Fatalf("persist interrupted step: %v", err)
	}
	if len(store.steps) != 3 || len(store.steps[0].Messages) != 2 || len(store.steps[1].Messages) != 1 || !store.steps[2].Interrupted {
		t.Fatalf("persisted steps = %#v, want two complete plus one interrupted", store.steps)
	}
	if store.steps[2].Messages[0].Metadata[messagepkg.AgentStepInterruptedMetadataKey] != true {
		t.Fatalf("interrupted metadata = %#v", store.steps[2].Messages[0].Metadata)
	}
	if got := store.steps[1].Messages[0].TurnRequestMessageID; got != "committed" {
		t.Fatalf("second step request message = %q, want first committed user", got)
	}
	if len(committer.messages) != 3 {
		t.Fatalf("memory messages = %d, want interrupted output excluded", len(committer.messages))
	}
}
