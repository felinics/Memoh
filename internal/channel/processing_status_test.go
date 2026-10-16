package channel_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/channel"
)

type fakeStatusNotifier struct {
	mu        sync.Mutex
	events    []string
	handle    channel.ProcessingStatusHandle
	seen      []channel.ProcessingStatusHandle
	cause     error
	refreshes atomic.Int32
	// inFlight is set while a refresh call is executing.
	inFlight   atomic.Bool
	refreshErr error
	// refreshDelay holds each refresh open, to observe StopRefresh waiting.
	refreshDelay time.Duration
}

func (n *fakeStatusNotifier) record(event string, handle channel.ProcessingStatusHandle) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = append(n.events, event)
	n.seen = append(n.seen, handle)
}

func (n *fakeStatusNotifier) snapshot() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.events...)
}

func (n *fakeStatusNotifier) ProcessingStarted(context.Context, channel.ChannelConfig, channel.InboundMessage, channel.ProcessingStatusInfo) (channel.ProcessingStatusHandle, error) {
	n.record("started", channel.ProcessingStatusHandle{})
	return n.handle, nil
}

func (n *fakeStatusNotifier) ProcessingCompleted(_ context.Context, _ channel.ChannelConfig, _ channel.InboundMessage, _ channel.ProcessingStatusInfo, handle channel.ProcessingStatusHandle) error {
	n.record("completed", handle)
	return nil
}

func (n *fakeStatusNotifier) ProcessingFailed(_ context.Context, _ channel.ChannelConfig, _ channel.InboundMessage, _ channel.ProcessingStatusInfo, handle channel.ProcessingStatusHandle, cause error) error {
	n.record("failed", handle)
	n.mu.Lock()
	n.cause = cause
	n.mu.Unlock()
	return nil
}

type fakeRefreshingNotifier struct {
	*fakeStatusNotifier
	interval time.Duration
}

func (n fakeRefreshingNotifier) ProcessingRefreshInterval() time.Duration { return n.interval }

func (n fakeRefreshingNotifier) ProcessingRefresh(_ context.Context, _ channel.ChannelConfig, _ channel.InboundMessage, _ channel.ProcessingStatusInfo, handle channel.ProcessingStatusHandle) error {
	n.inFlight.Store(true)
	defer n.inFlight.Store(false)
	if n.refreshDelay > 0 {
		time.Sleep(n.refreshDelay)
	}
	n.refreshes.Add(1)
	n.record("refresh", handle)
	return n.refreshErr
}

func startTestStatus(t *testing.T, notifier channel.ProcessingStatusNotifier, onError channel.ProcessingStatusErrorFunc) *channel.ProcessingStatus {
	t.Helper()
	return channel.StartProcessingStatus(context.Background(), notifier, channel.ChannelConfig{ID: "cfg"}, channel.InboundMessage{}, channel.ProcessingStatusInfo{ReplyTarget: "chat"}, onError)
}

func waitForRefreshes(t *testing.T, n *fakeStatusNotifier, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for n.refreshes.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("refreshes = %d, want at least %d", n.refreshes.Load(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestProcessingStatusRefreshesUntilComplete(t *testing.T) {
	base := &fakeStatusNotifier{handle: channel.ProcessingStatusHandle{Token: "tok"}}
	status := startTestStatus(t, fakeRefreshingNotifier{fakeStatusNotifier: base, interval: 5 * time.Millisecond}, nil)

	waitForRefreshes(t, base, 3)
	status.Complete(context.Background())
	after := base.refreshes.Load()
	time.Sleep(30 * time.Millisecond)
	if got := base.refreshes.Load(); got != after {
		t.Fatalf("refreshes continued after Complete: %d -> %d", after, got)
	}
	events := base.snapshot()
	if events[0] != "started" || events[len(events)-1] != "completed" {
		t.Fatalf("events = %v, want started ... completed", events)
	}
	for i, handle := range base.seen[1:] {
		if handle.Token != "tok" {
			t.Fatalf("call %d saw handle %q, want the Started handle", i+1, handle.Token)
		}
	}
}

func TestProcessingStatusStopRefreshWaitsForInFlightRefresh(t *testing.T) {
	base := &fakeStatusNotifier{refreshDelay: 20 * time.Millisecond}
	status := startTestStatus(t, fakeRefreshingNotifier{fakeStatusNotifier: base, interval: time.Millisecond}, nil)

	deadline := time.Now().Add(2 * time.Second)
	for !base.inFlight.Load() {
		if time.Now().After(deadline) {
			t.Fatal("refresh never started")
		}
		time.Sleep(100 * time.Microsecond)
	}
	status.StopRefresh()
	if base.inFlight.Load() {
		t.Fatal("StopRefresh returned while a refresh was still in flight")
	}
	stopped := base.refreshes.Load()
	time.Sleep(30 * time.Millisecond)
	if got := base.refreshes.Load(); got != stopped {
		t.Fatalf("refresh fired after StopRefresh returned: %d -> %d", stopped, got)
	}
	// StopRefresh alone does not end the session.
	if events := base.snapshot(); events[len(events)-1] == "completed" || events[len(events)-1] == "failed" {
		t.Fatalf("StopRefresh reported a terminal status: %v", events)
	}
	status.Complete(context.Background())
	if events := base.snapshot(); events[len(events)-1] != "completed" {
		t.Fatalf("events = %v, want completed last", events)
	}
}

func TestProcessingStatusCompleteAndFailAreIdempotent(t *testing.T) {
	base := &fakeStatusNotifier{}
	status := startTestStatus(t, base, nil)
	cause := errors.New("boom")

	status.Fail(context.Background(), cause)
	status.Fail(context.Background(), errors.New("second"))
	status.Complete(context.Background())
	status.StopRefresh()

	events := base.snapshot()
	if len(events) != 2 || events[0] != "started" || events[1] != "failed" {
		t.Fatalf("events = %v, want [started failed]", events)
	}
	if !errors.Is(base.cause, cause) {
		t.Fatalf("failed cause = %v, want %v", base.cause, cause)
	}
}

func TestProcessingStatusReportsFirstRefreshErrorOnly(t *testing.T) {
	base := &fakeStatusNotifier{refreshErr: errors.New("chat gone")}
	var mu sync.Mutex
	var stages []string
	status := startTestStatus(t, fakeRefreshingNotifier{fakeStatusNotifier: base, interval: time.Millisecond}, func(_ context.Context, stage string, _ error) {
		mu.Lock()
		stages = append(stages, stage)
		mu.Unlock()
	})
	waitForRefreshes(t, base, 3)
	status.Complete(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if len(stages) != 1 || stages[0] != channel.ProcessingStageRefresh {
		t.Fatalf("reported stages = %v, want one %q", stages, channel.ProcessingStageRefresh)
	}
}

func TestProcessingStatusStopsRefreshWhenContextEnds(t *testing.T) {
	base := &fakeStatusNotifier{}
	ctx, cancel := context.WithCancel(context.Background())
	status := channel.StartProcessingStatus(ctx, fakeRefreshingNotifier{fakeStatusNotifier: base, interval: time.Millisecond}, channel.ChannelConfig{}, channel.InboundMessage{}, channel.ProcessingStatusInfo{}, nil)
	waitForRefreshes(t, base, 1)
	cancel()
	// Returns once the loop observed the cancellation.
	status.StopRefresh()
	stopped := base.refreshes.Load()
	time.Sleep(20 * time.Millisecond)
	if got := base.refreshes.Load(); got != stopped {
		t.Fatalf("refresh fired after context ended: %d -> %d", stopped, got)
	}
}

func TestProcessingStatusNilNotifierIsInert(t *testing.T) {
	status := startTestStatus(t, nil, func(context.Context, string, error) {
		t.Fatal("inert status reported an error")
	})
	status.StopRefresh()
	status.Complete(context.Background())
	status.Fail(context.Background(), errors.New("ignored"))

	var nilStatus *channel.ProcessingStatus
	nilStatus.StopRefresh()
	nilStatus.Complete(context.Background())
	nilStatus.Fail(context.Background(), errors.New("ignored"))
}

func TestProcessingStatusPauseHoldsRefreshUntilResume(t *testing.T) {
	base := &fakeStatusNotifier{}
	status := startTestStatus(t, fakeRefreshingNotifier{fakeStatusNotifier: base, interval: 5 * time.Millisecond}, nil)
	defer status.Complete(context.Background())

	waitForRefreshes(t, base, 1)
	status.PauseRefresh()
	// A tick already past the pause check may still land; after that, none.
	time.Sleep(15 * time.Millisecond)
	paused := base.refreshes.Load()
	time.Sleep(30 * time.Millisecond)
	if got := base.refreshes.Load(); got != paused {
		t.Fatalf("refreshes continued while paused: %d -> %d", paused, got)
	}

	status.ResumeRefresh()
	waitForRefreshes(t, base, paused+2)
}

func TestProcessingStatusResumeRefreshesImmediately(t *testing.T) {
	base := &fakeStatusNotifier{}
	status := startTestStatus(t, fakeRefreshingNotifier{fakeStatusNotifier: base, interval: time.Hour}, nil)
	defer status.Complete(context.Background())

	status.ResumeRefresh() // not paused: no-op
	status.PauseRefresh()
	status.ResumeRefresh()
	waitForRefreshes(t, base, 1)
	time.Sleep(20 * time.Millisecond)
	if got := base.refreshes.Load(); got != 1 {
		t.Fatalf("refreshes = %d, want exactly the one sent on resume", got)
	}
}
