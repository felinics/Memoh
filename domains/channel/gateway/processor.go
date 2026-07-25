package gateway

import "context"

// InboundProcessor handles inbound messages and replies through the given sender.
type InboundProcessor interface {
	HandleInbound(ctx context.Context, cfg ChannelConfig, msg InboundMessage, sender StreamReplySender) error
}
