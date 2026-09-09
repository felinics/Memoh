package discuss

import (
	"strings"
	"sync"

	agentevent "github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/channel"
)

type discussEventProjector struct {
	mu          sync.RWMutex
	broadcaster DiscussStreamBroadcaster
}

func newDiscussEventProjector(broadcaster DiscussStreamBroadcaster) *discussEventProjector {
	return &discussEventProjector{broadcaster: broadcaster}
}

func (p *discussEventProjector) SetBroadcaster(broadcaster DiscussStreamBroadcaster) {
	p.mu.Lock()
	p.broadcaster = broadcaster
	p.mu.Unlock()
}

// Broadcast maps Agent events to the stable Channel stream contract.
func (p *discussEventProjector) Broadcast(botID string, event agentevent.StreamEvent) {
	streamEvent, ok := agentEventToChannelEvent(event)
	if !ok {
		return
	}
	p.mu.RLock()
	broadcaster := p.broadcaster
	p.mu.RUnlock()
	if broadcaster != nil {
		broadcaster.PublishEvent(botID, streamEvent)
	}
}

func agentEventToChannelEvent(event agentevent.StreamEvent) (channel.StreamEvent, bool) {
	switch event.Type {
	case agentevent.AgentStart:
		return channel.StreamEvent{Type: channel.StreamEventAgentStart}, true
	case agentevent.TextStart:
		return channel.StreamEvent{Type: channel.StreamEventPhaseStart, Phase: channel.StreamPhaseText}, true
	case agentevent.TextDelta:
		return channel.StreamEvent{Type: channel.StreamEventDelta, Delta: event.Delta}, true
	case agentevent.TextEnd:
		return channel.StreamEvent{Type: channel.StreamEventPhaseEnd, Phase: channel.StreamPhaseText}, true
	case agentevent.ReasoningStart:
		return channel.StreamEvent{Type: channel.StreamEventPhaseStart, Phase: channel.StreamPhaseReasoning}, true
	case agentevent.ReasoningDelta:
		return channel.StreamEvent{Type: channel.StreamEventDelta, Delta: event.Delta, Phase: channel.StreamPhaseReasoning}, true
	case agentevent.ReasoningEnd:
		return channel.StreamEvent{Type: channel.StreamEventPhaseEnd, Phase: channel.StreamPhaseReasoning}, true
	case agentevent.ToolCallStart:
		return channel.StreamEvent{
			Type:     channel.StreamEventToolCallStart,
			ToolCall: &channel.StreamToolCall{Name: event.ToolName, CallID: event.ToolCallID, Input: event.Input},
		}, true
	case agentevent.ToolCallEnd:
		return channel.StreamEvent{
			Type: channel.StreamEventToolCallEnd,
			ToolCall: &channel.StreamToolCall{
				Name: event.ToolName, CallID: event.ToolCallID, Input: event.Input, Result: event.Result,
			},
		}, true
	case agentevent.ToolApprovalRequest:
		return channel.StreamEvent{
			Type: channel.StreamEventToolCallStart,
			ToolCall: &channel.StreamToolCall{
				Name:       strings.TrimSpace(event.ToolName),
				CallID:     strings.TrimSpace(event.ToolCallID),
				Input:      event.Input,
				ApprovalID: strings.TrimSpace(event.ApprovalID),
				ShortID:    event.ShortID,
				Actions: []channel.Action{
					{Type: "tool_approval", Label: "Approve", Value: "approve:" + strings.TrimSpace(event.ApprovalID)},
					{Type: "tool_approval", Label: "Reject", Value: "reject:" + strings.TrimSpace(event.ApprovalID)},
				},
			},
		}, true
	case agentevent.UserInputRequest:
		userInputID := strings.TrimSpace(event.UserInputID)
		if userInputID == "" {
			userInputID = strings.TrimSpace(event.ApprovalID)
		}
		return channel.StreamEvent{
			Type: channel.StreamEventToolCallStart,
			ToolCall: &channel.StreamToolCall{
				Name:   strings.TrimSpace(event.ToolName),
				CallID: strings.TrimSpace(event.ToolCallID),
				Input: map[string]any{
					"user_input_id": userInputID,
					"short_id":      event.ShortID,
					"status":        strings.TrimSpace(event.Status),
					"payload":       event.Input,
				},
				ShortID: event.ShortID,
				Actions: []channel.Action{
					{Type: "user_input", Label: "Respond", Value: "respond:" + userInputID},
				},
			},
		}, true
	case agentevent.AgentEnd, agentevent.AgentAbort:
		return channel.StreamEvent{Type: channel.StreamEventAgentEnd}, true
	case agentevent.Error:
		return channel.StreamEvent{Type: channel.StreamEventError, Error: event.Error}, true
	default:
		return channel.StreamEvent{}, false
	}
}
