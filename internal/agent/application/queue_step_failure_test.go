package application

import (
	"context"
	"errors"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/step"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/runtimefence"
)

type failedSteerApplyBackend struct {
	*sessionruntime.MemoryBackend
	applyErr error
	releases int
}

func (b *failedSteerApplyBackend) ApplySteer(ctx context.Context, key sessionruntime.Key, ref sessionruntime.SteerClaimRef) error {
	if b.applyErr != nil {
		return b.applyErr
	}
	return b.MemoryBackend.ApplySteer(ctx, key, ref)
}

func (b *failedSteerApplyBackend) ReleaseSteer(ctx context.Context, key sessionruntime.Key, ref sessionruntime.SteerClaimRef) error {
	b.releases++
	return b.MemoryBackend.ReleaseSteer(ctx, key, ref)
}

func TestStepCommitSeparatesHistoryFailureFromQueueFailure(t *testing.T) {
	for _, historyFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "queue apply after committed history", true: "history before queue apply"}[historyFails], func(t *testing.T) {
			failure := errors.New("injected boundary failure")
			backend := &failedSteerApplyBackend{MemoryBackend: sessionruntime.NewMemoryBackend()}
			service, handle := newDeferredSteerTestService(t, backend)
			ctx := runtimefence.WithContext(context.Background(), runtimefence.Fence{BotID: handle.BotID, SessionID: handle.SessionID, Token: handle.FencingToken})
			item, err := service.EnqueueSteer(ctx, testQueueInput(handle.BotID, handle.SessionID, "steer-failure", "adjust"))
			if err != nil {
				t.Fatal(err)
			}
			req := ChatRequest{BotID: handle.BotID, ThreadID: handle.SessionID, RunID: handle.RunID, RunHandle: handle, UserMessagePersisted: true, PersistedUserMessageID: "user", QueueSteerEnabled: true}
			committer := service.newAgentStepCommitter(ctx, req, resolvedContext{runConfig: native.RunConfig{ContextLifecycle: contextfrag.NewLifecycleHolder()}})
			if committer == nil {
				t.Fatal("missing fenced step committer")
			}
			if _, err := committer.queueStep.commit(ctx, queueStepToolLoop, messagepkg.AgentStep{}, nil); err != nil {
				t.Fatal(err)
			}
			store := service.messageService.(*recordingStepPersister)
			if historyFails {
				store.stepErr = failure
			} else {
				backend.applyErr = failure
			}
			record := &step.Record{
				Result:   sdk.ModelResult{FinishReason: sdk.FinishReasonStop},
				Messages: []sdk.Message{sdk.AssistantMessage("committed response")},
			}
			if _, err := committer.commit(ctx, 0, record); !errors.Is(err, failure) {
				t.Fatalf("commit error=%v", err)
			}
			pending, _, err := service.sessionManager.PendingQueues(ctx, sessionruntime.Key{BotID: handle.BotID, SessionID: handle.SessionID}, 0)
			if err != nil {
				t.Fatal(err)
			}
			if historyFails {
				if len(committer.persistedMessages()) != 0 || committer.nextStep != 0 || backend.releases != 1 || len(pending) != 1 || pending[0].ID != item.ID {
					t.Fatal("uncommitted step did not return its claim")
				}
			} else {
				if len(committer.persistedMessages()) != 1 || committer.nextStep != 1 || backend.releases != 0 || len(pending) != 0 {
					t.Fatal("committed prefix was lost or its claim was released")
				}
				if _, err := committer.commit(ctx, 0, record); err == nil {
					t.Fatal("already-persisted step was accepted twice")
				}
				if len(store.steps) != 1 {
					t.Fatalf("history writes=%d", len(store.steps))
				}
			}
		})
	}
}
