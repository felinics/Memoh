package gateway

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
)

type lifecycleInboundProcessor struct {
	started chan struct{}
	stopped chan struct{}
}

func (p *lifecycleInboundProcessor) HandleInbound(ctx context.Context, _ ChannelConfig, _ InboundMessage, _ StreamReplySender) error {
	close(p.started)
	<-ctx.Done()
	close(p.stopped)
	return ctx.Err()
}

func TestManagerShutdownWaitsForInboundWorkers(t *testing.T) {
	processor := &lifecycleInboundProcessor{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
	}
	manager := NewManager(
		slog.New(slog.DiscardHandler),
		NewRegistry(),
		nil,
		processor,
		WithInboundWorkers(1),
	)
	manager.Start(t.Context())
	if err := manager.HandleInbound(t.Context(), ChannelConfig{}, InboundMessage{}); err != nil {
		t.Fatalf("HandleInbound() error = %v", err)
	}
	<-processor.started

	if err := manager.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case <-processor.stopped:
	default:
		t.Fatal("Shutdown returned before inbound workers stopped")
	}
}

func TestManagerRejectsInboundAfterShutdown(t *testing.T) {
	manager := NewManager(slog.New(slog.DiscardHandler), NewRegistry(), nil, &lifecycleInboundProcessor{})
	if err := manager.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := manager.HandleInbound(t.Context(), ChannelConfig{}, InboundMessage{}); err == nil {
		t.Fatal("HandleInbound() succeeded after shutdown")
	} else if !errors.Is(err, errManagerStopped) {
		t.Fatalf("HandleInbound() error = %v, want errManagerStopped", err)
	}
}

func TestManagerRejectsRuntimeAdmissionAfterShutdown(t *testing.T) {
	registry := NewRegistry()
	manager := NewManager(slog.New(slog.DiscardHandler), registry, nil, &lifecycleInboundProcessor{})
	if err := manager.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	adapter := &fakeAdapter{channelType: "test"}
	manager.RegisterAdapter(adapter)
	manager.AddAdapter(t.Context(), adapter)
	if _, ok := registry.Get("test"); ok {
		t.Fatal("adapter registered after shutdown")
	}
	if err := manager.EnsureConnection(t.Context(), ChannelConfig{ID: "cfg-1", ChannelType: "test"}); !errors.Is(err, errManagerStopped) {
		t.Fatalf("EnsureConnection() error = %v, want errManagerStopped", err)
	}
	if err := manager.Send(t.Context(), "bot-1", "test", SendRequest{}); !errors.Is(err, errManagerStopped) {
		t.Fatalf("Send() error = %v, want errManagerStopped", err)
	}
	if err := manager.React(t.Context(), "bot-1", "test", ReactRequest{}); !errors.Is(err, errManagerStopped) {
		t.Fatalf("React() error = %v, want errManagerStopped", err)
	}
}

func TestManagerStopsConnectionThatFinishesAfterShutdown(t *testing.T) {
	adapter := &fakeAdapter{
		channelType:    "test",
		connectStarted: make(chan struct{}),
		releaseConnect: make(chan struct{}),
	}
	registry := NewRegistry()
	registry.MustRegister(adapter)
	manager := NewManager(slog.New(slog.DiscardHandler), registry, &fakeConfigStore{}, &fakeInboundProcessorIntegration{})
	manager.Start(t.Context())

	result := make(chan error, 1)
	go func() {
		result <- manager.EnsureConnection(t.Context(), ChannelConfig{ID: "cfg-1", BotID: "bot-1", ChannelType: "test"})
	}()
	<-adapter.connectStarted
	if err := manager.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	close(adapter.releaseConnect)
	if err := <-result; err == nil {
		t.Fatal("EnsureConnection() succeeded after shutdown won the race")
	}

	adapter.mu.Lock()
	stops := adapter.stops
	adapter.mu.Unlock()
	if stops != 1 {
		t.Fatalf("late connection stops = %d, want 1", stops)
	}
	if statuses := manager.ConnectionStatusesByBot("bot-1"); len(statuses) != 0 {
		t.Fatalf("late connection remained registered: %+v", statuses)
	}
}

func TestManagerShutdownRetriesConnectionStop(t *testing.T) {
	sentinel := errors.New("stop failed")
	var attempts atomic.Int32
	cfg := ChannelConfig{ID: "cfg-1", BotID: "bot-1", ChannelType: "test"}
	conn := NewConnection(cfg, func(context.Context) error {
		if attempts.Add(1) == 1 {
			return sentinel
		}
		return nil
	})
	manager := NewManager(slog.New(slog.DiscardHandler), NewRegistry(), nil, nil)
	manager.connections[cfg.ID] = &connectionEntry{config: cfg, connection: conn}

	if err := manager.Shutdown(t.Context()); !errors.Is(err, sentinel) {
		t.Fatalf("first Shutdown() error = %v, want stop failure", err)
	}
	if err := manager.Shutdown(t.Context()); err != nil {
		t.Fatalf("retry Shutdown() error = %v", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("stop attempts = %d, want 2", got)
	}
}
