package inbound

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/command"
)

func TestDetectMode(t *testing.T) {
	tests := []struct {
		input    string
		wantMode InboundMode
		wantText string
	}{
		{"hello world", ModeInject, "hello world"},
		{"/btw hello", ModeInject, "hello"},
		{"/now hello", ModeParallel, "hello"},
		{"/next hello", ModeQueue, "hello"},
		{"/BTW hello", ModeInject, "hello"},
		{"/NOW hello", ModeParallel, "hello"},
		{"/NEXT hello", ModeQueue, "hello"},
		{"/now", ModeParallel, ""},
		{"/next", ModeQueue, ""},
		{"/btw", ModeInject, ""},
		{"  /now  hello  ", ModeParallel, "hello"},
		{"/unknown hello", ModeInject, "/unknown hello"},
		{"", ModeInject, ""},
		{"/new session", ModeInject, "/new session"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			mode, text := DetectMode(tt.input)
			if mode != tt.wantMode {
				t.Errorf("DetectMode(%q) mode = %d, want %d", tt.input, mode, tt.wantMode)
			}
			if text != tt.wantText {
				t.Errorf("DetectMode(%q) text = %q, want %q", tt.input, text, tt.wantText)
			}
		})
	}
}

func TestIsStartCommand(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"/start", true},
		{"/start@MemohBot", true},
		// Telegram deep links: /start <payload> (and /start@bot <payload>).
		{"/start abc123", true},
		{"/start@MemohBot abc123", true},
		{"/start deep_link_payload", true},
		{"/new", false},
		{"/started", false},
		{"start", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			invocation, err := command.ParseInvocation(command.InvocationInput{
				Text:       tt.input,
				BotAliases: []string{"MemohBot"},
			})
			got := err == nil && invocationHasResource(&invocation, "start")
			if got != tt.want {
				t.Errorf("start invocation for %q = %v, want %v (error: %v)", tt.input, got, tt.want, err)
			}
		})
	}
}

func TestIsModeCommand(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"/btw hello", true},
		{"/now hello", true},
		{"/next hello", true},
		{"/btw", true},
		{"/now", true},
		{"/next", true},
		{"/new", false},
		{"/fs list", false},
		{"hello", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := IsModeCommand(tt.input)
			if got != tt.want {
				t.Errorf("IsModeCommand(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestRouteDispatcher_InjectWhenActive(t *testing.T) {
	d := NewRouteDispatcher(slog.Default())
	routeID := "route-1"

	if d.IsActive(routeID) {
		t.Fatal("expected route to be inactive initially")
	}

	injectCh := d.MarkActive(routeID)
	if injectCh == nil {
		t.Fatal("expected non-nil inject channel")
	}
	if !d.IsActive(routeID) {
		t.Fatal("expected route to be active after MarkActive")
	}

	msg := InjectMessage{Text: "hello", HeaderifiedText: "[User] hello"}
	if !d.Inject(routeID, msg) {
		t.Fatal("expected inject to succeed when route is active")
	}

	select {
	case got := <-injectCh:
		if got.Text != "hello" {
			t.Errorf("got text %q, want %q", got.Text, "hello")
		}
	default:
		t.Fatal("expected message on inject channel")
	}
}

func TestRouteDispatcher_InjectWhenInactive(t *testing.T) {
	d := NewRouteDispatcher(slog.Default())
	routeID := "route-1"

	msg := InjectMessage{Text: "hello"}
	if d.Inject(routeID, msg) {
		t.Fatal("expected inject to fail when route is inactive")
	}
}

func TestRouteDispatcher_QueueAndDrain(t *testing.T) {
	d := NewRouteDispatcher(slog.Default())
	routeID := "route-1"

	d.MarkActive(routeID)

	d.Enqueue(routeID, QueuedTask{Text: "task-1"})
	d.Enqueue(routeID, QueuedTask{Text: "task-2"})

	result := d.MarkDone(routeID)
	if len(result.QueuedTasks) != 2 {
		t.Fatalf("expected 2 queued tasks, got %d", len(result.QueuedTasks))
	}
	if result.QueuedTasks[0].Text != "task-1" || result.QueuedTasks[1].Text != "task-2" {
		t.Errorf("unexpected task order: %v", result.QueuedTasks)
	}
	if d.IsActive(routeID) {
		t.Fatal("expected route to be inactive after MarkDone")
	}
}

func TestRouteDispatcher_OverlappingActiveOwners(t *testing.T) {
	d := NewRouteDispatcher(slog.Default())
	routeID := "route-1"

	d.MarkActive(routeID)
	d.MarkActive(routeID)
	d.Enqueue(routeID, QueuedTask{Text: "queued"})
	d.AddPendingPersist(routeID, func(context.Context) {})

	first := d.MarkDone(routeID)
	if len(first.QueuedTasks) != 0 {
		t.Fatalf("expected no queued tasks while an owner remains active, got %d", len(first.QueuedTasks))
	}
	if len(first.PendingPersists) != 0 {
		t.Fatalf("expected no pending persists while an owner remains active, got %d", len(first.PendingPersists))
	}
	if !d.IsActive(routeID) {
		t.Fatal("expected route to stay active until the last owner exits")
	}
	if !d.Inject(routeID, InjectMessage{Text: "still active"}) {
		t.Fatal("expected inject to remain available while an owner remains active")
	}

	second := d.MarkDone(routeID)
	if d.IsActive(routeID) {
		t.Fatal("expected route to be inactive after the last owner exits")
	}
	if len(second.QueuedTasks) != 1 || second.QueuedTasks[0].Text != "queued" {
		t.Fatalf("expected queued task on final release, got %v", second.QueuedTasks)
	}
	if len(second.PendingPersists) != 1 {
		t.Fatalf("expected pending persist on final release, got %d", len(second.PendingPersists))
	}
}

func TestRouteDispatcher_MarkDoneReturnsNilWhenEmpty(t *testing.T) {
	d := NewRouteDispatcher(slog.Default())
	routeID := "route-1"

	d.MarkActive(routeID)
	result := d.MarkDone(routeID)
	if result.QueuedTasks != nil {
		t.Fatalf("expected nil queued tasks, got %v", result.QueuedTasks)
	}
	if result.PendingPersists != nil {
		t.Fatalf("expected nil pending persists, got %v", result.PendingPersists)
	}
}

func TestRouteDispatcher_ConcurrentInject(t *testing.T) {
	d := NewRouteDispatcher(slog.Default())
	routeID := "route-1"

	injectCh := d.MarkActive(routeID)

	const numMessages = 10
	var wg sync.WaitGroup
	wg.Add(numMessages)
	for i := 0; i < numMessages; i++ {
		go func() {
			defer wg.Done()
			d.Inject(routeID, InjectMessage{Text: "msg"})
		}()
	}
	wg.Wait()

	count := 0
	for {
		select {
		case <-injectCh:
			count++
		default:
			goto done
		}
	}
done:
	if count != numMessages {
		t.Errorf("expected %d messages, got %d", numMessages, count)
	}
}

func TestRouteDispatcher_ParallelBypass(t *testing.T) {
	d := NewRouteDispatcher(slog.Default())
	routeID := "route-1"

	d.MarkActive(routeID)

	// In parallel mode, the caller does not interact with the dispatcher
	// at all — it just starts a new stream directly. Verify the route
	// stays active without interference.
	if !d.IsActive(routeID) {
		t.Fatal("expected route to still be active")
	}

	d.MarkDone(routeID)
	if d.IsActive(routeID) {
		t.Fatal("expected route to be inactive after MarkDone")
	}
}

func TestRouteDispatcher_Cleanup(t *testing.T) {
	d := NewRouteDispatcher(slog.Default())

	d.MarkActive("route-1")
	d.MarkDone("route-1")

	d.MarkActive("route-2")

	d.mu.RLock()
	initialCount := len(d.routes)
	d.mu.RUnlock()
	if initialCount != 2 {
		t.Fatalf("expected 2 routes, got %d", initialCount)
	}

	d.Cleanup(0)

	d.mu.RLock()
	afterCount := len(d.routes)
	d.mu.RUnlock()

	// route-1 is idle → cleaned up; route-2 is active → kept
	if afterCount != 1 {
		t.Fatalf("expected 1 route after cleanup, got %d", afterCount)
	}
	if d.IsActive("route-2") != true {
		t.Fatal("expected route-2 to still be active")
	}
}

func TestRouteDispatcher_InjectChannelFull(t *testing.T) {
	d := NewRouteDispatcher(slog.Default())
	routeID := "route-1"

	d.MarkActive(routeID)

	// Fill the inject channel to capacity
	for i := 0; i < injectChBuffer; i++ {
		if !d.Inject(routeID, InjectMessage{Text: "fill"}) {
			t.Fatalf("expected inject %d to succeed", i)
		}
	}

	// Next inject should fail (channel full)
	if d.Inject(routeID, InjectMessage{Text: "overflow"}) {
		t.Fatal("expected inject to fail when channel is full")
	}
}

func TestRouteDispatcher_QueueWhenInactive(t *testing.T) {
	d := NewRouteDispatcher(slog.Default())
	routeID := "route-1"

	// Enqueue without marking active — still stores in queue
	d.Enqueue(routeID, QueuedTask{Text: "task-1"})

	// MarkActive then MarkDone should return the queued task
	d.MarkActive(routeID)
	result := d.MarkDone(routeID)
	if len(result.QueuedTasks) != 1 {
		t.Fatalf("expected 1 queued task, got %d", len(result.QueuedTasks))
	}
}

func TestRouteDispatcher_MultipleMarkActive(t *testing.T) {
	d := NewRouteDispatcher(slog.Default())
	routeID := "route-1"

	ch1 := d.MarkActive(routeID)
	ch2 := d.MarkActive(routeID)

	if ch1 == nil || ch2 == nil {
		t.Fatal("expected non-nil channels")
	}

	_ = time.Now()
}

func TestRouteDispatcher_PendingPersistOrder(t *testing.T) {
	d := NewRouteDispatcher(slog.Default())
	routeID := "route-1"

	d.MarkActive(routeID)

	var order []string
	d.AddPendingPersist(routeID, func(_ context.Context) {
		order = append(order, "B")
	})
	d.AddPendingPersist(routeID, func(_ context.Context) {
		order = append(order, "C")
	})

	result := d.MarkDone(routeID)
	if len(result.PendingPersists) != 2 {
		t.Fatalf("expected 2 pending persists, got %d", len(result.PendingPersists))
	}

	// Execute persists — they should run in insertion order (B then C)
	for _, fn := range result.PendingPersists {
		fn(context.Background())
	}
	if len(order) != 2 || order[0] != "B" || order[1] != "C" {
		t.Errorf("expected [B C], got %v", order)
	}
}
