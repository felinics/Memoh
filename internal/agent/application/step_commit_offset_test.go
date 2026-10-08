package application

import (
	"context"
	"strings"
	"testing"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/step"
	"github.com/felinics/memoh/internal/runtimefence"
)

// A decision continuation resumes a run whose earlier steps are persisted, so
// the loop numbers its commits from req.StepIndexOffset. The committer must
// expect the same first index; a fresh cursor at zero rejects every commit
// and the answer to a pending ask_user never reaches the model.
func TestStepCommitterStartsAtRequestStepOffset(t *testing.T) {
	service, handle := newDeferredSteerTestService(t, sessionruntime.NewMemoryBackend())
	ctx := runtimefence.WithContext(context.Background(), runtimefence.Fence{BotID: handle.BotID, SessionID: handle.SessionID, Token: handle.FencingToken})
	req := ChatRequest{
		BotID: handle.BotID, ThreadID: handle.SessionID, RunID: handle.RunID, RunHandle: handle,
		UserMessagePersisted: true, PersistedUserMessageID: "user", StepIndexOffset: 2,
	}
	committer := service.newAgentStepCommitter(ctx, req, resolvedContext{runConfig: native.RunConfig{ContextLifecycle: contextfrag.NewLifecycleHolder()}})
	if committer == nil {
		t.Fatal("missing fenced step committer")
	}
	if committer.nextStep != 2 {
		t.Fatalf("nextStep = %d, want the request offset 2", committer.nextStep)
	}
	if _, err := committer.commit(ctx, 0, &step.Record{}); err == nil || !strings.Contains(err.Error(), "unexpected agent step 0, want 2") {
		t.Fatalf("commit at step 0 error = %v, want the offset guard", err)
	}
}
