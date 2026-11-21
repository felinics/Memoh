package external

import (
	"reflect"
	"strings"
	"sync"

	sdk "github.com/felinics/twilight/sdk"

	contextlimit "github.com/felinics/memoh/internal/agent/context/limit"
	"github.com/felinics/memoh/internal/agent/event"
)

// TurnStream is a turn's one event stream: it records the turn and forwards
// it to the live consumer from its own goroutine. A runtime emits from the
// loop that reads its agent, so emitting never waits on the consumer: a slow
// stream would hold up every response, permission request and stop
// confirmation behind it. The agent's events, the decisions that settle after
// a stop, and the events of Memoh tools served through the gateway arrive on
// different paths; they queue here in arrival order, so the live stream and
// the persisted transcript agree.
//
// While delivery lags, consecutive bare text or reasoning deltas fold into the
// pending tail, so the backlog grows with changes of event kind rather than
// with tokens, and a slow consumer receives fewer, larger deltas carrying the
// same text. How the text is split into deltas is not preserved.
type TurnStream struct {
	next  EventSink
	limit contextlimit.ToolOutputLimit

	mu      sync.Mutex
	wake    *sync.Cond
	pending []turnStreamItem
	closed  bool
	done    chan struct{}

	// Written only by the delivery goroutine, read after it finished.
	transcript *TranscriptRecorder
	steerIDs   []string
}

type turnStreamItem struct {
	ev event.StreamEvent
	// folded holds the concatenated delta once a later delta folded into ev.
	folded   *strings.Builder
	foldable bool
	// recordOnly keeps a decision that settled after the turn was stopped in
	// the transcript without sending another live frame.
	recordOnly bool
	// steer is user input the agent took mid-turn: it enters the transcript
	// as a user message where it landed, and accepted runs once everything
	// before it reached the consumer.
	steer *steerInput
}

type steerInput struct {
	id, text string
	accepted func()
}

// NewTurnStream starts the stream; Finish must be called to end it.
func NewTurnStream(next EventSink, limit contextlimit.ToolOutputLimit) *TurnStream {
	// Events are limited as they queue, so the recorder needs no limit of its own.
	s := &TurnStream{next: next, limit: limit, transcript: NewTranscriptRecorder(), done: make(chan struct{})}
	s.wake = sync.NewCond(&s.mu)
	go s.run()
	return s
}

// EmitStreamEvent queues ev for the consumer and the transcript. After Finish
// it is dropped: the consumer may already be gone.
func (s *TurnStream) EmitStreamEvent(ev event.StreamEvent) {
	ev = LimitStreamEvent(ev, s.limit)
	s.enqueue(turnStreamItem{ev: ev, foldable: foldableDelta(ev)})
}

// RecordTerminalDecision keeps ev in the transcript without a live frame.
func (s *TurnStream) RecordTerminalDecision(ev event.StreamEvent) {
	s.enqueue(turnStreamItem{ev: LimitStreamEvent(ev, s.limit), recordOnly: true})
}

// SteerInput records user input the agent took mid-turn at this point of the
// turn; accepted (may be nil) runs once the events before it were delivered.
func (s *TurnStream) SteerInput(id, text string, accepted func()) {
	s.enqueue(turnStreamItem{steer: &steerInput{id: id, text: text, accepted: accepted}})
}

func (s *TurnStream) enqueue(item turnStreamItem) {
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

func (s *TurnStream) run() {
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

func (s *TurnStream) deliver(item turnStreamItem) {
	if steer := item.steer; steer != nil {
		s.transcript.AddUser(steer.text)
		s.steerIDs = append(s.steerIDs, steer.id)
		if steer.accepted != nil {
			steer.accepted()
		}
		return
	}
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
// the consumer, so the consumer may be closed afterwards. Safe to call more
// than once.
func (s *TurnStream) Finish() {
	s.mu.Lock()
	s.closed = true
	s.wake.Signal()
	s.mu.Unlock()
	<-s.done
}

// Transcript is the turn as persisted, once every event has been delivered.
func (s *TurnStream) Transcript(finalText string) []sdk.Message {
	s.Finish()
	return s.transcript.Messages(finalText)
}

// SteerInputIDs names the mid-turn inputs the turn took, in order, once every
// event has been delivered.
func (s *TurnStream) SteerInputIDs() []string {
	s.Finish()
	return s.steerIDs
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
