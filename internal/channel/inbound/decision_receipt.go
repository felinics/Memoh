package inbound

import (
	"context"
	"log/slog"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/i18n"
)

// Acceptance is a control receipt, not model output. Keep the configuration at
// ingress so the stream can refresh the original card before a slow continuation.
type decisionReplySender struct {
	channel.StreamReplySender
	accepted func(context.Context, string)
}

func (s decisionReplySender) AcceptDecision(ctx context.Context, id string) { s.accepted(ctx, id) }

func (p *ChannelInboundProcessor) withDecisionReceipts(sender channel.StreamReplySender, cfg channel.ChannelConfig) channel.StreamReplySender {
	return decisionReplySender{StreamReplySender: sender, accepted: func(ctx context.Context, id string) {
		if p.registry == nil {
			return
		}
		adapter, ok := p.registry.Get(cfg.ChannelType)
		if !ok {
			return
		}
		updater, ok := adapter.(interface {
			UpdateUserInputCard(context.Context, channel.ChannelConfig, string, *i18n.Localizer) (bool, error)
		})
		if !ok {
			return
		}
		if _, err := updater.UpdateUserInputCard(ctx, cfg, id, p.localizer(ctx, cfg.BotID)); err != nil && p.logger != nil {
			p.logger.Warn("refresh accepted decision card failed", slog.Any("error", err))
		}
	}}
}
