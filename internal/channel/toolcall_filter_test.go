package channel

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
		{Type: StreamEventToolCallStart, ToolCall: &StreamToolCall{Name: "exec", CallID: "c3", ApprovalID: "approval-1"}},
		{Type: StreamEventStatus, Status: StreamStatusCompleted},
	}
	for _, e := range events {
		if err := stream.Push(ctx, e); err != nil {
			t.Fatalf("push %s: %v", e.Type, err)
		}
	}
	if len(sink.events) != 5 {
		t.Fatalf("expected 5 forwarded events, got %d: %+v", len(sink.events), sink.events)
	}
	if sink.events[0].Type != StreamEventDelta {
		t.Fatalf("expected delta first, got %s", sink.events[0].Type)
	}
	if sink.events[1].Type != StreamEventSegmentEnd || sink.events[1].ToolCall != nil {
		t.Fatalf("expected ordinary tool call start to become a bare segment end, got %#v", sink.events[1])
	}
	if sink.events[2].Type != StreamEventToolCallStart || sink.events[2].ToolCall == nil || sink.events[2].ToolCall.Name != "ask_user" {
		t.Fatalf("expected ask_user user-input tool call third, got %#v", sink.events[2])
	}
	if sink.events[3].Type != StreamEventToolCallStart || sink.events[3].ToolCall == nil || sink.events[3].ToolCall.ApprovalID != "approval-1" {
		t.Fatalf("expected approval tool call fourth, got %#v", sink.events[3])
	}
	if sink.events[4].Type != StreamEventStatus {
		t.Fatalf("expected status last, got %s", sink.events[4].Type)
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

	// tool_call_end should still be dropped silently without calling the primary
	if err := stream.Push(context.Background(), StreamEvent{Type: StreamEventToolCallEnd}); err != nil {
		t.Fatalf("tool call end should not propagate primary error, got %v", err)
	}
	// tool_call_start is forwarded as segment_end, so the primary error surfaces
	if err := stream.Push(context.Background(), StreamEvent{Type: StreamEventToolCallStart}); !errors.Is(err, boom) {
		t.Fatalf("expected segment end to surface primary error, got %v", err)
	}
}

func TestNewToolCallDroppingStreamNilPrimary(t *testing.T) {
	t.Parallel()

	if got := NewToolCallDroppingStream(nil); got != nil {
		t.Fatalf("expected nil wrapper when primary is nil, got %T", got)
	}
}

func TestToolCallDroppingStreamDropsFinalRepeatingClosedSegment(t *testing.T) {
	t.Parallel()

	sink := &recordingOutboundStream{}
	stream := NewToolCallDroppingStream(sink)
	ctx := context.Background()
	// Pre-tool text kept as its own assistant output (e.g. reasoning sat
	// between the text and the tool call) must not be delivered twice.
	events := []StreamEvent{
		{Type: StreamEventDelta, Delta: "Let me check.", Phase: StreamPhaseText},
		{Type: StreamEventDelta, Delta: "thinking", Phase: StreamPhaseReasoning},
		{Type: StreamEventToolCallStart, ToolCall: &StreamToolCall{Name: "exec", CallID: "c1"}},
		{Type: StreamEventToolCallEnd, ToolCall: &StreamToolCall{Name: "exec", CallID: "c1"}},
		{Type: StreamEventDelta, Delta: "Done.", Phase: StreamPhaseText},
		{Type: StreamEventFinal, Final: &StreamFinalizePayload{Message: Message{Text: " Let me check. "}}},
		{Type: StreamEventFinal, Final: &StreamFinalizePayload{Message: Message{Text: "Done."}}},
	}
	for _, event := range events {
		if err := stream.Push(ctx, event); err != nil {
			t.Fatalf("push %s: %v", event.Type, err)
		}
	}

	var finals []string
	for _, event := range sink.events {
		if event.Type == StreamEventFinal {
			finals = append(finals, event.Final.Message.PlainText())
		}
	}
	if len(finals) != 1 || finals[0] != "Done." {
		t.Fatalf("finals = %q, want only the post-tool text", finals)
	}
}

func TestToolCallDroppingStreamKeepsFinalWithAttachmentsOrNewText(t *testing.T) {
	t.Parallel()

	sink := &recordingOutboundStream{}
	stream := NewToolCallDroppingStream(sink)
	ctx := context.Background()
	events := []StreamEvent{
		{Type: StreamEventDelta, Delta: "draft", Phase: StreamPhaseText},
		{Type: StreamEventReset},
		{Type: StreamEventDelta, Delta: "Here it is.", Phase: StreamPhaseText},
		{Type: StreamEventToolCallStart, ToolCall: &StreamToolCall{Name: "read", CallID: "c1"}},
		{Type: StreamEventFinal, Final: &StreamFinalizePayload{Message: Message{Text: "draft"}}},
		{Type: StreamEventFinal, Final: &StreamFinalizePayload{Message: Message{
			Text:        "Here it is.",
			Attachments: []Attachment{{Type: AttachmentFile, URL: "https://example.com/a.txt"}},
		}}},
	}
	for _, event := range events {
		if err := stream.Push(ctx, event); err != nil {
			t.Fatalf("push %s: %v", event.Type, err)
		}
	}

	finals := 0
	for _, event := range sink.events {
		if event.Type == StreamEventFinal {
			finals++
		}
	}
	if finals != 2 {
		t.Fatalf("finals = %d, want 2 (reset text and attachment final are not duplicates)", finals)
	}
}
