package tools

import (
	"context"
	"testing"
	"time"
)

func TestRunSpawnHeartbeatStopsWithParentWithoutAffectingChildContext(t *testing.T) {
	parentCtx, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()
	childCtx, childCancel := context.WithCancel(context.WithoutCancel(parentCtx))
	defer childCancel()

	ticks := make(chan time.Time, 2)
	events := make(chan ToolStreamEvent, 1)
	done := make(chan struct{})
	go func() {
		runSpawnHeartbeat(parentCtx, func(event ToolStreamEvent) {
			events <- event
		}, ticks)
		close(done)
	}()

	ticks <- time.Now()
	select {
	case event := <-events:
		if event.Type != StreamEventSpawnHeartbeat {
			t.Fatalf("event type = %q, want spawn heartbeat", event.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not emit for controlled tick")
	}

	parentCancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not stop after parent cancellation")
	}
	if childCtx.Err() != nil {
		t.Fatalf("child context err = %v, want child to remain active", childCtx.Err())
	}

	ticks <- time.Now()
	if len(events) != 0 {
		t.Fatalf("late heartbeat events = %d, want no event", len(events))
	}
}
