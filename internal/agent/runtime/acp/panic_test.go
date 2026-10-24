package acp

import (
	"context"
	"log/slog"
	"testing"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

type panicApprovalCleanup struct{ fakeToolApprovalService }

func (*panicApprovalCleanup) CancelPendingForSession(context.Context, string, string, string) ([]toolapproval.Request, error) {
	panic("approval callback")
}

type panicInputCleanup struct{ fakeUserInputCanceller }

func (*panicInputCleanup) CancelPendingForSession(context.Context, string, string, string) ([]userinput.Request, error) {
	panic("input callback")
}

func TestPendingDecisionPanicReturnsInternal(t *testing.T) {
	for _, kind := range []string{"approval", "input"} {
		t.Run(kind, func(t *testing.T) {
			pool := &SessionPool{logger: slog.New(slog.DiscardHandler)}
			approval := &fakeToolApprovalService{}
			input := &fakeUserInputCanceller{}
			pool.approval, pool.userInput = approval, input
			if kind == "approval" {
				pool.approval = &panicApprovalCleanup{}
			} else {
				pool.userInput = &panicInputCleanup{}
			}
			err := pool.cancelPendingDecisions(context.Background(), "bot", "session", "stop")
			if !external.IsPanic(err) {
				t.Fatalf("cleanup = %v", err)
			}
			if kind == "approval" && input.cancelCount != 1 {
				t.Fatal("input cleanup was disturbed")
			}
			if kind == "input" && approval.cancelCount != 1 {
				t.Fatal("approval cleanup was disturbed")
			}
		})
	}
}

// The reaper serves every idle runtime. A runtime whose teardown panics does
// not take the process down, and the runtime is left closed rather than
// half-torn-down. Here the panic comes from reporting a decision cleanup with
// no logger, which is enough to put a panic in the teardown path.
func TestReaperPanicInOneRuntimeDoesNotEndTheReaper(t *testing.T) {
	pool := &SessionPool{
		runtimes:  map[string]*runtimeHandle{},
		bySession: map[string]string{},
	}
	broken := &runtimeHandle{id: "broken", status: stateIdle, boundSession: "session", hadPrompt: true}
	pool.runtimes[broken.id] = broken

	if pool.tryCloseIdle(broken, 0) {
		t.Fatal("tryCloseIdle reported a close for a runtime whose teardown panicked")
	}
	broken.state.Lock()
	closed := broken.closed
	broken.state.Unlock()
	if !closed {
		t.Fatal("the runtime was left half-torn-down")
	}
}
