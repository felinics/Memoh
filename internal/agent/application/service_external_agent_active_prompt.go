package application

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/felinics/memoh/internal/agent/runtime/native"
)

type externalAgentActivePromptHub struct {
	mu     sync.Mutex
	nextID int
	closed bool
	subs   map[int]*externalAgentActivePromptSubscriber
}

type externalAgentActivePromptSubscription struct {
	sub     *externalAgentActivePromptSubscriber
	release func()
}

type externalAgentActivePromptForwardOptions struct {
	SkipToolCallID  string
	SkipUserInputID string
	SkipApprovalID  string
}

type externalAgentActivePromptSubscriber struct {
	mu      sync.Mutex
	notify  chan struct{}
	done    chan struct{}
	closed  bool
	pending []native.StreamEvent
}

func newExternalAgentActivePromptHub() *externalAgentActivePromptHub {
	return &externalAgentActivePromptHub{subs: make(map[int]*externalAgentActivePromptSubscriber)}
}

func newExternalAgentActivePromptSubscriber() *externalAgentActivePromptSubscriber {
	return &externalAgentActivePromptSubscriber{
		notify: make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
}

func (s *externalAgentActivePromptSubscriber) enqueue(ev native.StreamEvent) {
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

func (s *externalAgentActivePromptSubscriber) next(ctx context.Context) (native.StreamEvent, bool, error) {
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

func (s *externalAgentActivePromptSubscriber) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.done)
	s.mu.Unlock()
}

func (h *externalAgentActivePromptHub) subscribe() (*externalAgentActivePromptSubscription, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, false
	}
	id := h.nextID
	h.nextID++
	sub := newExternalAgentActivePromptSubscriber()
	h.subs[id] = sub
	return &externalAgentActivePromptSubscription{
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

func (h *externalAgentActivePromptHub) emit(ev native.StreamEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	for _, ch := range h.subs {
		ch.enqueue(ev)
	}
}

func (h *externalAgentActivePromptHub) close() {
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

func (s *Service) registerExternalAgentActivePrompt(botID, sessionID string) *externalAgentActivePromptHub {
	botID = strings.TrimSpace(botID)
	sessionID = strings.TrimSpace(sessionID)
	key := sessionTurnKey(botID, sessionID)
	hub := newExternalAgentActivePromptHub()
	previous, replaced := s.externalAgentPromptHubs.Swap(key, hub)
	if replaced {
		previous.(*externalAgentActivePromptHub).close()
	}
	return hub
}

// hasExternalAgentActivePrompt reports whether a runtime driver call is currently
// executing for the session; the registration brackets the driver Prompt
// exactly, so this is the "driver actually stopped" signal deletion waits on.
func (s *Service) hasExternalAgentActivePrompt(botID, sessionID string) bool {
	key := sessionTurnKey(strings.TrimSpace(botID), strings.TrimSpace(sessionID))
	_, ok := s.externalAgentPromptHubs.Load(key)
	return ok
}

func (s *Service) unregisterExternalAgentActivePrompt(botID, sessionID string, hub *externalAgentActivePromptHub) {
	botID = strings.TrimSpace(botID)
	sessionID = strings.TrimSpace(sessionID)
	key := sessionTurnKey(botID, sessionID)
	s.externalAgentPromptHubs.CompareAndDelete(key, hub)
	hub.close()
}

func (s *Service) subscribeExternalAgentActivePrompt(botID, sessionID string) (*externalAgentActivePromptSubscription, bool) {
	botID = strings.TrimSpace(botID)
	sessionID = strings.TrimSpace(sessionID)
	key := sessionTurnKey(botID, sessionID)
	value, ok := s.externalAgentPromptHubs.Load(key)
	if !ok {
		return nil, false
	}
	return value.(*externalAgentActivePromptHub).subscribe()
}

func forwardExternalAgentActivePrompt(ctx context.Context, sub *externalAgentActivePromptSubscription, eventCh chan<- WSStreamEvent, opts externalAgentActivePromptForwardOptions) error {
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

func (o externalAgentActivePromptForwardOptions) skip(ev native.StreamEvent) bool {
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
