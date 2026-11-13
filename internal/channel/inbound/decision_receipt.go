package inbound

import (
	"context"
	"strings"

	"github.com/felinics/memoh/internal/channel"
)

// Acceptance is a control receipt, not model output. Defer card finalization
// until stream completion so buttons stay retryable on mid-flight interruptions.
type decisionReplySender struct {
	channel.StreamReplySender
	accepted  func(context.Context, string)
	finalized func(context.Context)
	refreshed func(string) bool
}

func (s decisionReplySender) AcceptDecision(ctx context.Context, id string) {
	if s.accepted != nil {
		s.accepted(ctx, id)
	}
}

func (s decisionReplySender) FinalizeDecision(ctx context.Context) {
	if s.finalized != nil {
		s.finalized(ctx)
	}
}

func (s decisionReplySender) DecisionCardRefreshed(id string) bool {
	return s.refreshed != nil && s.refreshed(id)
}

func (p *ChannelInboundProcessor) withDecisionReceipts(sender channel.StreamReplySender, cfg channel.ChannelConfig) channel.StreamReplySender {
	var acceptedID, refreshedID string
	return decisionReplySender{
		StreamReplySender: sender,
		accepted: func(_ context.Context, id string) {
			if trimmed := strings.TrimSpace(id); trimmed != "" {
				acceptedID = trimmed
			}
		},
		finalized: func(ctx context.Context) {
			if acceptedID != "" && p.updateUserInputCard(ctx, cfg, acceptedID, p.localizer(ctx, cfg.BotID)) {
				refreshedID = acceptedID
			}
		},
		refreshed: func(id string) bool { return refreshedID != "" && refreshedID == strings.TrimSpace(id) },
	}
}
