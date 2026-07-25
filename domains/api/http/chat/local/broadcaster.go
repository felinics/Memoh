package local

import (
	"context"

	"github.com/memohai/memoh/domains/channel/gateway"
)

// RouteHubBroadcaster implements gateway.StreamObserver by forwarding events
// to the RouteHub. This enables cross-channel visibility: events from external
// channels (Telegram, Feishu, …) are mirrored to RouteHub subscribers.
type RouteHubBroadcaster struct {
	hub *RouteHub
}

// NewRouteHubBroadcaster creates a broadcaster that publishes to the given hub.
func NewRouteHubBroadcaster(hub *RouteHub) *RouteHubBroadcaster {
	return &RouteHubBroadcaster{hub: hub}
}

// OnStreamEvent publishes the event to all RouteHub subscribers keyed by botID.
func (b *RouteHubBroadcaster) OnStreamEvent(_ context.Context, botID string, source gateway.ChannelType, event gateway.StreamEvent) {
	if b.hub == nil || botID == "" {
		return
	}
	// Stamp source channel into metadata so the WebUI can distinguish origin.
	// Clone metadata to avoid mutating the caller's event.
	enriched := event
	meta := make(map[string]any, len(event.Metadata)+1)
	for k, v := range event.Metadata {
		meta[k] = v
	}
	meta["source_channel"] = string(source)
	enriched.Metadata = meta
	b.hub.PublishEvent(botID, enriched)
}
