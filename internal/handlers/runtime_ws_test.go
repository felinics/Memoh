package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
)

const (
	runtimeWSBotID     = "bot-runtime-ws"
	runtimeWSSessionID = "session-runtime-ws"
)

// fakeRuntimeSource hands out subscriptions a test drives by hand. It records
// every subscribe so a test can assert that a replaced or unsubscribed entry
// was actually closed rather than merely dropped from the map.
type fakeRuntimeSource struct {
	mu      sync.Mutex
	err     error
	handles []*fakeRuntimeSubscription
}

type fakeRuntimeSubscription struct {
	botID     string
	sessionID string
	ch        chan sessionruntime.Event
	closeOnce sync.Once
	closed    chan struct{}
}

func (f *fakeRuntimeSubscription) emit(t *testing.T, event sessionruntime.Event) {
	t.Helper()
	select {
	case f.ch <- event:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime event was not consumed")
	}
}

func (f *fakeRuntimeSubscription) isClosed() bool {
	select {
	case <-f.closed:
		return true
	default:
		return false
	}
}

func (f *fakeRuntimeSource) Subscribe(_ context.Context, botID, sessionID string) (sessionruntime.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return sessionruntime.Subscription{}, f.err
	}
	handle := &fakeRuntimeSubscription{
		botID:     botID,
		sessionID: sessionID,
		ch:        make(chan sessionruntime.Event, 8),
		closed:    make(chan struct{}),
	}
	f.handles = append(f.handles, handle)
	return sessionruntime.Subscription{
		C: handle.ch,
		Close: func() {
			handle.closeOnce.Do(func() {
				close(handle.closed)
				close(handle.ch)
			})
		},
	}, nil
}

func (f *fakeRuntimeSource) subscription(t *testing.T, index int) *fakeRuntimeSubscription {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if index >= len(f.handles) {
		t.Fatalf("subscription %d does not exist; got %d", index, len(f.handles))
	}
	return f.handles[index]
}

func (f *fakeRuntimeSource) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.handles)
}

// runtimeWSHarness collects the frames a subscription writes, without a socket:
// wsWriter's queue is the boundary the forwarder actually writes to.
type runtimeWSHarness struct {
	source        *fakeRuntimeSource
	subscriptions *runtimeSubscriptions
	writer        *wsWriter
}

func newRuntimeWSHarness(t *testing.T) *runtimeWSHarness {
	t.Helper()
	source := &fakeRuntimeSource{}
	writer := &wsWriter{ch: make(chan []byte, 64), stop: make(chan struct{}), done: make(chan struct{})}
	return &runtimeWSHarness{
		source:        source,
		subscriptions: newRuntimeSubscriptions(source, writer, slog.Default()),
		writer:        writer,
	}
}

func (h *runtimeWSHarness) subscribe(sessionID string, authorize runtimeSubscribeAuthorizer) bool {
	return h.subscriptions.handle(context.Background(), runtimeWSBotID, runtimeClientMessage{
		Type:      runtimeSubscribeMessageType,
		SessionID: sessionID,
	}, authorize)
}

func (h *runtimeWSHarness) unsubscribe(sessionID string) bool {
	return h.subscriptions.handle(context.Background(), runtimeWSBotID, runtimeClientMessage{
		Type:      runtimeUnsubscribeMessageType,
		SessionID: sessionID,
	}, nil)
}

// next returns the next frame written, or fails. Frames are read off the
// writer queue in order, which is the ordering a client observes.
func (h *runtimeWSHarness) next(t *testing.T) map[string]any {
	t.Helper()
	select {
	case data := <-h.writer.ch:
		var frame map[string]any
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("unmarshal runtime frame: %v", err)
		}
		return frame
	case <-time.After(2 * time.Second):
		t.Fatal("no runtime frame was written")
		return nil
	}
}

func (h *runtimeWSHarness) expectNothing(t *testing.T) {
	t.Helper()
	select {
	case data := <-h.writer.ch:
		t.Fatalf("unexpected runtime frame: %s", data)
	case <-time.After(100 * time.Millisecond):
	}
}

func runtimeSnapshotEvent(sessionID string, seq int64) sessionruntime.Event {
	snapshot := sessionruntime.Snapshot{
		BotID:     runtimeWSBotID,
		SessionID: sessionID,
		Epoch:     "epoch-1",
		Seq:       seq,
		CurrentRunView: &sessionruntime.CurrentRunView{
			RunID:  "run-1",
			Status: sessionruntime.RunStatusRunning,
		},
	}
	return sessionruntime.Event{
		Type:      sessionruntime.EventRuntimeSnapshot,
		BotID:     runtimeWSBotID,
		SessionID: sessionID,
		Epoch:     "epoch-1",
		Seq:       seq,
		Snapshot:  &snapshot,
	}
}

func runtimeDeltaEvent(sessionID string, seq int64) sessionruntime.Event {
	status := sessionruntime.RunStatusRunning
	return sessionruntime.Event{
		Type:      sessionruntime.EventRuntimeDelta,
		BotID:     runtimeWSBotID,
		SessionID: sessionID,
		Epoch:     "epoch-1",
		Seq:       seq,
		Delta: &sessionruntime.RuntimeDelta{
			Run: &sessionruntime.CurrentRunPatch{RunID: "run-1", Status: &status},
		},
	}
}

// A subscriber's first frame must be the authoritative snapshot: a delta
// applied to no baseline is not interpretable, so ordering here is the whole
// contract rather than a nicety (SR-OBS-001).
func TestRuntimeSubscribeSendsSnapshotBeforeDelta(t *testing.T) {
	t.Parallel()

	harness := newRuntimeWSHarness(t)
	defer harness.subscriptions.close()

	if !harness.subscribe(runtimeWSSessionID, nil) {
		t.Fatal("runtime_subscribe was not handled")
	}
	subscription := harness.source.subscription(t, 0)
	subscription.emit(t, runtimeSnapshotEvent(runtimeWSSessionID, 1))
	subscription.emit(t, runtimeDeltaEvent(runtimeWSSessionID, 2))

	snapshot := harness.next(t)
	if snapshot["type"] != sessionruntime.EventRuntimeSnapshot {
		t.Fatalf("first frame = %#v, want runtime_snapshot", snapshot)
	}
	// Everything a client dedupes or recovers on is top level, so it can order
	// a frame without decoding the payload.
	if snapshot["session_id"] != runtimeWSSessionID || snapshot["epoch"] != "epoch-1" || snapshot["seq"] != float64(1) {
		t.Fatalf("snapshot frame = %#v, want top-level session/epoch/seq", snapshot)
	}
	if _, present := snapshot["snapshot"]; !present {
		t.Fatalf("snapshot frame carries no snapshot: %#v", snapshot)
	}
	if _, present := snapshot["stream_id"]; present {
		t.Fatalf("frame reintroduced stream_id: %#v", snapshot)
	}

	delta := harness.next(t)
	if delta["type"] != sessionruntime.EventRuntimeDelta || delta["seq"] != float64(2) {
		t.Fatalf("second frame = %#v, want runtime_delta at seq 2", delta)
	}
	if _, present := delta["delta"]; !present {
		t.Fatalf("delta frame carries no delta: %#v", delta)
	}
}

// Overflow is reported rather than absorbed: the client is told to re-fetch a
// snapshot, which is what keeps a slow consumer from silently diverging.
func TestRuntimeSubscribeForwardsDroppedAsRecoverySignal(t *testing.T) {
	t.Parallel()

	harness := newRuntimeWSHarness(t)
	defer harness.subscriptions.close()

	if !harness.subscribe(runtimeWSSessionID, nil) {
		t.Fatal("runtime_subscribe was not handled")
	}
	harness.source.subscription(t, 0).emit(t, sessionruntime.Event{
		Type:      sessionruntime.EventRuntimeDropped,
		BotID:     runtimeWSBotID,
		SessionID: runtimeWSSessionID,
		Epoch:     "epoch-1",
		Seq:       7,
		Message:   "runtime subscriber buffer overflow",
	})

	dropped := harness.next(t)
	if dropped["type"] != sessionruntime.EventRuntimeDropped {
		t.Fatalf("frame = %#v, want runtime_dropped", dropped)
	}
	if dropped["session_id"] != runtimeWSSessionID || dropped["epoch"] != "epoch-1" || dropped["seq"] != float64(7) {
		t.Fatalf("dropped frame = %#v, want the position the client should recover from", dropped)
	}
	if dropped["message"] != "runtime subscriber buffer overflow" {
		t.Fatalf("dropped frame message = %#v", dropped["message"])
	}
}

// A repeated subscribe replaces atomically. Two live forwarders on one session
// would interleave frames for the same session on one socket, so the old one
// must be closed rather than left running.
func TestRuntimeSubscribeReplacesExistingSubscriptionForSameSession(t *testing.T) {
	t.Parallel()

	harness := newRuntimeWSHarness(t)
	defer harness.subscriptions.close()

	if !harness.subscribe(runtimeWSSessionID, nil) {
		t.Fatal("first runtime_subscribe was not handled")
	}
	if !harness.subscribe(runtimeWSSessionID, nil) {
		t.Fatal("second runtime_subscribe was not handled")
	}
	if harness.source.count() != 2 {
		t.Fatalf("subscriptions created = %d, want 2", harness.source.count())
	}

	first := harness.source.subscription(t, 0)
	waitFor(t, "replaced subscription to close", first.isClosed)

	harness.subscriptions.mu.Lock()
	live := len(harness.subscriptions.active)
	harness.subscriptions.mu.Unlock()
	if live != 1 {
		t.Fatalf("live subscriptions = %d, want the replacement only", live)
	}

	// The replacement is the one still delivering.
	second := harness.source.subscription(t, 1)
	if second.isClosed() {
		t.Fatal("replacement subscription was closed")
	}
	second.emit(t, runtimeSnapshotEvent(runtimeWSSessionID, 1))
	if frame := harness.next(t); frame["type"] != sessionruntime.EventRuntimeSnapshot {
		t.Fatalf("replacement did not deliver: %#v", frame)
	}
}

func TestRuntimeUnsubscribeClosesSubscription(t *testing.T) {
	t.Parallel()

	harness := newRuntimeWSHarness(t)
	defer harness.subscriptions.close()

	if !harness.subscribe(runtimeWSSessionID, nil) {
		t.Fatal("runtime_subscribe was not handled")
	}
	if !harness.unsubscribe(runtimeWSSessionID) {
		t.Fatal("runtime_unsubscribe was not handled")
	}

	subscription := harness.source.subscription(t, 0)
	if !subscription.isClosed() {
		t.Fatal("runtime_unsubscribe left the subscription open")
	}
	harness.subscriptions.mu.Lock()
	live := len(harness.subscriptions.active)
	harness.subscriptions.mu.Unlock()
	if live != 0 {
		t.Fatalf("live subscriptions after unsubscribe = %d, want 0", live)
	}
	// An unsubscribe the client asked for is not a gap, so it gets no
	// recovery signal it would act on by re-subscribing.
	harness.expectNothing(t)
}

// Disconnect closes every subscription and leaks no forwarder. The runs behind
// them keep executing — they belong to the session, not the socket.
func TestRuntimeSubscriptionsCloseOnConnectionTeardown(t *testing.T) {
	t.Parallel()

	harness := newRuntimeWSHarness(t)
	before := runtime.NumGoroutine()

	sessions := []string{"session-a", "session-b", "session-c"}
	for _, sessionID := range sessions {
		if !harness.subscribe(sessionID, nil) {
			t.Fatalf("runtime_subscribe %s was not handled", sessionID)
		}
	}
	if harness.source.count() != len(sessions) {
		t.Fatalf("subscriptions = %d, want %d", harness.source.count(), len(sessions))
	}

	harness.subscriptions.close()

	for i := range sessions {
		if !harness.source.subscription(t, i).isClosed() {
			t.Fatalf("subscription %d survived connection teardown", i)
		}
	}
	waitFor(t, "forwarder goroutines to exit", func() bool {
		return runtime.NumGoroutine() <= before+1
	})
}

// One connection observes many sessions at once, and the frames stay routable:
// each carries the session it belongs to, so a client demultiplexes on
// session_id rather than on arrival order.
func TestRuntimeSubscribeSupportsMultipleSessionsPerConnection(t *testing.T) {
	t.Parallel()

	harness := newRuntimeWSHarness(t)
	defer harness.subscriptions.close()

	if !harness.subscribe("session-a", nil) || !harness.subscribe("session-b", nil) {
		t.Fatal("multi-session runtime_subscribe was not handled")
	}
	harness.source.subscription(t, 0).emit(t, runtimeSnapshotEvent("session-a", 1))
	harness.source.subscription(t, 1).emit(t, runtimeSnapshotEvent("session-b", 4))

	seen := map[string]float64{}
	for range 2 {
		frame := harness.next(t)
		sessionID, _ := frame["session_id"].(string)
		seq, _ := frame["seq"].(float64)
		seen[sessionID] = seq
	}
	if seen["session-a"] != 1 || seen["session-b"] != 4 {
		t.Fatalf("multi-session frames = %#v, want each session at its own position", seen)
	}

	// Closing one session leaves the other observing.
	if !harness.unsubscribe("session-a") {
		t.Fatal("runtime_unsubscribe was not handled")
	}
	if harness.source.subscription(t, 1).isClosed() {
		t.Fatal("unsubscribing one session closed another")
	}
}

// Authorization is re-checked on every subscribe, not inherited from the
// handshake: session_id alone must never be enough to observe a session.
func TestRuntimeSubscribeReauthorizesEverySubscribe(t *testing.T) {
	t.Parallel()

	harness := newRuntimeWSHarness(t)
	defer harness.subscriptions.close()

	var calls int
	authorize := func(_ context.Context, _ string) error {
		calls++
		if calls == 1 {
			return nil
		}
		return errors.New("session not found")
	}

	if !harness.subscribe(runtimeWSSessionID, authorize) {
		t.Fatal("first runtime_subscribe was not handled")
	}
	if harness.source.count() != 1 {
		t.Fatalf("authorized subscribe created %d subscriptions, want 1", harness.source.count())
	}

	// Access revoked between subscribes: the second is refused even though the
	// connection is the same one that was allowed a moment ago.
	if !harness.subscribe(runtimeWSSessionID, authorize) {
		t.Fatal("second runtime_subscribe was not handled")
	}
	if calls != 2 {
		t.Fatalf("authorizer calls = %d, want one per subscribe", calls)
	}
	if harness.source.count() != 1 {
		t.Fatalf("unauthorized subscribe created a subscription: %d", harness.source.count())
	}
	if harness.source.subscription(t, 0).isClosed() {
		t.Fatal("a refused subscribe closed the caller's existing authorized subscription")
	}

	frame := harness.next(t)
	if frame["type"] != "error" || !strings.Contains(frame["message"].(string), "session not found") {
		t.Fatalf("refused subscribe frame = %#v, want an error naming the refusal", frame)
	}
}

func TestRuntimeSubscribeRequiresSessionID(t *testing.T) {
	t.Parallel()

	harness := newRuntimeWSHarness(t)
	defer harness.subscriptions.close()

	if !harness.subscribe("   ", nil) {
		t.Fatal("runtime_subscribe was not handled")
	}
	if harness.source.count() != 0 {
		t.Fatal("runtime_subscribe without a session created a subscription")
	}
	if frame := harness.next(t); frame["type"] != "error" {
		t.Fatalf("frame = %#v, want an error", frame)
	}
}

// handle owns exactly two message types; everything else falls through to the
// turn-starting path in the connection loop.
func TestRuntimeSubscriptionsIgnoreUnrelatedMessages(t *testing.T) {
	t.Parallel()

	harness := newRuntimeWSHarness(t)
	defer harness.subscriptions.close()

	for _, messageType := range []string{"message", "abort", "retry_message", ""} {
		handled := harness.subscriptions.handle(context.Background(), runtimeWSBotID, runtimeClientMessage{
			Type:      messageType,
			SessionID: runtimeWSSessionID,
		}, nil)
		if handled {
			t.Fatalf("subscription handler claimed %q", messageType)
		}
	}
	if harness.source.count() != 0 {
		t.Fatal("unrelated messages created a subscription")
	}
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
