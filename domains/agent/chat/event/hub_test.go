package event

import (
	"testing"
	"time"
)

func TestHubPublishScopedByBotID(t *testing.T) {
	hub := NewHub()
	subA, cancelA := hub.Subscribe("bot-a", 8)
	defer cancelA()
	subB, cancelB := hub.Subscribe("bot-b", 8)
	defer cancelB()

	hub.Publish(Event{Type: EventTypeMessageCreated, BotID: "bot-a"})

	select {
	case <-subA.Events:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("expected event for bot-a subscriber")
	}

	select {
	case <-subB.Events:
		t.Fatalf("did not expect bot-b subscriber to receive bot-a event")
	case <-time.After(120 * time.Millisecond):
	}
}

func TestHubCancelUnsubscribe(t *testing.T) {
	hub := NewHub()
	sub, cancel := hub.Subscribe("bot-a", 8)
	cancel()

	select {
	case _, ok := <-sub.Events:
		if ok {
			t.Fatalf("expected stream to be closed after cancel")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("timed out waiting for stream close")
	}
}

func TestHubSlowSubscriberDoesNotBlockPublish(t *testing.T) {
	hub := NewHub()
	sub, cancel := hub.Subscribe("bot-a", 1)
	defer cancel()

	hub.Publish(Event{Type: EventTypeMessageCreated, BotID: "bot-a"})
	hub.Publish(Event{Type: EventTypeMessageCreated, BotID: "bot-a"})
	hub.Publish(Event{Type: EventTypeMessageCreated, BotID: "bot-a"})

	select {
	case <-sub.Events:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("expected at least one event in buffer")
	}
}

// TestHubSubscriptionDroppedCounter pins the per-subscription drop accounting
// the SSE writers rely on to emit a `dropped` frame to the client. Sending
// 5 events into a buffer-2 subscription yields 3 drops; reading the counter
// resets it so the next read starts fresh.
func TestHubSubscriptionDroppedCounter(t *testing.T) {
	hub := NewHub()
	sub, cancel := hub.Subscribe("bot-a", 2)
	defer cancel()

	for i := 0; i < 5; i++ {
		hub.Publish(Event{Type: EventTypeMessageCreated, BotID: "bot-a"})
	}

	if got := sub.DroppedSinceLastRead(); got != 3 {
		t.Fatalf("DroppedSinceLastRead = %d, want 3", got)
	}
	if got := sub.DroppedSinceLastRead(); got != 0 {
		t.Fatalf("DroppedSinceLastRead after reset = %d, want 0", got)
	}
}
