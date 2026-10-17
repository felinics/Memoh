package application

import (
	"context"
	"log/slog"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/step"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/apperror"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	memprovider "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/runtimefence"
	"github.com/felinics/memoh/internal/settings"
)

func TestRetryStepStartsReplacementBeforeDecisionAndContinuesOnClonedRequest(t *testing.T) {
	service, handle := newDeferredSteerTestService(t)
	ctx := runtimefence.WithContext(context.Background(), runtimefence.Fence{BotID: handle.BotID, SessionID: handle.SessionID, Token: handle.FencingToken})
	replacement := &messagepkg.TurnReplacement{OldTurnID: "old-turn", ReplacementTurnID: "new-turn", ReplacementTurnPosition: int64Pointer(2), RequestMessageID: "old-request", Reason: "retry"}
	committer := service.newAgentStepCommitter(ctx, ChatRequest{
		BotID: handle.BotID, ThreadID: handle.SessionID, RunID: handle.RunID, RunHandle: handle,
		TurnID: "new-turn", TurnPosition: int64Pointer(2), TurnReplacement: replacement,
		ReusePersistedUserMessage: true, PersistedUserMessageID: "old-request",
	}, resolvedContext{runConfig: native.RunConfig{ContextLifecycle: contextfrag.NewLifecycleHolder()}})
	if committer == nil {
		t.Fatal("missing replacement step committer")
	}
	if _, err := committer.commit(ctx, 0, &step.Record{
		Messages: []sdk.Message{sdk.AssistantMessage("Which option?")}, Deferred: &toolexec.ToolApprovalResult{},
	}); err != nil {
		t.Fatalf("commit deferred step: %v", err)
	}
	store := service.messageService.(*recordingStepPersister)
	if len(store.steps) != 1 || store.steps[0].Replacement != replacement || !committer.replacementStarted {
		t.Fatalf("waiting step did not start its replacement: %#v", store.steps)
	}
	if len(store.steps[0].Messages) != 1 || store.steps[0].Messages[0].SkipHistoryTurn {
		t.Fatalf("retry should clone its user in the bound transaction: %#v", store.steps[0])
	}
	if committer.turnRequestMessageID != "replacement-request" {
		t.Fatalf("continuation anchor = %q, want cloned request", committer.turnRequestMessageID)
	}
	if _, err := committer.commit(ctx, 1, &step.Record{Messages: []sdk.Message{sdk.AssistantMessage("done")}}); err != nil {
		t.Fatalf("commit follow-up: %v", err)
	}
	if store.steps[1].Replacement != nil || store.steps[1].Messages[0].TurnRequestMessageID != "replacement-request" {
		t.Fatalf("follow-up tried to replace again or used the old turn: %#v", store.steps[1])
	}
}

func TestReplacementFailureWithoutTextUsesBoundInterruptedStep(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "edit", true: "retry"}[reuse], func(t *testing.T) {
			service, handle := newDeferredSteerTestService(t)
			ctx := runtimefence.WithContext(context.Background(), runtimefence.Fence{BotID: handle.BotID, SessionID: handle.SessionID, Token: handle.FencingToken})
			replacement := &messagepkg.TurnReplacement{OldTurnID: "old-turn", ReplacementTurnID: "new-turn", ReplacementTurnPosition: int64Pointer(2)}
			if reuse {
				replacement.RequestMessageID = "old-request"
			}
			committer := service.newAgentStepCommitter(ctx, ChatRequest{
				BotID: handle.BotID, ThreadID: handle.SessionID, RunID: handle.RunID, RunHandle: handle,
				TurnID: "new-turn", TurnPosition: int64Pointer(2), TurnReplacement: replacement, Query: "edited request",
				ReusePersistedUserMessage: reuse, PersistedUserMessageID: "old-request",
			}, resolvedContext{runConfig: native.RunConfig{ContextLifecycle: contextfrag.NewLifecycleHolder()}})
			if committer == nil {
				t.Fatal("missing replacement step committer")
			}
			if _, err := committer.recordFailure(ctx, "", apperror.CodeAgentProviderRequestRejected); err != nil {
				t.Fatalf("record failure: %v", err)
			}
			store := service.messageService.(*recordingStepPersister)
			if len(store.steps) != 1 || store.steps[0].Replacement != replacement || !store.steps[0].Interrupted {
				t.Fatalf("failure did not cross bound replacement transaction: %#v", store.steps)
			}
			messages := store.steps[0].Messages
			if len(messages) != map[bool]int{false: 2, true: 1}[reuse] {
				t.Fatalf("failure inputs = %#v", messages)
			}
			assistant := messages[len(messages)-1]
			if assistant.SkipHistoryTurn || assistant.Metadata[messagepkg.HistoryErrorCodeMetadataKey] != string(apperror.CodeAgentProviderRequestRejected) {
				t.Fatalf("failure row lost turn binding or stable code: %#v", assistant)
			}
		})
	}
}

func TestRetryStepMemoryUsesClonedRequest(t *testing.T) {
	testRetryStepMemoryUsesClonedRequest(t, false)
}

func TestRetrySteeredStepMemoryUsesClonedRequest(t *testing.T) {
	testRetryStepMemoryUsesClonedRequest(t, true)
}

func testRetryStepMemoryUsesClonedRequest(t *testing.T, steered bool) {
	t.Helper()
	request := storedMemoryTestMessage(t, "replacement-request", "session-1", "user", "retried request")
	request.TurnID = "new-turn"
	store := &recordingStepPersister{recordingMessageService: &recordingMessageService{}, replacementRequest: &request}
	memory := &storeRoundMemoryProvider{afterChat: make(chan memprovider.AfterChatRequest, 1)}
	registry := memprovider.NewRegistry(slog.New(slog.DiscardHandler))
	registry.Register(storeRoundMemoryProviderID, memory)
	service := &Service{
		messageService: store, memoryRegistry: registry,
		settingsService: settings.NewService(slog.New(slog.DiscardHandler), &storeRoundSettingsQueries{}, nil, nil),
		logger:          slog.New(slog.DiscardHandler),
	}
	ctx := runtimefence.WithContext(context.Background(), runtimefence.Fence{BotID: storeRoundBotID, SessionID: "session-1", Token: 1})
	committer := service.newAgentStepCommitter(ctx, ChatRequest{
		BotID: storeRoundBotID, ThreadID: "session-1", RunID: "run-1", TurnID: "new-turn", TurnPosition: int64Pointer(2),
		ReusePersistedUserMessage: true, PersistedUserMessageID: "old-request",
		TurnReplacement: &messagepkg.TurnReplacement{
			OldTurnID: "old-turn", ReplacementTurnID: "new-turn", ReplacementTurnPosition: int64Pointer(2), RequestMessageID: "old-request",
		},
	}, resolvedContext{runConfig: native.RunConfig{ContextLifecycle: contextfrag.NewLifecycleHolder()}})
	if committer == nil {
		t.Fatal("missing replacement step committer")
	}
	nextStep := 0
	if steered {
		if _, err := committer.persist(ctx, 0, &step.Record{Messages: []sdk.Message{sdk.AssistantMessage("partial answer")}}, stepSteered, ""); err != nil {
			t.Fatalf("checkpoint replacement: %v", err)
		}
		nextStep++
	}
	if _, err := committer.commit(ctx, nextStep, &step.Record{Messages: []sdk.Message{sdk.AssistantMessage("new answer")}}); err != nil {
		t.Fatalf("commit replacement: %v", err)
	}
	if err := committer.finish(ctx, 0); err != nil {
		t.Fatalf("finish replacement: %v", err)
	}
	select {
	case got := <-memory.afterChat:
		if len(got.Messages) != 2 || got.Messages[0].SourceMessageID != "session-1/replacement-request" || got.Messages[0].Content != "retried request" {
			t.Fatalf("memory transcript = %#v, want cloned request exactly once", got.Messages)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retry memory extraction was skipped")
	}
}
