package application

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/felinics/memoh/internal/agent/runtime/native"
)

type acpActivePromptHub struct {
	mu     sync.Mutex
	nextID int
	closed bool
	subs   map[int]*acpActivePromptSubscriber
}

type acpActivePromptSubscription struct {
	sub     *acpActivePromptSubscriber
	release func()
}

type acpActivePromptForwardOptions struct {
	SkipToolCallID  string
	SkipUserInputID string
	SkipApprovalID  string
}

type acpActivePromptSubscriber struct {
	mu      sync.Mutex
	notify  chan struct{}
	done    chan struct{}
	closed  bool
	pending []native.StreamEvent
}

func newACPActivePromptHub() *acpActivePromptHub {
	return &acpActivePromptHub{subs: make(map[int]*acpActivePromptSubscriber)}
}

func newACPActivePromptSubscriber() *acpActivePromptSubscriber {
	return &acpActivePromptSubscriber{
		notify: make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
}

func (s *acpActivePromptSubscriber) enqueue(ev native.StreamEvent) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.pending = append(s.pending, ev)
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *acpActivePromptSubscriber) next(ctx context.Context) (native.StreamEvent, bool, error) {
	for {
		s.mu.Lock()
		if len(s.pending) > 0 {
			ev := s.pending[0]
			copy(s.pending, s.pending[1:])
			s.pending[len(s.pending)-1] = native.StreamEvent{}
			s.pending = s.pending[:len(s.pending)-1]
			s.mu.Unlock()
			return ev, true, nil
		}
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return native.StreamEvent{}, false, nil
		}
		select {
		case <-s.notify:
		case <-s.done:
		case <-ctx.Done():
			return native.StreamEvent{}, false, ctx.Err()
		}
	}
}

func (s *acpActivePromptSubscriber) close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.done)
	s.mu.Unlock()
}

func (h *acpActivePromptHub) subscribe() (*acpActivePromptSubscription, bool) {
	if h == nil {
		return nil, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, false
	}
	id := h.nextID
	h.nextID++
	sub := newACPActivePromptSubscriber()
	h.subs[id] = sub
	return &acpActivePromptSubscription{
		sub: sub,
		release: func() {
			h.mu.Lock()
			if h.subs[id] == sub {
				delete(h.subs, id)
			}
			h.mu.Unlock()
			sub.close()
		},
	}, true
}

func (h *acpActivePromptHub) emit(ev native.StreamEvent) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	for _, ch := range h.subs {
		ch.enqueue(ev)
	}
}

func (h *acpActivePromptHub) close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	subs := h.subs
	h.subs = nil
	h.mu.Unlock()
	for _, sub := range subs {
		sub.close()
	}
}

func (s *Service) registerACPActivePrompt(botID, sessionID string) *acpActivePromptHub {
	if s == nil {
		return nil
	}
	botID = strings.TrimSpace(botID)
	sessionID = strings.TrimSpace(sessionID)
	if botID == "" || sessionID == "" {
		return nil
	}
	key := sessionTurnKey(botID, sessionID)
	hub := newACPActivePromptHub()
	s.acpPromptMu.Lock()
	if s.acpPromptHubs == nil {
		s.acpPromptHubs = make(map[string]*acpActivePromptHub)
	}
	previous := s.acpPromptHubs[key]
	s.acpPromptHubs[key] = hub
	s.acpPromptMu.Unlock()
	if previous != nil {
		previous.close()
	}
	return hub
}

func (s *Service) unregisterACPActivePrompt(botID, sessionID string, hub *acpActivePromptHub) {
	if s == nil || hub == nil {
		return
	}
	botID = strings.TrimSpace(botID)
	sessionID = strings.TrimSpace(sessionID)
	if botID == "" || sessionID == "" {
		hub.close()
		return
	}
	key := sessionTurnKey(botID, sessionID)
	s.acpPromptMu.Lock()
	if s.acpPromptHubs[key] == hub {
		delete(s.acpPromptHubs, key)
	}
	s.acpPromptMu.Unlock()
	hub.close()
}

func (s *Service) subscribeACPActivePrompt(botID, sessionID string) (*acpActivePromptSubscription, bool) {
	if s == nil {
		return nil, false
	}
	botID = strings.TrimSpace(botID)
	sessionID = strings.TrimSpace(sessionID)
	if botID == "" || sessionID == "" {
		return nil, false
	}
	key := sessionTurnKey(botID, sessionID)
	s.acpPromptMu.Lock()
	hub := s.acpPromptHubs[key]
	s.acpPromptMu.Unlock()
	if hub == nil {
		return nil, false
	}
	return hub.subscribe()
}

func forwardACPActivePrompt(ctx context.Context, sub *acpActivePromptSubscription, eventCh chan<- WSStreamEvent, opts acpActivePromptForwardOptions) error {
	if sub == nil {
		return emitApprovalAck(ctx, eventCh)
	}
	defer sub.release()
	if eventCh == nil {
		return nil
	}
	if err := sendAgentStreamEvent(ctx, eventCh, native.StreamEvent{Type: native.EventStart}); err != nil {
		return err
	}
	for {
		ev, ok, err := sub.sub.next(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return sendAgentStreamEvent(ctx, eventCh, native.StreamEvent{Type: native.EventAbort})
		}
		if opts.skip(ev) {
			continue
		}
		if err := sendAgentStreamEvent(ctx, eventCh, ev); err != nil {
			return err
		}
		if ev.IsTerminal() {
			return nil
		}
	}
}

func (o acpActivePromptForwardOptions) skip(ev native.StreamEvent) bool {
	switch ev.Type {
	case native.EventUserInputRequest:
		if sameNonEmpty(ev.UserInputID, o.SkipUserInputID) {
			return true
		}
		return sameNonEmpty(ev.ToolCallID, o.SkipToolCallID)
	case native.EventToolApprovalRequest:
		if sameNonEmpty(ev.ApprovalID, o.SkipApprovalID) {
			return true
		}
		return sameNonEmpty(ev.ToolCallID, o.SkipToolCallID)
	case native.EventToolCallInputStart,
		native.EventToolCallStart,
		native.EventToolCallMetadata,
		native.EventToolCallProgress,
		native.EventToolCallEnd:
		return sameNonEmpty(ev.ToolCallID, o.SkipToolCallID)
	default:
		return false
	}
}

func sameNonEmpty(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	return a != "" && b != "" && a == b
}

func sendAgentStreamEvent(ctx context.Context, eventCh chan<- WSStreamEvent, ev native.StreamEvent) error {
	if eventCh == nil {
		return nil
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	select {
	case eventCh <- data:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
