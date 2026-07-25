package email

import (
	"context"

	"github.com/memohai/memoh/domains/channel"
)

// BoundaryProvider adapts the Channel Email Runtime to the process-boundary
// EmailProvider contract. Root command/result DTOs stay in domains/channel;
// this type performs explicit field mapping (no type aliases).
type BoundaryProvider struct {
	Runtime Runtime
}

func (p BoundaryProvider) RefreshEmailProvider(ctx context.Context, cmd channel.RefreshEmailProviderCommand) error {
	if p.Runtime == nil {
		return channel.ErrUnknown
	}
	return p.Runtime.RefreshProvider(ctx, cmd.ProviderID)
}

func (p BoundaryProvider) SendEmail(ctx context.Context, cmd channel.SendEmailCommand) (channel.SendEmailResult, error) {
	if p.Runtime == nil {
		return channel.SendEmailResult{}, channel.ErrUnknown
	}
	messageID, err := p.Runtime.SendEmail(ctx, cmd.BotID, cmd.ProviderID, OutboundEmail{
		To:      append([]string(nil), cmd.To...),
		Subject: cmd.Subject,
		Body:    cmd.Body,
		HTML:    cmd.HTML,
	})
	if err != nil {
		return channel.SendEmailResult{}, err
	}
	return channel.SendEmailResult{MessageID: messageID}, nil
}
