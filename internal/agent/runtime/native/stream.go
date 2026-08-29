package native

import "github.com/felinics/memoh/internal/agent/event"

// Stream event aliases keep existing consumers source-compatible while the
// shared event types live in internal/agent/event.

// StreamEventType identifies the kind of stream event.
type StreamEventType = event.StreamEventType

// StreamEvent is emitted by the agent during streaming.
type StreamEvent = event.StreamEvent

const (
	EventAgentStart          = event.AgentStart
	EventStart               = event.AgentStart
	EventTextStart           = event.TextStart
	EventTextDelta           = event.TextDelta
	EventTextEnd             = event.TextEnd
	EventReasoningStart      = event.ReasoningStart
	EventReasoningDelta      = event.ReasoningDelta
	EventReasoningEnd        = event.ReasoningEnd
	EventToolCallInputStart  = event.ToolCallInputStart
	EventToolCallStart       = event.ToolCallStart
	EventToolCallMetadata    = event.ToolCallMetadata
	EventToolCallProgress    = event.ToolCallProgress
	EventToolCallEnd         = event.ToolCallEnd
	EventToolApprovalRequest = event.ToolApprovalRequest
	EventUserInputRequest    = event.UserInputRequest
	EventAttachment          = event.Attachment
	EventReaction            = event.Reaction
	EventSpeech              = event.Speech
	EventAgentEnd            = event.AgentEnd
	EventEnd                 = event.AgentEnd
	EventAgentAbort          = event.AgentAbort
	EventAbort               = event.AgentAbort
	EventRetry               = event.Retry
	EventProgress            = event.Progress
	EventError               = event.Error
)
