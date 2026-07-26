package email

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"

	emailport "github.com/memohai/memoh/domains/channel/internal/port/email"
)

type managerProviderStore struct {
	emailport.ProviderStore
	record emailport.ProviderRecord
}

func (s *managerProviderStore) ListProviders(context.Context, string) ([]emailport.ProviderRecord, error) {
	return []emailport.ProviderRecord{s.record}, nil
}

func (s *managerProviderStore) FindProvider(context.Context, string) (emailport.ProviderRecord, error) {
	return s.record, nil
}

type managerBindingStore struct {
	emailport.BindingStore
	record emailport.BindingRecord
}

func (s *managerBindingStore) ListReadableBindings(context.Context, string) ([]emailport.BindingRecord, error) {
	return []emailport.BindingRecord{s.record}, nil
}

type managerReceiver struct {
	mu       sync.Mutex
	contexts []context.Context
	stoppers []*managerStopper
}

func (*managerReceiver) Type() emailport.ProviderName { return "manager-test" }

func (*managerReceiver) Meta() emailport.ProviderMeta {
	return emailport.ProviderMeta{Provider: "manager-test", DisplayName: "Manager test"}
}

func (*managerReceiver) NormalizeConfig(config map[string]any) (map[string]any, error) {
	return config, nil
}

func (r *managerReceiver) StartReceiving(ctx context.Context, _ map[string]any, _ emailport.InboundHandler) (emailport.Stopper, error) {
	stopper := &managerStopper{}
	r.mu.Lock()
	r.contexts = append(r.contexts, ctx)
	r.stoppers = append(r.stoppers, stopper)
	r.mu.Unlock()
	return stopper, nil
}

func (r *managerReceiver) snapshot() ([]context.Context, []*managerStopper) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]context.Context(nil), r.contexts...), append([]*managerStopper(nil), r.stoppers...)
}

type managerStopper struct {
	mu      sync.Mutex
	stopped bool
}

func (s *managerStopper) Stop(context.Context) error {
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
	return nil
}

func (s *managerStopper) isStopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped
}

func TestManagerOwnsReceiverContext(t *testing.T) {
	receiver := &managerReceiver{}
	registry := NewRegistry()
	registry.Register(receiver)
	providers := &managerProviderStore{record: emailport.ProviderRecord{
		ID:       "provider-1",
		Provider: "manager-test",
		Config:   json.RawMessage(`{}`),
	}}
	bindings := &managerBindingStore{record: emailport.BindingRecord{
		BotID:           "bot-1",
		EmailProviderID: "provider-1",
		CanRead:         true,
	}}
	service := NewService(slog.New(slog.DiscardHandler), providers, bindings, registry)
	manager := NewManager(slog.New(slog.DiscardHandler), service, nil, nil)

	startupCtx, cancelStartup := context.WithCancel(t.Context())
	if err := manager.Start(startupCtx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	cancelStartup()

	contexts, stoppers := receiver.snapshot()
	if len(contexts) != 1 || len(stoppers) != 1 {
		t.Fatalf("receiver starts = %d, want 1", len(contexts))
	}
	if err := contexts[0].Err(); err != nil {
		t.Fatalf("receiver inherited startup cancellation: %v", err)
	}

	refreshCtx, cancelRefresh := context.WithCancel(t.Context())
	if err := manager.RefreshProvider(refreshCtx, "provider-1"); err != nil {
		t.Fatalf("RefreshProvider() error = %v", err)
	}
	cancelRefresh()

	contexts, stoppers = receiver.snapshot()
	if len(contexts) != 2 || len(stoppers) != 2 {
		t.Fatalf("receiver starts after refresh = %d, want 2", len(contexts))
	}
	if !stoppers[0].isStopped() {
		t.Fatal("RefreshProvider() did not stop the previous receiver")
	}
	if err := contexts[1].Err(); err != nil {
		t.Fatalf("receiver inherited refresh request cancellation: %v", err)
	}

	if err := manager.Stop(t.Context()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if !stoppers[1].isStopped() {
		t.Fatal("Stop() did not stop the active receiver")
	}
	for i, ctx := range contexts {
		if err := ctx.Err(); err == nil {
			t.Fatalf("receiver context %d remains active after Stop()", i)
		}
	}
}
