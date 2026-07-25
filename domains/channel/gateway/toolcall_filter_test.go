package gateway

import (
	"context"
	"errors"
	"testing"
)

type recordingOutboundStream struct {
	events []StreamEvent
	closed bool
	err    error
}

func (r *recordingOutboundStream) Push(_ context.Context, event StreamEvent) error {
	if r.err != nil {
		return r.err
	}
	r.events = append(r.events, event)
	return nil
}

func (r *recordingOutboundStream) Close(_ context.Context) error {
	r.closed = true
	return nil
}

func TestToolCallDroppingStreamFiltersToolEvents(t *testing.T) {
	t.Parallel()

	sink := &recordingOutboundStream{}
	stream := NewToolCallDroppingStream(sink)
	if stream == nil {
		t.Fatalf("expected non-nil wrapper")
	}

	ctx := context.Background()
	events := []StreamEvent{
		{Type: StreamEventDelta, Delta: "hi"},
		{Type: StreamEventToolCallStart, ToolCall: &StreamToolCall{Name: "read", CallID: "c1"}},
		{Type: StreamEventToolCallStart, ToolCall: &StreamToolCall{Name: "ask_user", CallID: "c2", Actions: []Action{{Type: "user_input", Value: "respond:input-1"}}}},
		{Type: StreamEventToolCallEnd, ToolCall: &StreamToolCall{Name: "read", CallID: "c1"}},
		{Type: StreamEventStatus, Status: StreamStatusCompleted},
	}
	for _, e := range events {
		if err := stream.Push(ctx, e); err != nil {
			t.Fatalf("push %s: %v", e.Type, err)
		}
	}
	if len(sink.events) != 3 {
		t.Fatalf("expected 3 forwarded events, got %d: %+v", len(sink.events), sink.events)
	}
	if sink.events[0].Type != StreamEventDelta {
		t.Fatalf("expected delta first, got %s", sink.events[0].Type)
	}
	if sink.events[1].Type != StreamEventToolCallStart || sink.events[1].ToolCall == nil || sink.events[1].ToolCall.Name != "ask_user" {
		t.Fatalf("expected ask_user user-input tool call second, got %#v", sink.events[1])
	}
	if sink.events[2].Type != StreamEventStatus {
		t.Fatalf("expected status third, got %s", sink.events[2].Type)
	}

	if err := stream.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !sink.closed {
		t.Fatalf("expected primary close to be called")
	}
}

func TestToolCallDroppingStreamForwardsPrimaryError(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	stream := NewToolCallDroppingStream(&recordingOutboundStream{err: boom})
	err := stream.Push(context.Background(), StreamEvent{Type: StreamEventDelta, Delta: "x"})
	if !errors.Is(err, boom) {
		t.Fatalf("expected primary error to surface, got %v", err)
	}

	// tool events should still be dropped silently without calling the primary
	if err := stream.Push(context.Background(), StreamEvent{Type: StreamEventToolCallStart}); err != nil {
		t.Fatalf("tool event should not propagate primary error, got %v", err)
	}
}

func TestNewToolCallDroppingStreamNilPrimary(t *testing.T) {
	t.Parallel()

	if got := NewToolCallDroppingStream(nil); got != nil {
		t.Fatalf("expected nil wrapper when primary is nil, got %T", got)
	}
}
