package client

import (
	"reflect"
	"strings"
	"sync"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/event"
)

// TurnSink is a turn's one event stream: it records the turn and forwards it
// to a live consumer from its own goroutine. Session emits on acp-go-sdk's
// only notification goroutine, and reading from the Agent stops while that
// goroutine falls behind (see updateGate), so emitting never waits on the
// consumer: a slow stream would hold up every response, permission request
// and stop confirmation behind it. The Agent's events, the decisions that
// settle after a stop, and the events of Memoh tools served through the
// gateway arrive on different paths; they queue here in arrival order, so the
// live stream and the persisted transcript agree.
//
// While delivery lags, consecutive bare text or reasoning deltas fold into the
// pending tail, so the backlog grows with changes of event kind rather than
// with tokens, and a slow consumer receives fewer, larger deltas carrying the
// same text. How the text is split into deltas is not preserved.
type TurnSink struct {
	next  EventSink
	limit ToolOutputLimit

	mu      sync.Mutex
	wake    *sync.Cond
	pending []turnSinkItem
	closed  bool
	done    chan struct{}

	// Written only by the delivery goroutine, read after it finished.
	transcript *TranscriptRecorder
}

type turnSinkItem struct {
	ev event.StreamEvent
	// folded holds the concatenated delta once a later delta folded into ev.
	folded   *strings.Builder
	foldable bool
	// recordOnly keeps a decision that settled after the turn was stopped in
	// the transcript without sending another live frame.
	recordOnly bool
}

var _ TerminalDecisionSink = (*TurnSink)(nil)

func NewTurnSink(next EventSink, limit ToolOutputLimit) *TurnSink {
	// Events are limited as they queue, so the recorder needs no limit of its own.
	s := &TurnSink{next: next, limit: limit, transcript: NewTranscriptRecorder(), done: make(chan struct{})}
	s.wake = sync.NewCond(&s.mu)
	go s.run()
	return s
}

func (s *TurnSink) EmitStreamEvent(ev event.StreamEvent) {
	ev = LimitStreamEvent(ev, s.limit)
	s.enqueue(turnSinkItem{ev: ev, foldable: foldableDelta(ev)})
}

func (s *TurnSink) RecordTerminalDecision(ev event.StreamEvent) {
	s.enqueue(turnSinkItem{ev: LimitStreamEvent(ev, s.limit), recordOnly: true})
}

func (s *TurnSink) enqueue(item turnSinkItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if n := len(s.pending); item.foldable && n > 0 {
		if tail := &s.pending[n-1]; tail.foldable && tail.ev.Type == item.ev.Type {
			if tail.folded == nil {
				tail.folded = &strings.Builder{}
				tail.folded.WriteString(tail.ev.Delta)
			}
			tail.folded.WriteString(item.ev.Delta)
			return
		}
	}
	s.pending = append(s.pending, item)
	s.wake.Signal()
}

func (s *TurnSink) run() {
	defer close(s.done)
	for {
		s.mu.Lock()
		for len(s.pending) == 0 && !s.closed {
			s.wake.Wait()
		}
		batch := s.pending
		s.pending = nil
		s.mu.Unlock()
		if len(batch) == 0 {
			return
		}
		for _, item := range batch {
			s.deliver(item)
		}
	}
}

func (s *TurnSink) deliver(item turnSinkItem) {
	ev := item.ev
	if item.folded != nil {
		ev.Delta = item.folded.String()
	}
	s.transcript.Add(ev)
	if !item.recordOnly && s.next != nil {
		s.next.EmitStreamEvent(ev)
	}
}

// Finish stops taking events and returns once every accepted one has reached
// the consumer, so the consumer may be closed afterwards.
func (s *TurnSink) Finish() {
	s.mu.Lock()
	s.closed = true
	s.wake.Signal()
	s.mu.Unlock()
	<-s.done
}

// Transcript is the turn as persisted, once every event has been delivered.
func (s *TurnSink) Transcript(finalText string) []sdk.Message {
	s.Finish()
	return s.transcript.Messages(finalText)
}

// foldableDelta reports whether ev is a bare text or reasoning delta: one whose
// only payload is Delta, so that two of them concatenate into an equivalent
// event. Any other field set on the event keeps it separate.
func foldableDelta(ev event.StreamEvent) bool {
	if ev.Type != event.TextDelta && ev.Type != event.ReasoningDelta {
		return false
	}
	ev.Type, ev.Delta = "", ""
	return reflect.ValueOf(ev).IsZero()
}
