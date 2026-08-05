package channel

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
)

// mockAdapter is used for inbound handleInbound tests.
type mockAdapter struct {
	sentMessages []OutboundMessage
	streamEvents []StreamEvent
}

func (*mockAdapter) Type() ChannelType { return ChannelType("test") }
func (*mockAdapter) Descriptor() Descriptor {
	return Descriptor{
		Type:        ChannelType("test"),
		DisplayName: "Test",
		Capabilities: ChannelCapabilities{
			Text:      true,
			Reply:     true,
			Streaming: true,
		},
	}
}

func (m *mockAdapter) Send(_ context.Context, _ ChannelConfig, msg PreparedOutboundMessage) error {
	m.sentMessages = append(m.sentMessages, msg.LogicalMessage())
	return nil
}

func (m *mockAdapter) OpenStream(_ context.Context, _ ChannelConfig, _ string, _ StreamOptions) (PreparedOutboundStream, error) {
	return &mockAdapterStream{adapter: m}, nil
}

type mockAdapterStream struct {
	adapter *mockAdapter
}

func (s *mockAdapterStream) Push(_ context.Context, event PreparedStreamEvent) error {
	if s == nil || s.adapter == nil {
		return nil
	}
	logical := event.LogicalEvent()
	s.adapter.streamEvents = append(s.adapter.streamEvents, logical)
	if logical.Type == StreamEventFinal && logical.Final != nil && !logical.Final.Message.IsEmpty() {
		s.adapter.sentMessages = append(s.adapter.sentMessages, OutboundMessage{
			Target:  "stream-target",
			Message: logical.Final.Message,
		})
	}
	return nil
}

func (*mockAdapterStream) Close(_ context.Context) error {
	return nil
}

type fakeInboundProcessor struct {
	resp   *OutboundMessage
	err    error
	gotCfg ChannelConfig
	gotMsg InboundMessage
}

func (f *fakeInboundProcessor) HandleInbound(ctx context.Context, cfg ChannelConfig, msg InboundMessage, sender StreamReplySender) error {
	f.gotCfg = cfg
	f.gotMsg = msg
	if f.err != nil {
		return f.err
	}
	if f.resp == nil {
		return nil
	}
	if sender == nil {
		return errors.New("sender missing")
	}
	return sender.Send(ctx, *f.resp)
}

type fakeInboundStreamProcessor struct{}

func (*fakeInboundStreamProcessor) HandleInbound(ctx context.Context, _ ChannelConfig, _ InboundMessage, sender StreamReplySender) error {
	stream, err := sender.OpenStream(ctx, "stream-target", StreamOptions{})
	if err != nil {
		return err
	}
	if err := stream.Push(ctx, StreamEvent{
		Type:  StreamEventDelta,
		Delta: "partial",
	}); err != nil {
		return err
	}
	if err := stream.Push(ctx, StreamEvent{
		Type: StreamEventFinal,
		Final: &StreamFinalizePayload{
			Message: Message{Text: "stream-final"},
		},
	}); err != nil {
		return err
	}
	return stream.Close(ctx)
}

type inboundRequestContextKey struct{}

type contextCapturingInboundProcessor struct {
	contexts chan context.Context
}

func (p *contextCapturingInboundProcessor) HandleInbound(ctx context.Context, _ ChannelConfig, _ InboundMessage, _ StreamReplySender) error {
	p.contexts <- ctx
	return nil
}

func TestManagerInboundWorkersDoNotInheritFirstRequestContext(t *testing.T) {
	processor := &contextCapturingInboundProcessor{contexts: make(chan context.Context, 1)}
	m := NewManager(slog.New(slog.DiscardHandler), NewRegistry(), &fakeConfigStore{}, processor)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })

	requestCtx := context.WithValue(context.Background(), inboundRequestContextKey{}, "first-request")
	if err := m.HandleInbound(requestCtx, ChannelConfig{ID: "cfg-1"}, InboundMessage{}); err != nil {
		t.Fatalf("HandleInbound: %v", err)
	}

	select {
	case workerCtx := <-processor.contexts:
		if got := workerCtx.Value(inboundRequestContextKey{}); got != nil {
			t.Fatalf("worker inherited first request context value: %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for inbound worker")
	}
}

func TestManager_handleInbound(t *testing.T) {
	logger := slog.Default()

	t.Run("with_reply_sends_successfully", func(t *testing.T) {
		processor := &fakeInboundProcessor{
			resp: &OutboundMessage{
				Target: "target-id",
				Message: Message{
					Text: "AI reply content",
				},
			},
		}

		reg := NewRegistry()
		m := NewManager(logger, reg, &fakeConfigStore{}, processor)
		adapter := &mockAdapter{}
		m.RegisterAdapter(adapter)

		cfg := ChannelConfig{ID: "bot-1", BotID: "bot-1", ChannelType: ChannelType("test")}
		msg := InboundMessage{
			Channel:     ChannelType("test"),
			Message:     Message{Text: "hello"},
			ReplyTarget: "target-id",
			Conversation: Conversation{
				ID:   "chat-1",
				Type: ConversationTypePrivate,
			},
		}

		err := m.handleInbound(context.Background(), cfg, msg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(adapter.sentMessages) != 1 {
			t.Fatalf("expected 1 reply sent, got %d", len(adapter.sentMessages))
		}
		if adapter.sentMessages[0].Message.PlainText() != "AI reply content" {
			t.Errorf("reply content mismatch: %s", adapter.sentMessages[0].Message.PlainText())
		}
		if adapter.sentMessages[0].Target != "target-id" {
			t.Errorf("reply target mismatch: %s", adapter.sentMessages[0].Target)
		}
	})

	t.Run("no_reply_does_not_send", func(t *testing.T) {
		processor := &fakeInboundProcessor{resp: nil}
		reg := NewRegistry()
		m := NewManager(logger, reg, &fakeConfigStore{}, processor)
		adapter := &mockAdapter{}
		m.RegisterAdapter(adapter)

		cfg := ChannelConfig{ID: "bot-1", BotID: "bot-1", ChannelType: ChannelType("test")}
		msg := InboundMessage{
			Channel:     ChannelType("test"),
			Message:     Message{Text: "hello"},
			ReplyTarget: "target-id",
		}

		err := m.handleInbound(context.Background(), cfg, msg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(adapter.sentMessages) != 0 {
			t.Errorf("expected no reply sent, got %+v", adapter.sentMessages)
		}
	})

	t.Run("handler_error_returns_error", func(t *testing.T) {
		processor := &fakeInboundProcessor{err: context.Canceled}
		reg := NewRegistry()
		m := NewManager(logger, reg, &fakeConfigStore{}, processor)
		cfg := ChannelConfig{ID: "bot-1"}
		msg := InboundMessage{Message: Message{Text: "  "}} // whitespace-only message

		err := m.handleInbound(context.Background(), cfg, msg)
		if err == nil {
			t.Errorf("expected handler to return error")
		}
	})

	t.Run("stream sender forwards events", func(t *testing.T) {
		processor := &fakeInboundStreamProcessor{}
		reg := NewRegistry()
		m := NewManager(logger, reg, &fakeConfigStore{}, processor)
		adapter := &mockAdapter{}
		m.RegisterAdapter(adapter)

		cfg := ChannelConfig{ID: "bot-1", BotID: "bot-1", ChannelType: ChannelType("test")}
		msg := InboundMessage{
			Channel:     ChannelType("test"),
			Message:     Message{Text: "hello"},
			ReplyTarget: "stream-target",
			Conversation: Conversation{
				ID:   "chat-1",
				Type: ConversationTypePrivate,
			},
		}
		if err := m.handleInbound(context.Background(), cfg, msg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(adapter.streamEvents) < 2 {
			t.Fatalf("expected at least two stream events, got %d", len(adapter.streamEvents))
		}
		if len(adapter.sentMessages) == 0 {
			t.Fatal("expected stream final message to be published")
		}
		if adapter.sentMessages[len(adapter.sentMessages)-1].Message.PlainText() != "stream-final" {
			t.Fatalf("unexpected stream final message: %s", adapter.sentMessages[len(adapter.sentMessages)-1].Message.PlainText())
		}
	})
}
