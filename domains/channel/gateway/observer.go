package gateway

import "context"

// StreamObserver receives copies of stream events for cross-channel broadcasting
// or external notification (e.g. webhooks). Implementations must be safe for
// concurrent use and should not block.
type StreamObserver interface {
	// OnStreamEvent is called for every event pushed to an outbound stream.
	// botID identifies the bot that owns the conversation.
	// source is the channel type that originated the event (e.g. "telegram").
	OnStreamEvent(ctx context.Context, botID string, source ChannelType, event StreamEvent)
}

// teeStream wraps an OutboundStream and mirrors every Push call to an observer.
type teeStream struct {
	primary  OutboundStream
	observer StreamObserver
	botID    string
	source   ChannelType
}

// NewTeeStream wraps primary so that every Push also notifies observer.
func NewTeeStream(primary OutboundStream, observer StreamObserver, botID string, source ChannelType) OutboundStream {
	if observer == nil {
		return primary
	}
	return &teeStream{
		primary:  primary,
		observer: observer,
		botID:    botID,
		source:   source,
	}
}

func (t *teeStream) Push(ctx context.Context, event StreamEvent) error {
	err := t.primary.Push(ctx, event)
	// Notify observer regardless of push error — the event was produced and
	// should still be visible in monitoring/WebUI even if the primary channel
	// delivery failed (e.g. Telegram rate-limit).
	t.observer.OnStreamEvent(ctx, t.botID, t.source, event)
	return err
}

func (t *teeStream) Close(ctx context.Context) error {
	return t.primary.Close(ctx)
}
