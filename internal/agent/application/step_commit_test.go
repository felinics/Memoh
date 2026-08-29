package application

import (
	"context"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/uuid"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/runtimefence"
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
	holder := contextfrag.NewLifecycleHolder()
	holder.SetManifest(contextfrag.Manifest{View: contextfrag.ViewRunConfigPreProvider})
	req := ChatRequest{BotID: botID, ThreadID: sessionID, RunID: runID, TurnID: turnID, TurnPosition: &position, Query: "hello", SkipMemoryExtraction: true}
	ctx := runtimefence.WithContext(context.Background(), runtimefence.Fence{BotID: botID, SessionID: sessionID, Token: 7})
	rc := resolvedContext{model: models.GetResponse{ID: uuid.NewString()}}
	rc.runConfig.ContextLifecycle = holder
	committer := service.newAgentStepCommitter(ctx, req, rc)
	if committer == nil {
		t.Fatal("step committer was not enabled for an admitted fenced turn")
	}
	clock := newReasoningTimingTestClock()
	committer.reasoningTiming = newReasoningTimingTracker(clock.read)
	for i, text := range []string{"first", "second"} {
		if err := committer.commit(ctx, i, &sdk.StepResult{Messages: []sdk.Message{sdk.AssistantMessage(text)}}); err != nil {
			t.Fatalf("commit step %d: %v", i, err)
		}
	}
	partial := sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.ReasoningPart{Text: "partial reasoning"}}}
	committer.reasoningTiming.observe(native.StreamEvent{Type: native.EventReasoningDelta, Delta: "partial reasoning"})
	clock.advance(1500 * time.Millisecond)
	if err := committer.interrupt(ctx, 2, &sdk.StepResult{Messages: []sdk.Message{partial}}); err != nil {
		t.Fatalf("persist interrupted step: %v", err)
	}
	if len(store.steps) != 3 || len(store.steps[0].Messages) != 2 || len(store.steps[1].Messages) != 1 || !store.steps[2].Interrupted {
		t.Fatalf("persisted steps = %#v, want two complete plus one interrupted", store.steps)
	}
	if store.steps[2].Messages[0].Metadata[messagepkg.AgentStepInterruptedMetadataKey] != true {
		t.Fatalf("interrupted metadata = %#v", store.steps[2].Messages[0].Metadata)
	}
	timings := messagepkg.ReasoningTimingFromMetadata(store.steps[2].Messages[0].Metadata)
	if len(timings) != 1 || timings[0].DurationMS != 1500 || timings[0].State != "interrupted" {
		t.Fatalf("interrupted reasoning timing = %#v", timings)
	}
	if _, ok := store.steps[0].Messages[1].Metadata[contextfrag.MetadataContextLifecycleKey]; !ok {
		t.Fatalf("first step lifecycle metadata = %#v", store.steps[0].Messages[1].Metadata)
	}
	snapshot, ok := holder.Snapshot()
	if !ok || snapshot.AssistantMessageID != "committed" {
		t.Fatalf("lifecycle snapshot = %#v, set = %v", snapshot, ok)
	}
	if got := store.steps[1].Messages[0].TurnRequestMessageID; got != "committed" {
		t.Fatalf("second step request message = %q, want first committed user", got)
	}
	if len(committer.messages) != 3 {
		t.Fatalf("memory messages = %d, want interrupted output excluded", len(committer.messages))
	}
}
