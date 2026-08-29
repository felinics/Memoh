package acp

import (
	"testing"

	"github.com/felinics/memoh/internal/agent/event"
)

type recordingEventSink struct {
	events []event.StreamEvent
}

func (r *recordingEventSink) EmitStreamEvent(ev event.StreamEvent) {
	r.events = append(r.events, ev)
}

func TestFSChangeEventSinkNotifiesOnFSToolEnd(t *testing.T) {
	var got [][]string
	next := &recordingEventSink{}
	sink := newFSChangeEventSink(next, func(paths []string) { got = append(got, paths) })

	sink.EmitStreamEvent(event.StreamEvent{
		Type:     event.ToolCallEnd,
		ToolName: "write",
		Input:    map[string]any{"path": "/data/a.txt", "content": "x"},
	})
	sink.EmitStreamEvent(event.StreamEvent{
		Type:     event.ToolCallEnd,
		ToolName: "exec",
		Input:    map[string]any{"command": "make"},
	})

	if len(got) != 2 {
		t.Fatalf("notify calls = %d, want 2", len(got))
	}
	if len(got[0]) != 1 || got[0][0] != "/data/a.txt" {
		t.Fatalf("write paths = %v", got[0])
	}
	if got[1] != nil {
		t.Fatalf("exec paths = %v, want nil", got[1])
	}
	if len(next.events) != 2 {
		t.Fatalf("forwarded events = %d, want 2", len(next.events))
	}
}

func TestFSChangeEventSinkSkipsFailuresStartsAndOtherTools(t *testing.T) {
	var calls int
	next := &recordingEventSink{}
	sink := newFSChangeEventSink(next, func([]string) { calls++ })

	sink.EmitStreamEvent(event.StreamEvent{
		Type:     event.ToolCallEnd,
		ToolName: "write",
		Input:    map[string]any{"path": "/data/a.txt"},
		Error:    "failed",
	})
	sink.EmitStreamEvent(event.StreamEvent{
		Type:     event.ToolCallStart,
		ToolName: "write",
		Input:    map[string]any{"path": "/data/a.txt"},
	})
	sink.EmitStreamEvent(event.StreamEvent{
		Type:     event.ToolCallEnd,
		ToolName: "read",
		Input:    map[string]any{"path": "/data/a.txt"},
	})

	if calls != 0 {
		t.Fatalf("notify calls = %d, want 0", calls)
	}
	if len(next.events) != 3 {
		t.Fatalf("forwarded events = %d, want 3", len(next.events))
	}
}

func TestFSChangeEventSinkNilNotifyPassesThrough(t *testing.T) {
	next := &recordingEventSink{}
	sink := newFSChangeEventSink(next, nil)
	sink.EmitStreamEvent(event.StreamEvent{Type: event.ToolCallEnd, ToolName: "write"})
	if len(next.events) != 1 {
		t.Fatalf("forwarded events = %d, want 1", len(next.events))
	}
}
