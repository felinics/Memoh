package channel

import (
	"context"
	"strings"

	"github.com/felinics/memoh/internal/markdownmedia"
)

// toolCallDroppingStream hides ordinary tool calls from the wrapped primary
// stream while forwarding every other event unchanged. An ordinary
// tool_call_start becomes a segment_end, so the adapter still closes the text
// it streamed before the tool call as its own message; tool_call_end is
// dropped. Approval and ask_user starts are preserved because they carry IM
// interaction buttons. This is used to gate IM-facing streams when a bot's
// show_tool_calls_in_im setting is off: the IM adapter stops receiving tool
// lifecycle events, but any upstream TeeStream observer (e.g. the WebUI hub)
// still sees them because the tee mirrors events independently.
//
// A final whose text equals a segment already closed this way is dropped:
// runtimes that keep pre-tool text as its own assistant output (for example
// when reasoning separates it from the tool call) would otherwise deliver it
// twice.
type toolCallDroppingStream struct {
	primary   OutboundStream
	segment   strings.Builder
	committed []string
}

// NewToolCallDroppingStream wraps primary, replaces non-interactive
// tool_call_start events with segment_end and drops tool_call_end events.
// When primary is nil the function returns nil.
func NewToolCallDroppingStream(primary OutboundStream) OutboundStream {
	if primary == nil {
		return nil
	}
	return &toolCallDroppingStream{primary: primary}
}

func (s *toolCallDroppingStream) Push(ctx context.Context, event StreamEvent) error {
	if s == nil || s.primary == nil {
		return nil
	}
	switch event.Type {
	case StreamEventToolCallStart:
		if event.ToolCall != nil &&
			(event.ToolCall.ApprovalID != "" || hasUserInputAction(event.ToolCall.Actions)) {
			return s.primary.Push(ctx, event)
		}
		s.closeSegment()
		return s.primary.Push(ctx, StreamEvent{Type: StreamEventSegmentEnd})
	case StreamEventToolCallEnd:
		return nil
	case StreamEventSegmentEnd:
		s.closeSegment()
	case StreamEventDelta:
		if event.Phase != StreamPhaseReasoning {
			s.segment.WriteString(event.Delta)
		}
	case StreamEventReset:
		s.segment.Reset()
	case StreamEventFinal:
		s.segment.Reset()
		if s.isCommittedFinal(event.Final) {
			return nil
		}
	}
	return s.primary.Push(ctx, event)
}

func (s *toolCallDroppingStream) closeSegment() {
	if text := strings.TrimSpace(s.segment.String()); text != "" {
		s.committed = append(s.committed, text)
	}
	s.segment.Reset()
}

// isCommittedFinal reports whether a final only repeats text the adapter
// already delivered as a closed segment.
func (s *toolCallDroppingStream) isCommittedFinal(final *StreamFinalizePayload) bool {
	if final == nil || len(final.Message.Attachments) > 0 || len(final.Message.Actions) > 0 {
		return false
	}
	// A final that publishes media also delivers what the outbound stream
	// withheld; that stream drops the repeated text itself.
	if _, ok := final.Message.Metadata[markdownmedia.MetadataKey]; ok {
		return false
	}
	if _, ok := final.Message.Metadata[markdownmedia.EarlierMetadataKey]; ok {
		return false
	}
	text := strings.TrimSpace(final.Message.PlainText())
	if text == "" {
		return false
	}
	for _, committed := range s.committed {
		if committed == text {
			return true
		}
	}
	return false
}

func (s *toolCallDroppingStream) Close(ctx context.Context) error {
	if s == nil || s.primary == nil {
		return nil
	}
	return s.primary.Close(ctx)
}

func hasUserInputAction(actions []Action) bool {
	for _, action := range actions {
		if action.Type == "user_input" {
			return true
		}
	}
	return false
}
