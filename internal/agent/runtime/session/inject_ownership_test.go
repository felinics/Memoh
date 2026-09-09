package sessionruntime

import (
	"context"
	"fmt"
	"testing"

	"github.com/felinics/memoh/internal/agent/turn"
)

func TestFinishRunStopsInjectSendsWithoutClosingBorrowedChannel(t *testing.T) {
	manager := testRuntimeManager(t, NewMemoryBackend(), "owner-inject-stop")

	for i := range 100 {
		sessionID := fmt.Sprintf("session-inject-stop-%d", i)
		runID := fmt.Sprintf("stream-inject-stop-%d", i)
		injectCh := make(chan turn.InjectMessage, 1)
		if err := manager.StartRun(context.Background(), testBotID, sessionID, runID, make(chan struct{}, 1), func() {}, injectCh); err != nil {
			t.Fatalf("start run %d: %v", i, err)
		}
		ctrl := manager.localControlForScope(testBotID, sessionID, runID)
		if ctrl == nil {
			t.Fatalf("run %d has no local control", i)
		}
		start := make(chan struct{})
		steerDone := make(chan struct{})
		go func() {
			<-start
			_, _ = manager.Steer(context.Background(), testBotID, sessionID, runID, "race teardown")
			close(steerDone)
		}()
		close(start)
		if err := manager.FinishRun(context.Background(), requireRunHandle(t, manager, testBotID, sessionID, runID), RunStatusCompleted, ""); err != nil {
			t.Fatalf("finish run %d: %v", i, err)
		}
		receiveTestResult(t, "concurrent steer", steerDone)
		requireInjectStopped(t, ctrl)
		closeExecutionInjectChannel(t, injectCh)
	}
}

func TestFinishRunAcceptsExecutionClosedInjectChannel(t *testing.T) {
	manager := testRuntimeManager(t, NewMemoryBackend(), "owner-inject-closed")
	injectCh := make(chan turn.InjectMessage, 1)
	if err := manager.StartRun(context.Background(), testBotID, "session-inject-closed", "stream-inject-closed", make(chan struct{}, 1), func() {}, injectCh); err != nil {
		t.Fatalf("start run: %v", err)
	}
	ctrl := manager.localControlForScope(testBotID, "session-inject-closed", "stream-inject-closed")
	if ctrl == nil {
		t.Fatal("run has no local control")
	}
	closeExecutionInjectChannel(t, injectCh)
	if err := manager.FinishRun(context.Background(), requireRunHandle(t, manager, testBotID, "session-inject-closed", "stream-inject-closed"), RunStatusCompleted, ""); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	requireInjectStopped(t, ctrl)
}

func requireInjectStopped(t *testing.T, ctrl *runControl) {
	t.Helper()
	if ok, _ := ctrl.sendInject(context.Background(), turn.InjectMessage{Text: "after teardown"}); ok {
		t.Fatal("session runtime accepted inject after teardown")
	}
}

func closeExecutionInjectChannel(t *testing.T, injectCh chan turn.InjectMessage) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("session runtime closed execution-owned inject channel: %v", recovered)
		}
	}()
	close(injectCh)
}
