package sessionruntime

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/turn"
	chatview "github.com/felinics/memoh/internal/agent/view"
)

// commitHeldRunOutput commits what the run is holding, as the end of its
// output window would.
func commitHeldRunOutput(t *testing.T, manager *Manager, handle RunHandle) {
	t.Helper()
	ctrl := manager.localControlForHandle(handle)
	if ctrl == nil {
		t.Fatal("run control is missing")
	}
	ctrl.output.mu.Lock()
	defer ctrl.output.mu.Unlock()
	if err := manager.flushRunOutputLocked(context.Background(), ctrl); err != nil {
		t.Fatalf("commit held run output: %v", err)
	}
}

func startOutputTestRun(t *testing.T, manager *Manager, sessionID string) (RunHandle, Subscription, Event) {
	t.Helper()
	if err := manager.StartRun(context.Background(), testBotID, sessionID, testRunID, make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start run: %v", err)
	}
	handle := requireRunHandle(t, manager, testBotID, sessionID, testRunID)
	sub, err := manager.Subscribe(context.Background(), testBotID, sessionID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(sub.Close)
	baseline := waitRuntimeEvent(t, sub.C, func(event Event) bool { return event.Type == EventRuntimeSnapshot })
	return handle, sub, baseline
}

func TestRunOutputCommitsAWindowOfAppendsAsOneTransition(t *testing.T) {
	t.Parallel()

	manager := testRuntimeManager(t, NewMemoryBackend(), "owner-output-window")
	handle, sub, baseline := startOutputTestRun(t, manager, "session-output-window")
	ctx := context.Background()
	if _, err := manager.HandleAgentEvent(ctx, handle, native.StreamEvent{Type: native.EventToolCallStart, ToolName: "exec", ToolCallID: "call-1"}); err != nil {
		t.Fatalf("start tool call: %v", err)
	}
	toolStarted := waitRuntimeEvent(t, sub.C, func(event Event) bool { return event.Type == EventRuntimeDelta })
	if toolStarted.Seq != baseline.Seq+1 {
		t.Fatalf("tool start seq = %d, want %d", toolStarted.Seq, baseline.Seq+1)
	}
	for _, event := range []native.StreamEvent{
		{Type: native.EventToolCallProgress, ToolName: "exec", ToolCallID: "call-1", Progress: "queued"},
		{Type: native.EventToolCallProgress, ToolName: "exec", ToolCallID: "call-1", Progress: "running"},
		{Type: native.EventReasoningDelta, Delta: "think "},
		{Type: native.EventReasoningDelta, Delta: "twice"},
		{Type: native.EventTextDelta, Delta: "answer "},
		{Type: native.EventTextDelta, Delta: "here"},
	} {
		if _, err := manager.HandleAgentEvent(ctx, handle, event); err != nil {
			t.Fatalf("handle %s: %v", event.Type, err)
		}
	}

	committed := waitRuntimeEvent(t, sub.C, func(event Event) bool { return event.Type == EventRuntimeDelta })
	if committed.Seq != toolStarted.Seq+1 {
		t.Fatalf("output seq = %d, want one transition after %d", committed.Seq, toolStarted.Seq)
	}
	delta := committed.Delta
	if len(delta.ProgressAppends) != 2 || delta.ProgressAppends[0].Progress != "queued" || delta.ProgressAppends[1].Progress != "running" {
		t.Fatalf("progress appends = %#v, want both in order", delta.ProgressAppends)
	}
	if len(delta.MessageAppends) != 2 ||
		delta.MessageAppends[0].Type != chatview.UIMessageReasoning || delta.MessageAppends[0].Content != "think twice" ||
		delta.MessageAppends[1].Type != chatview.UIMessageText || delta.MessageAppends[1].Content != "answer here" {
		t.Fatalf("message appends = %#v, want one append per block", delta.MessageAppends)
	}
	select {
	case event := <-sub.C:
		t.Fatalf("unexpected event after the window = %#v", event)
	case <-time.After(3 * runOutputFlushInterval):
	}

	snapshot, err := manager.Snapshot(ctx, testBotID, "session-output-window")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Seq != committed.Seq {
		t.Fatalf("snapshot seq = %d, want the published %d", snapshot.Seq, committed.Seq)
	}
	assertRuntimeBlock(t, snapshot.CurrentRunView.Messages, chatview.UIMessageReasoning, "", "think twice")
	assertRuntimeBlock(t, snapshot.CurrentRunView.Messages, chatview.UIMessageText, "", "answer here")
}

func TestRunOutputCommitsBeforeTheTransitionThatFollowsIt(t *testing.T) {
	t.Parallel()

	manager := testRuntimeManager(t, NewMemoryBackend(), "owner-output-order")
	handle, sub, baseline := startOutputTestRun(t, manager, "session-output-order")
	ctx := context.Background()
	if _, err := manager.HandleAgentEvent(ctx, handle, native.StreamEvent{Type: native.EventTextDelta, Delta: "before the tool"}); err != nil {
		t.Fatalf("handle text: %v", err)
	}
	if _, err := manager.HandleAgentEvent(ctx, handle, native.StreamEvent{Type: native.EventToolCallStart, ToolName: "exec", ToolCallID: "call-1"}); err != nil {
		t.Fatalf("start tool call: %v", err)
	}

	output := waitRuntimeEvent(t, sub.C, func(event Event) bool { return event.Type == EventRuntimeDelta })
	if output.Seq != baseline.Seq+1 || len(output.Delta.MessageAppends) != 1 || output.Delta.MessageAppends[0].Content != "before the tool" {
		t.Fatalf("first delta = seq %d %#v, want the held text at %d", output.Seq, output.Delta, baseline.Seq+1)
	}
	tool := waitRuntimeEvent(t, sub.C, func(event Event) bool { return event.Type == EventRuntimeDelta })
	if tool.Seq != output.Seq+1 || len(tool.Delta.MessageUpserts) == 0 {
		t.Fatalf("second delta = seq %d %#v, want the tool start at %d", tool.Seq, tool.Delta, output.Seq+1)
	}
}

type failNextUpdateBackend struct {
	*MemoryBackend
	failNext atomic.Bool
}

func (b *failNextUpdateBackend) Update(ctx context.Context, key Key, update SnapshotUpdate) (Snapshot, bool, error) {
	if b.failNext.CompareAndSwap(true, false) {
		return Snapshot{}, false, errors.New("live state write failed")
	}
	return b.MemoryBackend.Update(ctx, key, update)
}

func TestRunOutputKeepsHeldOutputAcrossAFailedWrite(t *testing.T) {
	t.Parallel()

	backend := &failNextUpdateBackend{MemoryBackend: NewMemoryBackend()}
	manager := testRuntimeManager(t, backend, "owner-output-retry")
	handle, sub, baseline := startOutputTestRun(t, manager, "session-output-retry")
	ctx := context.Background()
	backend.failNext.Store(true)
	if _, err := manager.HandleAgentEvent(ctx, handle, native.StreamEvent{Type: native.EventTextDelta, Delta: "kept "}); err != nil {
		t.Fatalf("handle text: %v", err)
	}
	ctrl := manager.localControlForHandle(handle)
	deadline := time.After(5 * time.Second)
	for {
		ctrl.output.mu.Lock()
		failed := ctrl.output.err != nil
		ctrl.output.mu.Unlock()
		if failed {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the window's write never failed")
		case <-time.After(5 * time.Millisecond):
		}
	}

	// The failure reaches the run with its next append, which is still held.
	if _, err := manager.HandleAgentEvent(ctx, handle, native.StreamEvent{Type: native.EventTextDelta, Delta: "lost?"}); err == nil {
		t.Fatal("append after a failed window write reported no error")
	}
	if _, err := manager.HandleAgentEvent(ctx, handle, native.StreamEvent{Type: native.EventTextDelta, Delta: " no"}); err != nil {
		t.Fatalf("append after the reported failure: %v", err)
	}
	committed := waitRuntimeEvent(t, sub.C, func(event Event) bool { return event.Type == EventRuntimeDelta })
	if committed.Seq != baseline.Seq+1 || len(committed.Delta.MessageAppends) != 1 || committed.Delta.MessageAppends[0].Content != "kept lost? no" {
		t.Fatalf("committed output = seq %d %#v, want every held append at %d", committed.Seq, committed.Delta.MessageAppends, baseline.Seq+1)
	}
	snapshot, err := manager.Snapshot(ctx, testBotID, "session-output-retry")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	assertRuntimeBlock(t, snapshot.CurrentRunView.Messages, chatview.UIMessageText, "", "kept lost? no")
}

// lockedBuffer collects log output written from timer goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A failed window write whose retry commits the output costs the run nothing,
// so the run hears nothing of it; the failure is still on record.
func TestRunOutputRecordsAFailedWriteItsRetryCommitted(t *testing.T) {
	t.Parallel()

	backend := &failNextUpdateBackend{MemoryBackend: NewMemoryBackend()}
	logs := &lockedBuffer{}
	manager := testRuntimeManagerWithOptions(t, backend, Options{
		OwnerID: "owner-output-recovered", StateTTL: time.Hour, OwnerLeaseTTL: 2 * time.Second,
		Logger: slog.New(slog.NewTextHandler(logs, nil)),
	})
	handle, sub, baseline := startOutputTestRun(t, manager, "session-output-recovered")
	ctx := context.Background()
	backend.failNext.Store(true)
	if _, err := manager.HandleAgentEvent(ctx, handle, native.StreamEvent{Type: native.EventTextDelta, Delta: "kept"}); err != nil {
		t.Fatalf("handle text: %v", err)
	}
	committed := waitRuntimeEvent(t, sub.C, func(event Event) bool { return event.Type == EventRuntimeDelta })
	if committed.Seq != baseline.Seq+1 || len(committed.Delta.MessageAppends) != 1 || committed.Delta.MessageAppends[0].Content != "kept" {
		t.Fatalf("retried output = seq %d %#v, want the held append at %d", committed.Seq, committed.Delta.MessageAppends, baseline.Seq+1)
	}

	if _, err := manager.HandleAgentEvent(ctx, handle, native.StreamEvent{Type: native.EventTextDelta, Delta: " going"}); err != nil {
		t.Fatalf("append after the retry committed: %v", err)
	}
	if got := strings.Count(logs.String(), "commit run output failed"); got != 1 {
		t.Fatalf("failure records = %d, want 1\n%s", got, logs.String())
	}
}

type loadCountingBackend struct {
	*MemoryBackend
	loads atomic.Int32
}

func (b *loadCountingBackend) Load(ctx context.Context, key Key) (Snapshot, bool, error) {
	b.loads.Add(1)
	return b.MemoryBackend.Load(ctx, key)
}

func TestIdleSubscriptionReconcilesByCursorAlone(t *testing.T) {
	t.Parallel()

	backend := &loadCountingBackend{MemoryBackend: NewMemoryBackend()}
	manager := testRuntimeManager(t, backend, "owner-idle-subscription")
	const sessionID = "session-idle-subscription"
	ctx := context.Background()
	if err := manager.StartRun(ctx, testBotID, sessionID, testRunID, make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start run: %v", err)
	}
	handle := requireRunHandle(t, manager, testBotID, sessionID, testRunID)
	if _, err := manager.HandleAgentEvent(ctx, handle, native.StreamEvent{Type: native.EventTextDelta, Delta: "finished answer"}); err != nil {
		t.Fatalf("handle text: %v", err)
	}
	if _, err := manager.FinishRun(ctx, handle, RunStatusCompleted); err != nil {
		t.Fatalf("finish run: %v", err)
	}

	sub, err := manager.Subscribe(ctx, testBotID, sessionID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(sub.Close)
	baseline := waitRuntimeEvent(t, sub.C, func(event Event) bool { return event.Type == EventRuntimeSnapshot })
	if baseline.Snapshot.CurrentRunView == nil || len(baseline.Snapshot.CurrentRunView.Messages) == 0 {
		t.Fatalf("baseline = %#v, want the finished run's view", baseline.Snapshot.CurrentRunView)
	}
	loads := backend.loads.Load()
	time.Sleep(2*time.Second + 300*time.Millisecond)
	if got := backend.loads.Load(); got != loads {
		t.Fatalf("idle reconcile loaded the snapshot %d times, want none while its cursor stood still", got-loads)
	}

	// A write the subscriber never received a delta for still reaches it.
	moved, _, err := backend.Update(ctx, Key{BotID: testBotID, SessionID: sessionID}, func(snapshot Snapshot, _ bool) (Snapshot, bool, error) {
		snapshot.Seq++
		return snapshot, true, nil
	})
	if err != nil {
		t.Fatalf("move snapshot: %v", err)
	}
	recovered := waitRuntimeEvent(t, sub.C, func(event Event) bool { return event.Type == EventRuntimeSnapshot })
	if recovered.Seq != moved.Seq {
		t.Fatalf("reconciled snapshot seq = %d, want %d", recovered.Seq, moved.Seq)
	}
}

func runRuntimeBackendCursorContract(t *testing.T, suite runtimeBackendContractSuite) {
	t.Helper()

	backend := suite.newBackend(t)
	manager := testRuntimeManager(t, backend, "owner-cursor")
	const sessionID = "session-cursor"
	key := Key{BotID: testBotID, SessionID: sessionID}
	ctx := context.Background()
	requireStoredCursor := func(step string) {
		t.Helper()
		snapshot, ok, err := backend.Load(ctx, key)
		if err != nil || !ok {
			t.Fatalf("%s: load snapshot = ok:%v err:%v", step, ok, err)
		}
		cursor, ok, err := backend.LoadCursor(ctx, key)
		if err != nil || !ok || cursor != snapshot.cursor() {
			t.Fatalf("%s: stored cursor = %#v ok:%v err:%v, want %#v", step, cursor, ok, err, snapshot.cursor())
		}
	}
	if err := manager.StartRun(ctx, testBotID, sessionID, testRunID, make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start run: %v", err)
	}
	requireStoredCursor("start")
	handle := requireRunHandle(t, manager, testBotID, sessionID, testRunID)
	if _, err := manager.HandleAgentEvent(ctx, handle, native.StreamEvent{Type: native.EventTextDelta, Delta: "output"}); err != nil {
		t.Fatalf("handle text: %v", err)
	}
	commitHeldRunOutput(t, manager, handle)
	requireStoredCursor("output")
	if _, err := manager.FinishRun(ctx, handle, RunStatusCompleted); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	requireStoredCursor("finish")
}
