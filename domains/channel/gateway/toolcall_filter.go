package gateway

import "context"

// toolCallDroppingStream drops ordinary tool_call_start / tool_call_end events
// while forwarding every other event to the wrapped primary stream unchanged.
// Approval and ask_user starts are preserved because they carry IM interaction
// buttons. This is used to gate IM-facing streams when a bot's
// show_tool_calls_in_im setting is off: the IM adapter stops receiving tool
// lifecycle events, but any upstream TeeStream observer (e.g. the WebUI hub)
// still sees them because the tee mirrors events independently.
type toolCallDroppingStream struct {
	primary OutboundStream
}

// NewToolCallDroppingStream wraps primary and drops non-interactive
// tool_call_start / tool_call_end events. When primary is nil the function
// returns nil.
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
	if event.Type == StreamEventToolCallStart && event.ToolCall != nil &&
		(event.ToolCall.ApprovalID != "" || hasUserInputAction(event.ToolCall.Actions)) {
		return s.primary.Push(ctx, event)
	}
	if event.Type == StreamEventToolCallStart || event.Type == StreamEventToolCallEnd {
		return nil
	}
	return s.primary.Push(ctx, event)
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
