package application

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/step"
	"github.com/felinics/memoh/internal/apperror"
)

func TestCheckpointDistinguishesUserAbortFromRevokedOwner(t *testing.T) {
	for _, cause := range []error{context.Canceled, sessionruntime.ErrRunOwnershipLost} {
		t.Run(cause.Error(), func(t *testing.T) {
			store := &recordingStepPersister{recordingMessageService: &recordingMessageService{}}
			owner, cancel := context.WithCancelCause(context.Background())
			committer := &agentStepCommitter{service: &Service{}, persister: store, ownerContext: owner, req: ChatRequest{BotID: "bot", ThreadID: "session", RunID: "run", UserMessagePersisted: true}, rc: resolvedContext{runConfig: native.RunConfig{ContextLifecycle: contextfrag.NewLifecycleHolder()}}}
			cancel(cause)
			err := committer.interrupt(context.WithoutCancel(owner), 0, &step.Record{Messages: []sdk.Message{sdk.AssistantMessage("partial")}})
			if errors.Is(cause, sessionruntime.ErrRunOwnershipLost) {
				if !errors.Is(err, cause) || len(store.steps) != 0 {
					t.Fatalf("revoked checkpoint: writes=%d err=%v", len(store.steps), err)
				}
			} else if err != nil || len(store.steps) != 1 {
				t.Fatalf("user abort checkpoint: writes=%d err=%v", len(store.steps), err)
			}
		})
	}
}

func TestFinishFailurePreservesOwnershipAndPartialOutput(t *testing.T) {
	for _, tt := range []struct {
		name        string
		cancelCause error
		partial     bool
		retry       bool
		code        apperror.Code
		wantSteps   int
	}{
		{name: "user cancel", cancelCause: context.Canceled},
		{name: "failed stream drained after disconnect", cancelCause: context.Canceled, code: apperror.CodeChatGPTNotConnected, wantSteps: 1},
		{name: "revoked owner", cancelCause: sessionruntime.ErrRunOwnershipLost, code: apperror.CodeChatGPTNotConnected},
		{name: "preserve partial output", partial: true, code: apperror.CodeChatGPTNotConnected, wantSteps: 1},
		{name: "retry does not append failure", retry: true, code: apperror.CodeChatGPTNotConnected},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &recordingStepPersister{recordingMessageService: &recordingMessageService{}}
			owner, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			committer := &agentStepCommitter{
				service: &Service{logger: slog.New(slog.DiscardHandler)}, persister: store, ownerContext: owner,
				req: ChatRequest{BotID: "bot", ThreadID: "session", RunID: "run", UserMessagePersisted: true, SkipHistoryTurn: tt.retry},
				rc:  resolvedContext{runConfig: native.RunConfig{ContextLifecycle: contextfrag.NewLifecycleHolder()}},
			}
			if tt.partial {
				if err := committer.interrupt(owner, 0, &step.Record{Messages: []sdk.Message{sdk.AssistantMessage("partial")}}); err != nil {
					t.Fatal(err)
				}
			}
			if tt.cancelCause != nil {
				cancel(tt.cancelCause)
			}
			err := committer.finish(context.WithoutCancel(owner), 0, tt.code)
			if errors.Is(tt.cancelCause, sessionruntime.ErrRunOwnershipLost) {
				if !errors.Is(err, tt.cancelCause) {
					t.Fatalf("revoked finish error = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if len(store.steps) != tt.wantSteps {
				t.Fatalf("persisted %d steps, want %d", len(store.steps), tt.wantSteps)
			}
		})
	}
}

func TestSubagentRejectsRevokedCheckpointWithDetachedCallback(t *testing.T) {
	store := &recordingStepPersister{recordingMessageService: &recordingMessageService{}}
	service := &Service{messageService: store}
	owner, cancel := context.WithCancelCause(subagentRunContext("bot", "session", 9))
	_, checkpoint := service.SubagentStepCommit(owner, "bot", "session", "model", "request", nil, nil)
	if checkpoint == nil {
		t.Fatal("missing checkpoint callback")
	}
	cancel(sessionruntime.ErrRunOwnershipLost)
	err := checkpoint(context.WithoutCancel(owner), 0, &step.Record{Messages: []sdk.Message{sdk.AssistantMessage("late")}})
	if !errors.Is(err, sessionruntime.ErrRunOwnershipLost) || len(store.steps) != 0 {
		t.Fatalf("revoked subagent checkpoint: writes=%d err=%v", len(store.steps), err)
	}
}
