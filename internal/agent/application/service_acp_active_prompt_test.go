package application

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/native"
)

func TestACPActivePromptHubDoesNotDropQueuedEvents(t *testing.T) {
	t.Parallel()

	hub := newACPActivePromptHub()
	sub, ok := hub.subscribe()
	if !ok {
		t.Fatal("subscribe failed")
	}
	defer sub.release()

	const eventCount = 300
	for i := 0; i < eventCount; i++ {
		hub.emit(native.StreamEvent{Type: native.EventTextDelta, Delta: strconv.Itoa(i)})
	}
	hub.emit(native.StreamEvent{Type: native.EventEnd})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for i := 0; i < eventCount; i++ {
		ev, ok, err := sub.sub.next(ctx)
		if err != nil {
			t.Fatalf("next event %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("subscription closed at event %d", i)
		}
		if ev.Type != native.EventTextDelta || ev.Delta != strconv.Itoa(i) {
			t.Fatalf("event %d = %#v", i, ev)
		}
	}
	ev, ok, err := sub.sub.next(ctx)
	if err != nil {
		t.Fatalf("next terminal event: %v", err)
	}
	if !ok || ev.Type != native.EventEnd {
		t.Fatalf("terminal event = %#v, ok=%v", ev, ok)
	}
}

func TestForwardACPActivePromptSkipsOnlyMatchingDecisionProjection(t *testing.T) {
	t.Parallel()

	hub := newACPActivePromptHub()
	sub, ok := hub.subscribe()
	if !ok {
		t.Fatal("subscribe failed")
	}

	eventCh := make(chan WSStreamEvent, 4)
	done := make(chan error, 1)
	go func() {
		done <- forwardACPActivePrompt(context.Background(), sub, eventCh, acpActivePromptForwardOptions{
			SkipToolCallID:  "call-1",
			SkipUserInputID: "input-1",
		})
	}()

	hub.emit(native.StreamEvent{
		Type:        native.EventUserInputRequest,
		ToolCallID:  "call-1",
		UserInputID: "input-1",
		Status:      "submitted",
	})
	hub.emit(native.StreamEvent{
		Type:       native.EventToolCallEnd,
		ToolCallID: "call-1",
		ToolName:   "ask_user",
		Result:     map[string]any{"status": "submitted"},
	})
	hub.emit(native.StreamEvent{
		Type:        native.EventUserInputRequest,
		ToolCallID:  "call-2",
		UserInputID: "input-2",
		Status:      "pending",
	})
	hub.emit(native.StreamEvent{Type: native.EventEnd})

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("forward active prompt: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for active prompt forwarding")
	}

	events := make([]native.StreamEvent, 0, 3)
	for i := 0; i < 3; i++ {
		select {
		case raw := <-eventCh:
			var ev native.StreamEvent
			if err := json.Unmarshal(raw, &ev); err != nil {
				t.Fatalf("unmarshal event %d: %v", i, err)
			}
			events = append(events, ev)
		default:
			t.Fatalf("missing event %d", i)
		}
	}
	if events[0].Type != native.EventStart {
		t.Fatalf("first event = %#v, want start", events[0])
	}
	if events[1].Type != native.EventUserInputRequest || events[1].ToolCallID != "call-2" {
		t.Fatalf("second event = %#v, want later user input request", events[1])
	}
	if events[2].Type != native.EventEnd {
		t.Fatalf("third event = %#v, want end", events[2])
	}
	if len(eventCh) != 0 {
		t.Fatalf("unexpected extra events = %d", len(eventCh))
	}
}
