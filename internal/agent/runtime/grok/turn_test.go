package grok

import (
	"reflect"
	"testing"
	"time"

	contextlimit "github.com/felinics/memoh/internal/agent/context/limit"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

func TestTurnSinkNeverWaitsAndKeepsTheTurnInOrder(t *testing.T) {
	release := make(chan struct{})
	var live []string
	sink := newTurnSink(external.EventSinkFunc(func(ev event.StreamEvent) {
		<-release
		live = append(live, ev.Delta)
	}), contextlimit.ToolOutputLimit{})
	text := func(delta string) event.StreamEvent { return event.StreamEvent{Type: event.TextDelta, Delta: delta} }

	emitted := make(chan struct{})
	go func() {
		sink.EmitStreamEvent(text("a"))
		sink.RecordTerminalDecision(text("x"))
		sink.EmitStreamEvent(text("b"))
		close(emitted)
	}()
	select {
	case <-emitted:
	case <-time.After(5 * time.Second):
		t.Fatal("emitting waited on a stalled consumer")
	}
	close(release)

	want := external.NewTranscriptRecorder()
	for _, delta := range []string{"a", "x", "b"} {
		want.Add(text(delta))
	}
	if got := sink.transcript(""); !reflect.DeepEqual(got, want.Messages("")) || !reflect.DeepEqual(live, []string{"a", "b"}) {
		t.Fatalf("transcript %#v, live %v; want every event in order and the recorded one kept off the live stream", got, live)
	}
}
