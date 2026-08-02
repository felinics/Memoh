package channel

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

const (
	// InternalSendToolCallIDMetadataKey is trusted server metadata used only to
	// coordinate an explicit send tool call with its Telegram preview stream.
	InternalSendToolCallIDMetadataKey = "memoh.internal_send_tool_call_id"
	sendToolStreamAttachTimeout       = 1200 * time.Millisecond
	sendToolStreamTombstoneTTL        = 30 * time.Second
)

type SendToolStreamKey struct {
	BotID      string
	Platform   ChannelType
	Target     string
	ToolCallID string
}

func (k SendToolStreamKey) normalized() SendToolStreamKey {
	return SendToolStreamKey{
		BotID:      strings.TrimSpace(k.BotID),
		Platform:   ChannelType(strings.ToLower(strings.TrimSpace(k.Platform.String()))),
		Target:     strings.TrimSpace(k.Target),
		ToolCallID: strings.TrimSpace(k.ToolCallID),
	}
}

func (k SendToolStreamKey) valid() bool {
	return k.BotID != "" && k.Platform != "" && k.Target != "" && k.ToolCallID != ""
}

type sendToolStreamEntry struct {
	mu       sync.Mutex
	ready    chan struct{}
	stream   OutboundStream
	attached bool
	finished bool
}

// SendToolStreamCoordinator serializes preview deltas and final send delivery
// for a tool call. It lives in the Channel process, so it works in both the
// embedded and split Server/Channel deployments.
type SendToolStreamCoordinator struct {
	mu      sync.Mutex
	entries map[SendToolStreamKey]*sendToolStreamEntry
}

func NewSendToolStreamCoordinator() *SendToolStreamCoordinator {
	return &SendToolStreamCoordinator{entries: make(map[SendToolStreamKey]*sendToolStreamEntry)}
}

func (c *SendToolStreamCoordinator) entry(key SendToolStreamKey) (*sendToolStreamEntry, bool) {
	if c == nil {
		return nil, false
	}
	key = key.normalized()
	if !key.valid() {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[key]
	if entry == nil {
		entry = &sendToolStreamEntry{ready: make(chan struct{})}
		c.entries[key] = entry
	}
	return entry, true
}

// Attach transfers stream ownership to the coordinator. False means final
// delivery already fell back to a one-shot send, so the caller must close the
// newly opened preview instead of exposing a stale partial message.
func (c *SendToolStreamCoordinator) Attach(key SendToolStreamKey, stream OutboundStream) bool {
	entry, ok := c.entry(key)
	if !ok || stream == nil {
		return false
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.finished || entry.attached {
		return false
	}
	entry.stream = stream
	entry.attached = true
	close(entry.ready)
	return true
}

func (c *SendToolStreamCoordinator) PushDelta(ctx context.Context, key SendToolStreamKey, delta string) error {
	if delta == "" {
		return nil
	}
	entry, ok := c.entry(key)
	if !ok {
		return errors.New("send preview key is invalid")
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.finished || entry.stream == nil {
		return nil
	}
	return entry.stream.Push(ctx, StreamEvent{Type: StreamEventDelta, Delta: delta, Phase: StreamPhaseText})
}

// Finalize returns handled=true only when the final message was committed
// through an attached preview. If no preview arrives promptly, it leaves
// handled=false so Manager.Send performs the normal one-shot send.
func (c *SendToolStreamCoordinator) Finalize(ctx context.Context, key SendToolStreamKey, message Message) (bool, error) {
	entry, ok := c.entry(key)
	if !ok {
		return false, nil
	}
	timer := time.NewTimer(sendToolStreamAttachTimeout)
	defer timer.Stop()
	select {
	case <-entry.ready:
	case <-timer.C:
		entry.mu.Lock()
		entry.finished = true
		entry.mu.Unlock()
		c.expire(key)
		return false, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}

	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.finished || entry.stream == nil {
		return false, nil
	}
	entry.finished = true
	if err := entry.stream.Push(ctx, StreamEvent{
		Type:  StreamEventFinal,
		Final: &StreamFinalizePayload{Message: message},
	}); err != nil {
		_ = entry.stream.Close(context.WithoutCancel(ctx))
		c.expire(key)
		return false, err
	}
	_ = entry.stream.Push(ctx, StreamEvent{Type: StreamEventStatus, Status: StreamStatusCompleted})
	// The final message has already been committed. A close error must never
	// trigger Manager.Send's one-shot fallback, which would duplicate the text.
	_ = entry.stream.Close(ctx)
	c.remove(key)
	return true, nil
}

func (c *SendToolStreamCoordinator) Abort(ctx context.Context, key SendToolStreamKey) {
	entry, ok := c.entry(key)
	if !ok {
		return
	}
	entry.mu.Lock()
	if !entry.finished {
		entry.finished = true
		if entry.stream != nil {
			_ = entry.stream.Push(ctx, StreamEvent{Type: StreamEventError, Error: "send was not completed"})
			_ = entry.stream.Close(ctx)
		}
	}
	entry.mu.Unlock()
	c.remove(key)
}

func (c *SendToolStreamCoordinator) remove(key SendToolStreamKey) {
	if c == nil {
		return
	}
	key = key.normalized()
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}

func (c *SendToolStreamCoordinator) expire(key SendToolStreamKey) {
	time.AfterFunc(sendToolStreamTombstoneTTL, func() { c.remove(key) })
}

func takeInternalSendToolCallID(message *Message) string {
	if message == nil || len(message.Metadata) == 0 {
		return ""
	}
	value, _ := message.Metadata[InternalSendToolCallIDMetadataKey].(string)
	metadata := make(map[string]any, len(message.Metadata)-1)
	for key, item := range message.Metadata {
		if key != InternalSendToolCallIDMetadataKey {
			metadata[key] = item
		}
	}
	if len(metadata) == 0 {
		metadata = nil
	}
	message.Metadata = metadata
	return strings.TrimSpace(value)
}
