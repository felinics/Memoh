package local

import (
	"context"
	"errors"
	"strings"

	"github.com/memohai/memoh/domains/channel/gateway"
)

// WebAdapter implements gateway.Sender for the local Web channel.
type WebAdapter struct {
	hub *RouteHub
}

// NewWebAdapter creates a WebAdapter backed by the given route hub.
func NewWebAdapter(hub *RouteHub) *WebAdapter {
	return &WebAdapter{hub: hub}
}

// Type returns the Web channel type.
func (*WebAdapter) Type() gateway.ChannelType {
	return WebType
}

// Descriptor returns the Web channel metadata.
func (*WebAdapter) Descriptor() gateway.Descriptor {
	return gateway.Descriptor{
		Type:        WebType,
		DisplayName: "Web",
		Configless:  true,
		Capabilities: gateway.ChannelCapabilities{
			Text:            true,
			Markdown:        true,
			Reply:           true,
			Attachments:     true,
			Streaming:       true,
			NativeUserInput: true,
			BlockStreaming:  true,
		},
		TargetSpec: gateway.TargetSpec{
			Format: "bot_id",
			Hints: []gateway.TargetHint{
				{Label: "Bot ID", Example: "bot_123"},
			},
		},
	}
}

// Send publishes an outbound message to the Web route hub.
func (a *WebAdapter) Send(_ context.Context, _ gateway.ChannelConfig, msg gateway.PreparedOutboundMessage) error {
	if a.hub == nil {
		return errors.New("web hub not configured")
	}
	target := strings.TrimSpace(msg.Target)
	if target == "" {
		return errors.New("web target is required")
	}
	logical := msg.LogicalMessage()
	if logical.Message.IsEmpty() {
		return errors.New("message is required")
	}
	a.hub.Publish(target, logical)
	return nil
}

// OpenStream opens a local stream session bound to the target route.
func (a *WebAdapter) OpenStream(ctx context.Context, _ gateway.ChannelConfig, target string, _ gateway.StreamOptions) (gateway.PreparedOutboundStream, error) {
	if a.hub == nil {
		return nil, errors.New("web hub not configured")
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, errors.New("web target is required")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return newLocalOutboundStream(a.hub, target), nil
}
