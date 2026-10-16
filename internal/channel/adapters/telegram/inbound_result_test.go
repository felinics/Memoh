package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/logger"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureReceiver records the handler the manager hands to a connection.
type captureReceiver struct{ handler channel.InboundHandler }

func (*captureReceiver) Type() channel.ChannelType { return channel.ChannelType("capture") }
func (*captureReceiver) Descriptor() channel.Descriptor {
	return channel.Descriptor{Type: channel.ChannelType("capture"), DisplayName: "Capture"}
}

func (r *captureReceiver) Connect(_ context.Context, cfg channel.ChannelConfig, handler channel.InboundHandler) (channel.Connection, error) {
	r.handler = handler
	return channel.NewConnection(cfg, func(context.Context) error { return nil }), nil
}

type failingProcessor struct{}

func (failingProcessor) HandleInbound(context.Context, channel.ChannelConfig, channel.InboundMessage, channel.StreamReplySender) error {
	return errors.New("resolve route: connection reset")
}

// A failed message leaves only the inbound unit's result line; the adapter
// does not log the error the handler returned.
func TestDispatchInboundFailureHasOneRecord(t *testing.T) {
	var logs lockedBuffer
	log := logger.New(&logs, "debug", "json")
	receiver := &captureReceiver{}
	processor := failingProcessor{}
	manager := channel.NewManager(log, channel.NewRegistry(), nil, processor)
	manager.RegisterAdapter(receiver)
	cfg := channel.ChannelConfig{ID: "cfg-1", BotID: "bot-1", ChannelType: receiver.Type(), UpdatedAt: time.Now()}
	if err := manager.EnsureConnection(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	logs.Reset()

	adapter := NewTelegramAdapter(log)
	returned := make(chan struct{})
	handler := func(ctx context.Context, cfg channel.ChannelConfig, msg channel.InboundMessage) error {
		defer close(returned)
		return receiver.handler(ctx, cfg, msg)
	}
	adapter.dispatchInbound(context.Background(), cfg, handler, channel.InboundMessage{Channel: receiver.Type(), BotID: "bot-1"})
	<-returned
	// The adapter would log right after the handler returns, on its own
	// goroutine.
	time.Sleep(50 * time.Millisecond)

	var failures []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		if record["level"] == "ERROR" || record["level"] == "WARN" || record["error"] != nil {
			failures = append(failures, record)
		}
	}
	if len(failures) != 1 || failures[0]["msg"] != "inbound message" || failures[0]["error"] != "resolve route: connection reset" {
		t.Fatalf("failure records = %v, want one inbound message line", failures)
	}
}
