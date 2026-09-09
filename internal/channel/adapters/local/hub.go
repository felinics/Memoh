package local

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/channel"
)

// RouteHubEvent is a routed outbound stream event for local transports.
type RouteHubEvent struct {
	Target string              `json:"target"`
	Event  channel.StreamEvent `json:"event"`
}

// RouteHub is a pub/sub hub that routes outbound messages to local subscribers by route key.
type RouteHub struct {
	mu      sync.RWMutex
	streams map[string]map[string]chan RouteHubEvent
}

// NewRouteHub creates an empty RouteHub.
func NewRouteHub() *RouteHub {
	return &RouteHub{
		streams: map[string]map[string]chan RouteHubEvent{},
	}
}

// Subscribe registers a new stream for the given route key and returns a stream ID,
// a read-only channel for messages, and a cancel function to unsubscribe.
func (h *RouteHub) Subscribe(routeKey string) (string, <-chan RouteHubEvent, func()) {
	streamID := uuid.NewString()
	ch := make(chan RouteHubEvent, 32)

	h.mu.Lock()
	streams, ok := h.streams[routeKey]
	if !ok {
		streams = map[string]chan RouteHubEvent{}
		h.streams[routeKey] = streams
	}
	streams[streamID] = ch
	h.mu.Unlock()

	cancel := func() {
		h.mu.Lock()
		streams := h.streams[routeKey]
		if streams != nil {
			if current, ok := streams[streamID]; ok {
				delete(streams, streamID)
				close(current)
			}
			if len(streams) == 0 {
				delete(h.streams, routeKey)
			}
		}
		h.mu.Unlock()
	}

	return streamID, ch, cancel
}

// Publish delivers a message to all subscribers of the given route key.
// Slow receivers are silently dropped.
func (h *RouteHub) Publish(routeKey string, msg channel.OutboundMessage) {
	h.PublishEvent(routeKey, channel.StreamEvent{
		Type: channel.StreamEventFinal,
		Final: &channel.StreamFinalizePayload{
			Message: msg.Message,
		},
	})
}

// PublishEvent delivers a stream event to all subscribers of the given route key.
// Slow receivers are silently dropped.
func (h *RouteHub) PublishEvent(routeKey string, event channel.StreamEvent) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, ch := range h.streams[routeKey] {
		payload := RouteHubEvent{
			Target: routeKey,
			Event:  event,
		}
		select {
		case ch <- payload:
		default:
			// Drop if receiver is slow.
		}
	}
}

type localOutboundStream struct {
	hub    *RouteHub
	target string
	closed atomic.Bool
}

func newLocalOutboundStream(hub *RouteHub, target string) channel.PreparedOutboundStream {
	return &localOutboundStream{
		hub:    hub,
		target: target,
	}
}

func (s *localOutboundStream) Push(ctx context.Context, event channel.PreparedStreamEvent) error {
	if s == nil || s.hub == nil {
		return errors.New("route hub not configured")
	}
	if s.closed.Load() {
		return errors.New("stream is closed")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	s.hub.PublishEvent(s.target, event.LogicalEvent())
	return nil
}

func (s *localOutboundStream) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	s.closed.Store(true)
	return nil
}
