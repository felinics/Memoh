package local

import (
	"context"
	"testing"

	"github.com/felinics/memoh/internal/channel"
)

func TestRouteHubBroadcaster_OnStreamEvent(t *testing.T) {
	hub := NewRouteHub()
	_, ch, cancel := hub.Subscribe("bot1")
	defer cancel()

	broadcaster := NewRouteHubBroadcaster(hub)
	event := channel.StreamEvent{
		Type:  channel.StreamEventDelta,
		Delta: "hello from telegram",
	}

	broadcaster.OnStreamEvent(context.Background(), "bot1", "telegram", event)

	select {
	case received := <-ch:
		if received.Event.Delta != "hello from telegram" {
			t.Fatalf("unexpected delta: %s", received.Event.Delta)
		}
		if received.Event.Metadata == nil {
			t.Fatal("expected metadata to be set")
		}
		if received.Event.Metadata["source_channel"] != "telegram" {
			t.Fatalf("unexpected source_channel: %v", received.Event.Metadata["source_channel"])
		}
	default:
		t.Fatal("expected event to be published to hub")
	}
}

func TestRouteHubBroadcaster_EmptyBotID(t *testing.T) {
	hub := NewRouteHub()
	_, ch, cancel := hub.Subscribe("")
	defer cancel()

	broadcaster := NewRouteHubBroadcaster(hub)
	broadcaster.OnStreamEvent(context.Background(), "", "telegram", channel.StreamEvent{
		Type:  channel.StreamEventDelta,
		Delta: "should be dropped",
	})

	select {
	case <-ch:
		t.Fatal("expected no event for empty botID")
	default:
		// OK: no event published.
	}
}

func TestRouteHubBroadcaster_NilHub(_ *testing.T) {
	broadcaster := NewRouteHubBroadcaster(nil)
	// Must not panic.
	broadcaster.OnStreamEvent(context.Background(), "bot1", "telegram", channel.StreamEvent{
		Type:  channel.StreamEventDelta,
		Delta: "no-op",
	})
}

func TestRouteHubBroadcaster_PreservesOriginalMetadata(t *testing.T) {
	hub := NewRouteHub()
	_, ch, cancel := hub.Subscribe("bot1")
	defer cancel()

	broadcaster := NewRouteHubBroadcaster(hub)
	event := channel.StreamEvent{
		Type:  channel.StreamEventDelta,
		Delta: "data",
		Metadata: map[string]any{
			"existing_key": "existing_value",
		},
	}

	broadcaster.OnStreamEvent(context.Background(), "bot1", "feishu", event)

	select {
	case received := <-ch:
		if received.Event.Metadata["source_channel"] != "feishu" {
			t.Fatalf("unexpected source_channel: %v", received.Event.Metadata["source_channel"])
		}
		// The original event should NOT be mutated (enriched is a copy).
		if event.Metadata["source_channel"] != nil {
			t.Fatal("original event metadata should not be mutated")
		}
	default:
		t.Fatal("expected event")
	}
}
