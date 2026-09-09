package application

import (
	"context"
	"log/slog"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/uuid"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/runtimefence"
)

func stampedSteeringMessage(text string) sdk.Message {
	return native.StampContextInjection(sdk.UserMessage(text), event.ContextInjectionSteering)
}

func TestAgentStepCommitterLabelsLeadingUserRowsByStamp(t *testing.T) {
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
		stampedSteeringMessage("<message>stop</message>"),
		sdk.AssistantMessage("second"),
	}}); err != nil {
		t.Fatalf("commit step 1: %v", err)
	}

	if len(store.steps) != 2 || len(store.steps[0].Messages) != 2 || len(store.steps[1].Messages) != 3 {
		t.Fatalf("persisted steps = %#v", store.steps)
	}
	if injection := messagepkg.ContextInjectionFromMetadata(store.steps[0].Messages[0].Metadata); injection != nil {
		t.Fatalf("request user message marked as injection: %#v", injection)
	}
	prepared := messagepkg.ContextInjectionFromMetadata(store.steps[1].Messages[0].Metadata)
	if prepared == nil || prepared.Kind != messagepkg.ContextInjectionPrepared {
		t.Fatalf("prepared user message metadata = %#v", store.steps[1].Messages[0].Metadata)
	}
	steering := messagepkg.ContextInjectionFromMetadata(store.steps[1].Messages[1].Metadata)
	if steering == nil || steering.Kind != messagepkg.ContextInjectionSteering {
		t.Fatalf("steering user message metadata = %#v", store.steps[1].Messages[1].Metadata)
	}
	if store.steps[1].Messages[2].Role != "assistant" || messagepkg.ContextInjectionFromMetadata(store.steps[1].Messages[2].Metadata) != nil {
		t.Fatalf("assistant row = %#v", store.steps[1].Messages[2])
	}
}

func TestInterleaveInjectedMessagesStampsSteeringInContent(t *testing.T) {
	t.Parallel()

	round := []ModelMessage{
		{Role: "user", Content: newTextContent("hello")},
		{Role: "assistant", Content: newTextContent("working")},
		{Role: "assistant", Content: newTextContent("done")},
	}
	messages := interleaveInjectedMessages(round, []InjectedMessageRecord{
		{HeaderifiedText: "<message>stop</message>", InsertAfter: 1},
	})
	if len(messages) != 4 || messages[2].Role != "user" || messages[2].TextContent() != "<message>stop</message>" {
		t.Fatalf("messages = %#v", messages)
	}
	if kind := contextInjectionKindOf(messages[2]); kind != messagepkg.ContextInjectionSteering {
		t.Fatalf("stamp = %q, want steering", kind)
	}
	if kind := contextInjectionKindOf(messages[0]); kind != "" {
		t.Fatalf("request row stamped: %q", kind)
	}
	if messages := interleaveInjectedMessages(round, nil); len(messages) != 3 {
		t.Fatalf("no injections should return the round untouched: %#v", messages)
	}
}

// The terminal snapshot drops the already persisted request row and inserts
// synthetic tool closures before rows are written, so the marker must travel
// with the row itself rather than with its position.
func TestStoreRoundKeepsSteeringMarkerAcrossRowFiltering(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	service := &Service{messageService: messages, logger: slog.New(slog.DiscardHandler)}
	round := append([]ModelMessage{{Role: "user", Content: newTextContent("hello")}},
		sdkMessagesToModelMessages([]sdk.Message{sdk.AssistantMessage("working")})...)
	round = interleaveInjectedMessages(round, []InjectedMessageRecord{{HeaderifiedText: "<message>stop</message>", InsertAfter: 1}})
	round = append(round, sdkMessagesToModelMessages([]sdk.Message{sdk.AssistantMessage("done")})...)

	_, err := service.storeRoundWithOptionsResult(t.Context(), ChatRequest{
		BotID: "bot-1", ThreadID: "session-1", Query: "hello", UserMessagePersisted: true, ReusePersistedUserMessage: true,
	}, round, "model-1", storeRoundOptions{})
	if err != nil {
		t.Fatalf("storeRoundWithOptionsResult() error = %v", err)
	}
	var steering []messagepkg.PersistInput
	for _, persisted := range messages.persisted {
		if messagepkg.ContextInjectionFromMetadata(persisted.Metadata) != nil {
			steering = append(steering, persisted)
		}
	}
	if len(steering) != 1 || steering[0].Role != "user" {
		t.Fatalf("steering rows = %#v, want exactly the injected user row", steering)
	}
}
