package event

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

const (
	// DefaultBufferSize is the default per-subscriber channel buffer.
	DefaultBufferSize = 64
)

// EventType identifies the event category published by the message event hub.
type EventType string

const (
	// EventTypeMessageCreated is emitted after a message is persisted successfully.
	EventTypeMessageCreated EventType = "message_created"
	// EventTypeSessionCreated is emitted after a new user-facing session is
	// created. Consumers use it to surface a fresh session in sidebars before
	// the first message arrives.
	EventTypeSessionCreated EventType = "session_created"
	// EventTypeSessionTitleUpdated is emitted after a session title is auto-generated.
	EventTypeSessionTitleUpdated EventType = "session_title_updated"
	// EventTypeBackgroundTask is emitted for live background task updates.
	EventTypeBackgroundTask EventType = "background_task"
)

// Event is the normalized payload emitted by the in-process message event hub.
type Event struct {
	Type  EventType       `json:"type"`
	BotID string          `json:"bot_id"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// Publisher publishes events to subscribers.
type Publisher interface {
	Publish(event Event)
}

// Subscriber subscribes to bot-scoped events.
type Subscriber interface {
	Subscribe(botID string, buffer int) (*Subscription, func())
}

// Subscription is a live event subscription handed back from Subscribe. It
// exposes the read-only event channel and a counter of events dropped on
// account of a full subscriber buffer. The counter is consumer-reset so
// SSE writers can surface a "your view is stale" frame on the wire between
// regular events.
type Subscription struct {
	ID      string
	Events  <-chan Event
	dropped atomic.Int64
}

// DroppedSinceLastRead returns the number of events dropped on this
// subscription since the last call (or since Subscribe), then resets the
// counter to zero atomically. Returning and resetting in one operation keeps
// the producer free to keep counting concurrent drops.
func (s *Subscription) DroppedSinceLastRead() int64 {
	if s == nil {
		return 0
	}
	return s.dropped.Swap(0)
}

// dropLogInterval rate-limits the "subscriber buffer full" log so a sustained
// burst doesn't flood the log file. Per-subscription atomic counters still
// record the exact drop count between log lines.
const dropLogInterval = 5 * time.Second

// subscriberState couples a subscriber's delivery channel with its dropped
// counter so Publish can both deliver and account for misses without an extra
// lookup.
type subscriberState struct {
	ch  chan Event
	sub *Subscription
}

// Hub is an in-process pub/sub dispatcher for bot-scoped message events.
type Hub struct {
	mu      sync.RWMutex
	streams map[string]map[string]*subscriberState

	logger       *slog.Logger
	dropped      atomic.Int64
	lastLoggedNS atomic.Int64
}

// NewHub creates an empty message event hub. An optional logger is used to
// rate-limit-log dropped events; if nil, slog.Default() is used.
func NewHub(loggers ...*slog.Logger) *Hub {
	var logger *slog.Logger
	if len(loggers) > 0 {
		logger = loggers[0]
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Hub{
		streams: map[string]map[string]*subscriberState{},
		logger:  logger,
	}
}

// Publish broadcasts one event to all subscribers under the same bot ID.
// Slow subscribers are accounted for non-blockingly: the per-subscription
// dropped counter is bumped so the SSE writer can surface a `dropped` frame
// to the client, and a hub-wide counter is logged at most once per interval
// for operator visibility.
func (h *Hub) Publish(event Event) {
	if h == nil {
		return
	}
	botID := strings.TrimSpace(event.BotID)
	if botID == "" {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, st := range h.streams[botID] {
		select {
		case st.ch <- event:
		default:
			st.sub.dropped.Add(1)
			h.recordDrop(botID, event.Type)
		}
	}
}

// recordDrop bumps the hub-wide drop counter and, when the rate-limit window
// has elapsed, logs the count since the last line. Per-subscription accounting
// happens in Publish so the SSE writer can react per-stream.
func (h *Hub) recordDrop(botID string, typ EventType) {
	h.dropped.Add(1)
	now := time.Now().UnixNano()
	last := h.lastLoggedNS.Load()
	if now-last < int64(dropLogInterval) {
		return
	}
	if !h.lastLoggedNS.CompareAndSwap(last, now) {
		return
	}
	dropped := h.dropped.Swap(0)
	h.logger.Warn("message event hub dropped events on full subscriber buffer",
		slog.Int64("dropped_since_last", dropped),
		slog.String("bot_id", botID),
		slog.String("last_event_type", string(typ)),
	)
}

// Subscribe registers one subscriber under a bot ID and returns the
// Subscription handle plus a cancel function. The Subscription exposes the
// read-only event channel and a per-stream dropped counter.
func (h *Hub) Subscribe(botID string, buffer int) (*Subscription, func()) {
	if h == nil {
		ch := make(chan Event)
		close(ch)
		return &Subscription{Events: ch}, func() {}
	}
	botID = strings.TrimSpace(botID)
	if botID == "" {
		ch := make(chan Event)
		close(ch)
		return &Subscription{Events: ch}, func() {}
	}
	if buffer <= 0 {
		buffer = DefaultBufferSize
	}

	streamID := uuid.NewString()
	ch := make(chan Event, buffer)
	sub := &Subscription{ID: streamID, Events: ch}
	state := &subscriberState{ch: ch, sub: sub}

	h.mu.Lock()
	streams, ok := h.streams[botID]
	if !ok {
		streams = map[string]*subscriberState{}
		h.streams[botID] = streams
	}
	streams[streamID] = state
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			streams := h.streams[botID]
			if streams != nil {
				if current, ok := streams[streamID]; ok {
					delete(streams, streamID)
					close(current.ch)
				}
				if len(streams) == 0 {
					delete(h.streams, botID)
				}
			}
			h.mu.Unlock()
		})
	}

	return sub, cancel
}
