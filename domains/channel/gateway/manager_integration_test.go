package gateway

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeConfigStore struct {
	effectiveConfig        ChannelConfig
	channelIdentityConfig  ChannelIdentityBinding
	configsByType          map[ChannelType][]ChannelConfig
	boundChannelIdentityID string
}

func (f *fakeConfigStore) ResolveEffectiveConfig(_ context.Context, _ string, _ ChannelType) (ChannelConfig, error) {
	return f.effectiveConfig, nil
}

func (f *fakeConfigStore) GetChannelIdentityConfig(_ context.Context, _ string, _ ChannelType) (ChannelIdentityBinding, error) {
	if f.channelIdentityConfig.ID == "" && len(f.channelIdentityConfig.Config) == 0 {
		return ChannelIdentityBinding{}, errors.New("channel user config not found")
	}
	return f.channelIdentityConfig, nil
}

func (f *fakeConfigStore) UpsertChannelIdentityConfig(_ context.Context, _ string, _ ChannelType, _ UpsertChannelIdentityConfigRequest) (ChannelIdentityBinding, error) {
	return f.channelIdentityConfig, nil
}

func (f *fakeConfigStore) ListConfigsByType(_ context.Context, channelType ChannelType) ([]ChannelConfig, error) {
	if f.configsByType == nil {
		return nil, nil
	}
	return f.configsByType[channelType], nil
}

func (f *fakeConfigStore) ResolveChannelIdentityBinding(_ context.Context, _ ChannelType, _ BindingCriteria) (string, error) {
	if f.boundChannelIdentityID == "" {
		return "", errors.New("channel user binding not found")
	}
	return f.boundChannelIdentityID, nil
}

type fakeInboundProcessorIntegration struct {
	resp   *OutboundMessage
	err    error
	gotCfg ChannelConfig
	gotMsg InboundMessage
}

func (f *fakeInboundProcessorIntegration) HandleInbound(ctx context.Context, cfg ChannelConfig, msg InboundMessage, sender StreamReplySender) error {
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

type fakeAdapter struct {
	channelType    ChannelType
	connectErr     error
	connectStarted chan struct{}
	releaseConnect chan struct{}
	mu             sync.Mutex
	started        []ChannelConfig
	connectCtxs    []context.Context
	sent           []OutboundMessage
	stops          int
}

func (f *fakeAdapter) Type() ChannelType {
	return f.channelType
}

func (f *fakeAdapter) Descriptor() Descriptor {
	return Descriptor{Type: f.channelType, DisplayName: "Fake", Capabilities: ChannelCapabilities{Text: true}}
}

func (*fakeAdapter) ResolveTarget(channelIdentityConfig map[string]any) (string, error) {
	value := strings.TrimSpace(ReadString(channelIdentityConfig, "target"))
	if value == "" {
		return "", errors.New("missing target")
	}
	return "resolved:" + value, nil
}

func (*fakeAdapter) NormalizeTarget(raw string) string { return strings.TrimSpace(raw) }

func (f *fakeAdapter) Connect(ctx context.Context, cfg ChannelConfig, _ InboundHandler) (Connection, error) {
	if f.connectErr != nil {
		return nil, f.connectErr
	}
	if f.connectStarted != nil {
		close(f.connectStarted)
	}
	if f.releaseConnect != nil {
		<-f.releaseConnect
	}
	f.mu.Lock()
	f.started = append(f.started, cfg)
	f.connectCtxs = append(f.connectCtxs, ctx)
	f.mu.Unlock()
	stop := func(context.Context) error {
		f.mu.Lock()
		f.stops++
		f.mu.Unlock()
		return nil
	}
	return NewConnection(cfg, stop), nil
}

func (f *fakeAdapter) Send(_ context.Context, _ ChannelConfig, msg PreparedOutboundMessage) error {
	f.mu.Lock()
	f.sent = append(f.sent, msg.LogicalMessage())
	f.mu.Unlock()
	return nil
}

func TestManagerHandleInboundIntegratesAdapter(t *testing.T) {
	t.Parallel()

	log := slog.New(slog.DiscardHandler)
	store := &fakeConfigStore{}
	processor := &fakeInboundProcessorIntegration{
		resp: &OutboundMessage{
			Target: "123",
			Message: Message{
				Text: "ok",
			},
		},
	}
	reg := NewRegistry()
	adapter := &fakeAdapter{channelType: ChannelType("test")}
	manager := NewManager(log, reg, store, processor)
	manager.RegisterAdapter(adapter)

	cfg := ChannelConfig{
		ID:          "cfg-1",
		BotID:       "bot-1",
		ChannelType: ChannelType("test"),
		Credentials: map[string]any{"botToken": "token"},
		UpdatedAt:   time.Now(),
	}
	err := manager.handleInbound(context.Background(), cfg, InboundMessage{
		Channel:     ChannelType("test"),
		Message:     Message{Text: "hi"},
		BotID:       "bot-1",
		ReplyTarget: "123",
		Conversation: Conversation{
			ID:   "chat-1",
			Type: ConversationTypePrivate,
		},
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if processor.gotMsg.Conversation.ID != "chat-1" || processor.gotMsg.Message.PlainText() != "hi" || processor.gotMsg.BotID != "bot-1" {
		t.Fatalf("unexpected inbound message: %+v", processor.gotMsg)
	}

	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if len(adapter.sent) != 1 {
		t.Fatalf("expected 1 send, got %d", len(adapter.sent))
	}
	if adapter.sent[0].Target != "123" || adapter.sent[0].Message.PlainText() != "ok" {
		t.Fatalf("unexpected outbound message: %+v", adapter.sent[0])
	}
}

func TestManagerSendUsesBinding(t *testing.T) {
	t.Parallel()

	log := slog.New(slog.DiscardHandler)
	store := &fakeConfigStore{
		effectiveConfig: ChannelConfig{
			ID:          "cfg-1",
			BotID:       "bot-1",
			ChannelType: ChannelType("test"),
			Credentials: map[string]any{"botToken": "token"},
			UpdatedAt:   time.Now(),
		},
		channelIdentityConfig: ChannelIdentityBinding{
			ID:     "binding-1",
			Config: map[string]any{"target": "alice"},
		},
	}
	reg := NewRegistry()
	adapter := &fakeAdapter{channelType: ChannelType("test")}
	manager := NewManager(log, reg, store, &fakeInboundProcessorIntegration{})
	manager.RegisterAdapter(adapter)

	err := manager.Send(context.Background(), "bot-1", ChannelType("test"), SendRequest{
		ChannelIdentityID: "user-1",
		Message: Message{
			Text: "hello",
		},
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if len(adapter.sent) != 1 {
		t.Fatalf("expected 1 send, got %d", len(adapter.sent))
	}
	if adapter.sent[0].Target != "resolved:alice" || adapter.sent[0].Message.PlainText() != "hello" {
		t.Fatalf("unexpected outbound message: %+v", adapter.sent[0])
	}
}

func TestManagerReconcileStartsAndStops(t *testing.T) {
	t.Parallel()

	log := slog.New(slog.DiscardHandler)
	store := &fakeConfigStore{}
	reg := NewRegistry()
	adapter := &fakeAdapter{channelType: ChannelType("test")}
	manager := NewManager(log, reg, store, &fakeInboundProcessorIntegration{})
	manager.RegisterAdapter(adapter)

	cfg := ChannelConfig{
		ID:          "cfg-1",
		BotID:       "bot-1",
		ChannelType: ChannelType("test"),
		Credentials: map[string]any{"botToken": "token"},
		UpdatedAt:   time.Now(),
	}
	manager.reconcile(context.Background(), []ChannelConfig{cfg})
	statuses := manager.ConnectionStatusesByBot("bot-1")
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status after start, got %d", len(statuses))
	}
	if !statuses[0].Running {
		t.Fatalf("expected running status after start")
	}
	manager.reconcile(context.Background(), nil)
	statuses = manager.ConnectionStatusesByBot("bot-1")
	if len(statuses) != 0 {
		t.Fatalf("expected 0 status after remove, got %d", len(statuses))
	}

	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if len(adapter.started) != 1 {
		t.Fatalf("expected 1 start, got %d", len(adapter.started))
	}
	if adapter.stops != 1 {
		t.Fatalf("expected 1 stop, got %d", adapter.stops)
	}
}

func TestManagerConnectionStatusesByBotTracksConnectFailure(t *testing.T) {
	t.Parallel()

	log := slog.New(slog.DiscardHandler)
	store := &fakeConfigStore{}
	reg := NewRegistry()
	adapter := &fakeAdapter{
		channelType: ChannelType("test"),
		connectErr:  errors.New("dial failed"),
	}
	manager := NewManager(log, reg, store, &fakeInboundProcessorIntegration{})
	manager.RegisterAdapter(adapter)

	cfg := ChannelConfig{
		ID:          "cfg-fail-1",
		BotID:       "bot-1",
		ChannelType: ChannelType("test"),
		Credentials: map[string]any{"botToken": "token"},
		UpdatedAt:   time.Now(),
	}
	manager.reconcile(context.Background(), []ChannelConfig{cfg})

	statuses := manager.ConnectionStatusesByBot("bot-1")
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	if statuses[0].Running {
		t.Fatalf("expected non-running status on connect failure")
	}
	if statuses[0].LastError == "" {
		t.Fatalf("expected last error on connect failure")
	}
}

func TestManagerEnsureConnectionDetachesRequestContext(t *testing.T) {
	t.Parallel()

	log := slog.New(slog.DiscardHandler)
	store := &fakeConfigStore{}
	reg := NewRegistry()
	adapter := &fakeAdapter{channelType: ChannelType("test")}
	manager := NewManager(log, reg, store, &fakeInboundProcessorIntegration{})
	manager.RegisterAdapter(adapter)

	cfg := ChannelConfig{
		ID:          "cfg-1",
		BotID:       "bot-1",
		ChannelType: ChannelType("test"),
		Credentials: map[string]any{"token": "x"},
		UpdatedAt:   time.Now(),
	}
	reqCtx, cancel := context.WithCancel(context.Background())
	if err := manager.EnsureConnection(reqCtx, cfg); err != nil {
		cancel()
		t.Fatalf("expected no error, got %v", err)
	}
	cancel()

	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if len(adapter.connectCtxs) != 1 {
		t.Fatalf("expected 1 connect context, got %d", len(adapter.connectCtxs))
	}
	if err := adapter.connectCtxs[0].Err(); err != nil {
		t.Fatalf("expected detached context to remain active, got %v", err)
	}
}
