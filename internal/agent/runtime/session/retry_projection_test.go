package sessionruntime

import (
	"context"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/turn"
)

func startRetryProjectionRun(t *testing.T, name string) (*Manager, RunHandle, Subscription) {
	t.Helper()
	manager := testRuntimeManager(t, NewMemoryBackend(), "owner-"+name)
	sub, err := manager.Subscribe(context.Background(), testBotID, "session-"+name)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(sub.Close)
	if err := manager.StartRun(context.Background(), testBotID, "session-"+name, "run-"+name, make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start run: %v", err)
	}
	return manager, requireRunHandle(t, manager, testBotID, "session-"+name, "run-"+name), sub
}

func retryDelta(t *testing.T, sub Subscription) *CurrentRunPatch {
	t.Helper()
	event := waitRuntimeEvent(t, sub.C, func(event Event) bool {
		return event.Type == EventRuntimeDelta && event.Delta != nil && event.Delta.Run != nil &&
			(event.Delta.Run.Retry != nil || event.Delta.Run.ClearRetry)
	})
	return event.Delta.Run
}

func TestRuntimeProjectionCarriesRetryUntilNextOutput(t *testing.T) {
	manager, handle, sub := startRetryProjectionRun(t, "retry-proj")
	ctx := context.Background()
	if _, err := manager.HandleAgentEvent(ctx, handle, native.StreamEvent{
		Type: native.EventRetry, Attempt: 2, MaxAttempt: 4, RetryDelayMs: 1500, RetryReason: "rate_limited",
	}); err != nil {
		t.Fatalf("retry event: %v", err)
	}
	patch := retryDelta(t, sub)
	if patch.ClearRetry || patch.Retry == nil {
		t.Fatalf("retry patch = %#v", patch)
	}
	want := RunRetryView{Attempt: 2, MaxAttempt: 4, DelayMs: 1500, Reason: "rate_limited"}
	got := *patch.Retry
	if !got.RetryAt.After(time.Time{}) || got.RetryAt.Sub(*patch.UpdatedAt) != 1500*time.Millisecond {
		t.Fatalf("retry_at = %v, updated_at = %v", got.RetryAt, patch.UpdatedAt)
	}
	got.RetryAt = time.Time{}
	if got != want {
		t.Fatalf("retry = %#v, want %#v", got, want)
	}
	snapshot, err := manager.Snapshot(ctx, testBotID, "session-retry-proj")
	if err != nil || snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Retry == nil ||
		snapshot.CurrentRunView.Retry.Attempt != 2 || snapshot.CurrentRunView.Retry.Reason != "rate_limited" {
		t.Fatalf("snapshot retry = %#v, %v", snapshot.CurrentRunView, err)
	}

	if _, err := manager.HandleAgentEvent(ctx, handle, native.StreamEvent{Type: native.EventTextDelta, Delta: "hi"}); err != nil {
		t.Fatalf("text event: %v", err)
	}
	if patch := retryDelta(t, sub); !patch.ClearRetry || patch.Retry != nil {
		t.Fatalf("clear patch = %#v", patch)
	}
	snapshot, err = manager.Snapshot(ctx, testBotID, "session-retry-proj")
	if err != nil || snapshot.CurrentRunView.Retry != nil {
		t.Fatalf("snapshot after output = %#v, %v", snapshot.CurrentRunView, err)
	}
}

func TestRuntimeProjectionClearsRetryOnTerminal(t *testing.T) {
	manager, handle, sub := startRetryProjectionRun(t, "retry-term")
	ctx := context.Background()
	if _, err := manager.HandleAgentEvent(ctx, handle, native.StreamEvent{Type: native.EventRetry, Attempt: 1, MaxAttempt: 3, RetryDelayMs: 10}); err != nil {
		t.Fatalf("retry event: %v", err)
	}
	_ = retryDelta(t, sub)
	if _, err := manager.HandleAgentEvent(ctx, handle, native.StreamEvent{Type: native.EventError, Code: "agent.response_timeout"}); err != nil {
		t.Fatalf("error event: %v", err)
	}
	if patch := retryDelta(t, sub); !patch.ClearRetry || patch.ErrorCode == nil || *patch.ErrorCode != "agent.response_timeout" {
		t.Fatalf("error patch = %#v", patch)
	}

	manager, handle, sub = startRetryProjectionRun(t, "retry-fin")
	if _, err := manager.HandleAgentEvent(ctx, handle, native.StreamEvent{Type: native.EventRetry, Attempt: 1, MaxAttempt: 3, RetryDelayMs: 10}); err != nil {
		t.Fatalf("retry event: %v", err)
	}
	_ = retryDelta(t, sub)
	if _, err := manager.FinishRun(ctx, handle, RunStatusErrored); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if patch := retryDelta(t, sub); !patch.ClearRetry || patch.Status == nil {
		t.Fatalf("finish patch = %#v", patch)
	}
	snapshot, err := manager.Snapshot(ctx, testBotID, "session-retry-fin")
	if err != nil || snapshot.CurrentRunView.Retry != nil {
		t.Fatalf("snapshot after finish = %#v, %v", snapshot.CurrentRunView, err)
	}
}
