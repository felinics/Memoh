package discuss

import (
	"strings"
	"sync"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/channel/gateway"
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
func (p *discussEventProjector) Broadcast(botID string, event agentdomain.StreamEvent) {
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

func agentEventToChannelEvent(event agentdomain.StreamEvent) (gateway.StreamEvent, bool) {
	switch event.Type {
	case agentdomain.AgentStart:
		return gateway.StreamEvent{Type: gateway.StreamEventAgentStart}, true
	case agentdomain.TextStart:
		return gateway.StreamEvent{Type: gateway.StreamEventPhaseStart, Phase: gateway.StreamPhaseText}, true
	case agentdomain.TextDelta:
		return gateway.StreamEvent{Type: gateway.StreamEventDelta, Delta: event.Delta}, true
	case agentdomain.TextEnd:
		return gateway.StreamEvent{Type: gateway.StreamEventPhaseEnd, Phase: gateway.StreamPhaseText}, true
	case agentdomain.ReasoningStart:
		return gateway.StreamEvent{Type: gateway.StreamEventPhaseStart, Phase: gateway.StreamPhaseReasoning}, true
	case agentdomain.ReasoningDelta:
		return gateway.StreamEvent{Type: gateway.StreamEventDelta, Delta: event.Delta, Phase: gateway.StreamPhaseReasoning}, true
	case agentdomain.ReasoningEnd:
		return gateway.StreamEvent{Type: gateway.StreamEventPhaseEnd, Phase: gateway.StreamPhaseReasoning}, true
	case agentdomain.ToolCallStart:
		return gateway.StreamEvent{
			Type:     gateway.StreamEventToolCallStart,
			ToolCall: &gateway.StreamToolCall{Name: event.ToolName, CallID: event.ToolCallID, Input: event.Input},
		}, true
	case agentdomain.ToolCallEnd:
		return gateway.StreamEvent{
			Type: gateway.StreamEventToolCallEnd,
			ToolCall: &gateway.StreamToolCall{
				Name: event.ToolName, CallID: event.ToolCallID, Input: event.Input, Result: event.Result,
			},
		}, true
	case agentdomain.ToolApprovalRequest:
		return gateway.StreamEvent{
			Type: gateway.StreamEventToolCallStart,
			ToolCall: &gateway.StreamToolCall{
				Name:       strings.TrimSpace(event.ToolName),
				CallID:     strings.TrimSpace(event.ToolCallID),
				Input:      event.Input,
				ApprovalID: strings.TrimSpace(event.ApprovalID),
				ShortID:    event.ShortID,
				Actions: []gateway.Action{
					{Type: "tool_approval", Label: "Approve", Value: "approve:" + strings.TrimSpace(event.ApprovalID)},
					{Type: "tool_approval", Label: "Reject", Value: "reject:" + strings.TrimSpace(event.ApprovalID)},
				},
			},
		}, true
	case agentdomain.UserInputRequest:
		userInputID := strings.TrimSpace(event.UserInputID)
		if userInputID == "" {
			userInputID = strings.TrimSpace(event.ApprovalID)
		}
		return gateway.StreamEvent{
			Type: gateway.StreamEventToolCallStart,
			ToolCall: &gateway.StreamToolCall{
				Name:   strings.TrimSpace(event.ToolName),
				CallID: strings.TrimSpace(event.ToolCallID),
				Input: map[string]any{
					"user_input_id": userInputID,
					"short_id":      event.ShortID,
					"status":        strings.TrimSpace(event.Status),
					"payload":       event.Input,
				},
				ShortID: event.ShortID,
				Actions: []gateway.Action{
					{Type: "user_input", Label: "Respond", Value: "respond:" + userInputID},
				},
			},
		}, true
	case agentdomain.AgentEnd, agentdomain.AgentAbort:
		return gateway.StreamEvent{Type: gateway.StreamEventAgentEnd}, true
	case agentdomain.Error:
		return gateway.StreamEvent{Type: gateway.StreamEventError, Error: event.Error}, true
	default:
		return gateway.StreamEvent{}, false
	}
}
