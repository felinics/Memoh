package application

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	sdk "github.com/memohai/twilight-ai/sdk"

	messagepkg "github.com/memohai/memoh/internal/chat/message"
	"github.com/memohai/memoh/internal/models"
	"github.com/memohai/memoh/internal/runtimefence"
)

type recordingStepPersister struct {
	*recordingMessageService
	steps []messagepkg.AgentStepCommit
}

func (s *recordingStepPersister) PersistAgentStep(_ context.Context, step messagepkg.AgentStepCommit) ([]messagepkg.Message, bool, error) {
	s.steps = append(s.steps, step)
	result := make([]messagepkg.Message, len(step.Messages))
	for i, input := range step.Messages {
		result[i] = messagepkg.Message{ID: fmt.Sprintf("step-%d-%d", step.StepIndex, i), Role: input.Role}
	}
	return result, true, nil
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
	if len(store.steps) != 2 || len(store.steps[0].Messages) != 2 || len(store.steps[1].Messages) != 1 {
		t.Fatalf("step message counts = %#v, want [user+assistant, assistant]", store.steps)
	}
	if got := store.steps[1].Messages[0].TurnRequestMessageID; got != "step-0-0" {
		t.Fatalf("second step request message = %q, want first committed user", got)
	}
}
