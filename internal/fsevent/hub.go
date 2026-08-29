// Package fsevent fans out debounced per-bot workspace filesystem change
// notifications from host-side mutation chokepoints (fs API, agent tools,
// workspace watchers) to live UI subscribers.
package fsevent

import (
	"sync"
	"time"
)

// DefaultWindow is the trailing coalescing window applied to publishes.
const DefaultWindow = 200 * time.Millisecond

// maxBatchPaths bounds one delivery's path list; larger batches collapse to a
// wildcard (nil) so payloads stay small and consumers fall back to a full
// refresh.
const maxBatchPaths = 16

type pendingBatch struct {
	paths    map[string]struct{}
	wildcard bool
}

func (b *pendingBatch) add(paths []string) {
	if b.wildcard {
		return
	}
	if paths == nil {
		b.wildcard = true
		b.paths = nil
		return
	}
	for _, p := range paths {
		b.paths[p] = struct{}{}
	}
	if len(b.paths) > maxBatchPaths {
		b.wildcard = true
		b.paths = nil
	}
}

func (b *pendingBatch) delivery() []string {
	if b.wildcard {
		return nil
	}
	paths := make([]string, 0, len(b.paths))
	for p := range b.paths {
		paths = append(paths, p)
	}
	return paths
}

func newPendingBatch() *pendingBatch {
	return &pendingBatch{paths: make(map[string]struct{})}
}

// subscriber owns a one-slot mailbox drained by its own goroutine, so one
// slow or blocked callback (a wedged WebSocket writer) can neither delay
// other subscribers nor pile up a goroutine per flush — backlog merges into
// the single pending batch instead.
type subscriber struct {
	fn     func([]string)
	notify chan struct{}
	done   chan struct{}

	mu      sync.Mutex
	pending *pendingBatch
}

func (s *subscriber) deposit(paths []string) {
	s.mu.Lock()
	if s.pending == nil {
		s.pending = newPendingBatch()
	}
	s.pending.add(paths)
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *subscriber) take() (delivery []string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return nil, false
	}
	delivery = s.pending.delivery()
	s.pending = nil
	return delivery, true
}

func (s *subscriber) run() {
	for {
		select {
		case <-s.done:
			return
		case <-s.notify:
			for {
				delivery, ok := s.take()
				if !ok {
					break
				}
				s.fn(delivery)
			}
		}
	}
}

// Hub coalesces Publish calls per bot within a trailing window and delivers
// one batch to every subscriber of that bot. A nil paths slice means
// "unknown scope" and turns the whole batch into a wildcard.
type Hub struct {
	mu      sync.Mutex
	window  time.Duration
	nextID  int
	subs    map[string]map[int]*subscriber
	pending map[string]*pendingBatch
}

func NewHub(window time.Duration) *Hub {
	if window <= 0 {
		window = DefaultWindow
	}
	return &Hub{
		window:  window,
		subs:    make(map[string]map[int]*subscriber),
		pending: make(map[string]*pendingBatch),
	}
}

// Publish records a change for botID. paths carries the touched absolute
// paths when known; nil means unknown scope (wildcard).
func (h *Hub) Publish(botID string, paths []string) {
	if botID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	batch, ok := h.pending[botID]
	if !ok {
		batch = newPendingBatch()
		h.pending[botID] = batch
		time.AfterFunc(h.window, func() { h.flush(botID) })
	}
	batch.add(paths)
}

func (h *Hub) flush(botID string) {
	h.mu.Lock()
	batch, ok := h.pending[botID]
	if !ok {
		h.mu.Unlock()
		return
	}
	delete(h.pending, botID)
	delivery := batch.delivery()
	subs := make([]*subscriber, 0, len(h.subs[botID]))
	for _, sub := range h.subs[botID] {
		subs = append(subs, sub)
	}
	h.mu.Unlock()
	for _, sub := range subs {
		sub.deposit(delivery)
	}
}

// Subscribe registers fn for botID deliveries and returns an idempotent
// cancel. fn runs on a dedicated per-subscription goroutine, one delivery at
// a time; batches arriving while fn runs merge into a single pending batch.
func (h *Hub) Subscribe(botID string, fn func(paths []string)) (cancel func()) {
	sub := &subscriber{
		fn:     fn,
		notify: make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
	go sub.run()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextID++
	id := h.nextID
	if h.subs[botID] == nil {
		h.subs[botID] = make(map[int]*subscriber)
	}
	h.subs[botID][id] = sub
	var once sync.Once
	return func() {
		once.Do(func() { close(sub.done) })
		h.mu.Lock()
		defer h.mu.Unlock()
		if subs, ok := h.subs[botID]; ok {
			delete(subs, id)
			if len(subs) == 0 {
				delete(h.subs, botID)
			}
		}
	}
}
