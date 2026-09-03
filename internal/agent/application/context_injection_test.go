package application

import (
	"context"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/uuid"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/runtimefence"
)

func TestAgentStepCommitterMarksPreparedUserMessagesAsContextInjection(t *testing.T) {
	botID, sessionID, runID, turnID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	position := int64(1)
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
		t.Fatal("step committer was not enabled")
	}

	if err := committer.commit(ctx, 0, &sdk.StepResult{Messages: []sdk.Message{sdk.AssistantMessage("first")}}); err != nil {
		t.Fatalf("commit step 0: %v", err)
	}
	if err := committer.commit(ctx, 1, &sdk.StepResult{Messages: []sdk.Message{
		sdk.UserMessage("[Background tasks]\nnpm test running"),
		sdk.AssistantMessage("second"),
	}}); err != nil {
		t.Fatalf("commit step 1: %v", err)
	}

	if len(store.steps) != 2 || len(store.steps[0].Messages) != 2 || len(store.steps[1].Messages) != 2 {
		t.Fatalf("persisted steps = %#v", store.steps)
	}
	if injection := messagepkg.ContextInjectionFromMetadata(store.steps[0].Messages[0].Metadata); injection != nil {
		t.Fatalf("request user message marked as injection: %#v", injection)
	}
	injection := messagepkg.ContextInjectionFromMetadata(store.steps[1].Messages[0].Metadata)
	if injection == nil || injection.Kind != messagepkg.ContextInjectionPrepared {
		t.Fatalf("prepared user message metadata = %#v", store.steps[1].Messages[0].Metadata)
	}
	if store.steps[1].Messages[1].Role != "assistant" || messagepkg.ContextInjectionFromMetadata(store.steps[1].Messages[1].Metadata) != nil {
		t.Fatalf("assistant row = %#v", store.steps[1].Messages[1])
	}
}

func TestInterleaveInjectedMessagesReportsSteeringIndexes(t *testing.T) {
	t.Parallel()

	round := []ModelMessage{
		{Role: "user", Content: newTextContent("hello")},
		{Role: "assistant", Content: newTextContent("working")},
		{Role: "assistant", Content: newTextContent("done")},
	}
	messages, metadata := interleaveInjectedMessages(round, []InjectedMessageRecord{
		{HeaderifiedText: "<message>stop</message>", InsertAfter: 1},
	})
	if len(messages) != 4 || messages[2].Role != "user" || messages[2].TextContent() != "<message>stop</message>" {
		t.Fatalf("messages = %#v", messages)
	}
	if injection := messagepkg.ContextInjectionFromMetadata(metadata[2]); injection == nil || injection.Kind != messagepkg.ContextInjectionSteering {
		t.Fatalf("metadata = %#v", metadata)
	}
	if len(metadata) != 1 {
		t.Fatalf("metadata marks non-injected rows: %#v", metadata)
	}
	if messages, metadata := interleaveInjectedMessages(round, nil); len(messages) != 3 || metadata != nil {
		t.Fatalf("no injections should return the round untouched: %#v %#v", messages, metadata)
	}
}
