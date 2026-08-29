package sessionruntime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	chatview "github.com/felinics/memoh/internal/agent/view"
)

type afterFuncTrackingContext struct {
	context.Context
	done    chan struct{}
	stopped chan struct{}
	once    sync.Once
}

func (c *afterFuncTrackingContext) Done() <-chan struct{} {
	return c.done
}

func (c *afterFuncTrackingContext) AfterFunc(func()) func() bool {
	return func() bool {
		stopped := false
		c.once.Do(func() {
			stopped = true
			close(c.stopped)
		})
		return stopped
	}
}

func TestEnqueueRuntimeEventOverflowPublishesRecoveryMarker(t *testing.T) {
	ch := make(chan Event, 1)
	ch <- Event{Type: EventRuntimeDelta, BotID: "bot-1", SessionID: "session-1", Epoch: "epoch-1", Seq: 1, Delta: &RuntimeDelta{}}

	enqueueRuntimeEvent(ch, Event{Type: EventRuntimeDelta, BotID: "bot-1", SessionID: "session-1", Epoch: "epoch-1", Seq: 2, Delta: &RuntimeDelta{}})

	event := <-ch
	if event.Type != EventRuntimeDropped || event.Epoch != "epoch-1" || event.Seq != 2 {
		t.Fatalf("overflow event = %#v", event)
	}
}

func TestMemoryBackendRejectsUnserializableSnapshotWithoutCorruptingState(t *testing.T) {
	backend := NewMemoryBackend()
	key := Key{BotID: "bot-1", SessionID: "session-1"}
	if _, changed, err := backend.Update(context.Background(), key, func(Snapshot, bool) (Snapshot, bool, error) {
		return Snapshot{BotID: key.BotID, SessionID: key.SessionID, Seq: 1}, true, nil
	}); err != nil || !changed {
		t.Fatalf("seed snapshot: changed=%v err=%v", changed, err)
	}

	if _, changed, err := backend.Update(context.Background(), key, func(snapshot Snapshot, _ bool) (Snapshot, bool, error) {
		snapshot.Seq = 2
		snapshot.CurrentRunView = &CurrentRunView{
			RunID:    "run-1",
			Status:   RunStatusRunning,
			Messages: []chatview.UIMessage{{ID: 1, Type: chatview.UIMessageTool, Input: make(chan struct{})}},
		}
		return snapshot, true, nil
	}); err == nil || changed {
		t.Fatalf("unserializable update: changed=%v err=%v", changed, err)
	}

	snapshot, ok, err := backend.Load(context.Background(), key)
	if err != nil || !ok {
		t.Fatalf("load preserved snapshot: ok=%v err=%v", ok, err)
	}
	if snapshot.Seq != 1 || snapshot.CurrentRunView != nil {
		t.Fatalf("snapshot corrupted after rejected update: %#v", snapshot)
	}
}

func TestMemoryBackendRejectsCanceledOperations(t *testing.T) {
	backend := NewMemoryBackend()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	key := Key{BotID: "bot-1", SessionID: "session-1"}

	if _, _, err := backend.Load(ctx, key); !errors.Is(err, context.Canceled) {
		t.Fatalf("Load error = %v, want context.Canceled", err)
	}
	if _, _, err := backend.Update(ctx, key, func(snapshot Snapshot, _ bool) (Snapshot, bool, error) {
		return snapshot, true, nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Update error = %v, want context.Canceled", err)
	}
	if _, err := backend.Subscribe(ctx, key); !errors.Is(err, context.Canceled) {
		t.Fatalf("Subscribe error = %v, want context.Canceled", err)
	}
}

func TestMemoryBackendDoesNotImplementDistributedCoordination(t *testing.T) {
	t.Parallel()

	if _, ok := any(NewMemoryBackend()).(DistributedBackend); ok {
		t.Fatal("MemoryBackend unexpectedly implements DistributedBackend")
	}
}

func TestMemorySubscriptionCloseUnregistersWithLiveParentContext(t *testing.T) {
	t.Parallel()

	backend := NewMemoryBackend()
	key := Key{BotID: "bot-1", SessionID: "session-1"}
	sub, err := backend.Subscribe(context.Background(), key)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	sub.Close()

	backend.mu.Lock()
	remaining := len(backend.subscribers.subs[key.String()])
	backend.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("remaining subscribers = %d", remaining)
	}
	if _, ok := <-sub.C; ok {
		t.Fatal("subscription channel remained open after Close")
	}
}

func TestMemorySubscriptionConcurrentContextCancellation(t *testing.T) {
	t.Parallel()

	backend := NewMemoryBackend()
	key := Key{BotID: "bot-1", SessionID: "session-cancel"}
	for range 1000 {
		ctx, cancel := context.WithCancel(context.Background())
		cancelDone := make(chan struct{})
		go func() {
			cancel()
			close(cancelDone)
		}()
		sub, err := backend.Subscribe(ctx, key)
		if err == nil {
			sub.Close()
		}
		<-cancelDone
	}
}

func TestMemoryBackendCloseStopsSubscriptionContextWatch(t *testing.T) {
	t.Parallel()

	ctx := &afterFuncTrackingContext{Context: context.Background(), done: make(chan struct{}), stopped: make(chan struct{})}
	backend := NewMemoryBackend()
	sub, err := backend.Subscribe(ctx, Key{BotID: "bot-1", SessionID: "session-close"})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := backend.Close(); err != nil {
		t.Fatalf("close backend: %v", err)
	}
	select {
	case <-ctx.stopped:
	case <-time.After(time.Second):
		t.Fatal("backend Close did not stop the subscription context watch")
	}
	if _, ok := <-sub.C; ok {
		t.Fatal("subscription channel remained open after backend Close")
	}
	sub.Close()
}
