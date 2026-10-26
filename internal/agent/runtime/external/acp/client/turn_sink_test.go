package client

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/event"
)

// gatedSink blocks its first delivery until released, so a test can queue a
// backlog behind it deterministically.
type gatedSink struct {
	mu        sync.Mutex
	got       []string
	entered   chan struct{}
	release   chan struct{}
	firstOnce sync.Once
}

func newGatedSink() *gatedSink {
	return &gatedSink{entered: make(chan struct{}), release: make(chan struct{})}
}

func (s *gatedSink) EmitStreamEvent(ev event.StreamEvent) {
	s.firstOnce.Do(func() {
		close(s.entered)
		<-s.release
	})
	entry := fmt.Sprintf("%s:%s", ev.Type, ev.Delta)
	if ev.ToolCallID != "" {
		entry += "#" + ev.ToolCallID
	}
	if len(ev.Metadata) > 0 {
		entry += "+meta"
	}
	s.mu.Lock()
	s.got = append(s.got, entry)
	s.mu.Unlock()
}

func (s *gatedSink) entries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.got...)
}

func textDelta(delta string) event.StreamEvent {
	return event.StreamEvent{Type: event.TextDelta, Delta: delta}
}

func reasoningDelta(delta string) event.StreamEvent {
	return event.StreamEvent{Type: event.ReasoningDelta, Delta: delta}
}

func TestTurnSinkNeverWaitsAndKeepsTheTurnInOrder(t *testing.T) {
	release := make(chan struct{})
	var live []string
	sink := NewTurnSink(EventSinkFunc(func(ev event.StreamEvent) {
		<-release
		live = append(live, ev.Delta)
	}), ToolOutputLimit{})

	emitted := make(chan struct{})
	go func() {
		sink.EmitStreamEvent(textDelta("a"))
		sink.RecordTerminalDecision(textDelta("x"))
		sink.EmitStreamEvent(textDelta("b"))
		close(emitted)
	}()
	select {
	case <-emitted:
	case <-time.After(5 * time.Second):
		t.Fatal("emitting waited on a stalled consumer")
	}
	close(release)

	want := NewTranscriptRecorder()
	for _, delta := range []string{"a", "x", "b"} {
		want.Add(textDelta(delta))
	}
	if got := sink.Transcript(""); !reflect.DeepEqual(got, want.Messages("")) || !reflect.DeepEqual(live, []string{"a", "b"}) {
		t.Fatalf("transcript %#v, live %v; want every event in order and the recorded one kept off the live stream", got, live)
	}
}

func TestTurnSinkFoldsBacklogAndFinishDeliversIt(t *testing.T) {
	next := newGatedSink()
	sink := NewTurnSink(next, ToolOutputLimit{})
	sink.EmitStreamEvent(textDelta("a"))
	<-next.entered // "a" is being delivered; everything below queues behind it.

	sink.EmitStreamEvent(textDelta("b"))
	sink.EmitStreamEvent(textDelta("c"))
	sink.EmitStreamEvent(reasoningDelta("x"))
	sink.EmitStreamEvent(reasoningDelta("y"))
	sink.EmitStreamEvent(event.StreamEvent{Type: event.ToolCallStart, ToolCallID: "call-1"})
	sink.EmitStreamEvent(textDelta("d"))
	sink.EmitStreamEvent(event.StreamEvent{Type: event.TextDelta, Delta: "e", Metadata: map[string]any{"k": "v"}})
	sink.EmitStreamEvent(textDelta("f"))
	sink.EmitStreamEvent(textDelta("g"))

	finished := make(chan struct{})
	go func() {
		sink.Finish()
		close(finished)
	}()
	select {
	case <-finished:
		t.Fatal("Finish returned while deliveries were still pending")
	case <-time.After(50 * time.Millisecond):
	}
	close(next.release)
	<-finished
	sink.EmitStreamEvent(textDelta("late"))

	want := []string{
		"text_delta:a",
		"text_delta:bc",
		"reasoning_delta:xy",
		"tool_call_start:#call-1",
		"text_delta:d",
		"text_delta:e+meta", // not bare: never folded into a neighbour
		"text_delta:fg",
	}
	if got := next.entries(); !reflect.DeepEqual(got, want) {
		t.Fatalf("delivered %q, want %q", got, want)
	}
}
