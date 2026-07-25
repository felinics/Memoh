package network

import (
	"context"
	"errors"
	"testing"
)

type fakeRuntime struct {
	ensureRequests []RuntimeNetworkRequest
	removeRequests []RuntimeNetworkRequest
	statusResult   RuntimeNetworkStatus
}

func (*fakeRuntime) Kind() string { return "fake" }

func (*fakeRuntime) Descriptor() RuntimeDescriptor {
	return RuntimeDescriptor{Kind: "fake", DisplayName: "Fake Runtime"}
}

func (f *fakeRuntime) EnsureNetwork(_ context.Context, req RuntimeNetworkRequest) (RuntimeNetworkStatus, error) {
	f.ensureRequests = append(f.ensureRequests, req)
	return RuntimeNetworkStatus{Attached: true, IP: "10.0.0.8"}, nil
}

func (f *fakeRuntime) RemoveNetwork(_ context.Context, req RuntimeNetworkRequest) error {
	f.removeRequests = append(f.removeRequests, req)
	return nil
}

func (f *fakeRuntime) StatusNetwork(context.Context, RuntimeNetworkRequest) (RuntimeNetworkStatus, error) {
	return f.statusResult, nil
}

type fakeOverlay struct {
	ensureRequests []AttachmentRequest
	detachRequests []AttachmentRequest
	statusResult   OverlayStatus
	ensureErr      error
}

func (*fakeOverlay) Kind() string { return "fake-overlay" }

func (*fakeOverlay) Descriptor() ProviderDescriptor {
	return ProviderDescriptor{Kind: "fake-overlay", DisplayName: "Fake Overlay"}
}

func (f *fakeOverlay) EnsureAttached(_ context.Context, req AttachmentRequest) (OverlayStatus, error) {
	f.ensureRequests = append(f.ensureRequests, req)
	if f.ensureErr != nil {
		return OverlayStatus{}, f.ensureErr
	}
	return OverlayStatus{Provider: "fake-overlay", Attached: true}, nil
}

func (f *fakeOverlay) Detach(_ context.Context, req AttachmentRequest) error {
	f.detachRequests = append(f.detachRequests, req)
	return nil
}

func (f *fakeOverlay) Status(context.Context, AttachmentRequest) (OverlayStatus, error) {
	return f.statusResult, nil
}

type fakeProvider struct {
	overlay *fakeOverlay
}

func (*fakeProvider) Kind() string { return "fake-overlay" }

func (*fakeProvider) Descriptor() ProviderDescriptor {
	return ProviderDescriptor{
		Kind:         "fake-overlay",
		DisplayName:  "Fake Overlay",
		ConfigSchema: ConfigSchema{Version: 1},
	}
}

func (*fakeProvider) NormalizeConfig(raw map[string]any) (map[string]any, error) {
	return cloneMap(raw), nil
}

func (*fakeProvider) Status(context.Context, BotOverlayConfig) (ProviderStatus, error) {
	return ProviderStatus{State: StatusStateReady}, nil
}

func (*fakeProvider) ExecuteAction(context.Context, BotOverlayConfig, string, map[string]any) (ProviderActionExecution, error) {
	return ProviderActionExecution{}, nil
}

func (*fakeProvider) ListNodes(context.Context, string, BotOverlayConfig) ([]NodeOption, error) {
	return nil, nil
}

func (p *fakeProvider) BuildDriver(BotOverlayConfig) (OverlayDriver, error) {
	return p.overlay, nil
}

func TestControllerEnsureAttachedDelegatesRuntimeAndOverlay(t *testing.T) {
	runtime := &fakeRuntime{}
	overlay := &fakeOverlay{}
	registry := NewRegistry()
	if err := registry.Register(&fakeProvider{overlay: overlay}); err != nil {
		t.Fatalf("register provider: %v", err)
	}
	controller := NewController(runtime, nil, registry)

	req := AttachmentRequest{
		BotID:       "bot-1",
		ContainerID: "workspace-bot-1",
		Runtime: RuntimeNetworkRequest{
			ContainerID: "workspace-bot-1",
		},
		Overlay: BotOverlayConfig{
			Enabled:  true,
			Provider: "fake-overlay",
		},
	}

	status, err := controller.EnsureAttached(context.Background(), req)
	if err != nil {
		t.Fatalf("EnsureAttached failed: %v", err)
	}
	if status.Runtime.IP != "10.0.0.8" || !status.Runtime.Attached {
		t.Fatalf("unexpected runtime status: %+v", status.Runtime)
	}
	if status.Overlay.Provider != "fake-overlay" || !status.Overlay.Attached {
		t.Fatalf("unexpected overlay status: %+v", status.Overlay)
	}
	if len(runtime.ensureRequests) != 1 {
		t.Fatalf("expected one runtime ensure call, got %d", len(runtime.ensureRequests))
	}
	if len(overlay.ensureRequests) != 1 {
		t.Fatalf("expected one overlay ensure call, got %d", len(overlay.ensureRequests))
	}
}

func TestControllerDetachDelegatesOverlayBeforeRuntime(t *testing.T) {
	runtime := &fakeRuntime{}
	overlay := &fakeOverlay{}
	registry := NewRegistry()
	if err := registry.Register(&fakeProvider{overlay: overlay}); err != nil {
		t.Fatalf("register provider: %v", err)
	}
	controller := NewController(runtime, nil, registry)

	req := AttachmentRequest{
		BotID:       "bot-1",
		ContainerID: "workspace-bot-1",
		Runtime:     RuntimeNetworkRequest{ContainerID: "workspace-bot-1"},
		Overlay: BotOverlayConfig{
			Enabled:  true,
			Provider: "fake-overlay",
		},
	}

	if err := controller.Detach(context.Background(), req); err != nil {
		t.Fatalf("Detach failed: %v", err)
	}
	if len(overlay.detachRequests) != 1 {
		t.Fatalf("expected one overlay detach call, got %d", len(overlay.detachRequests))
	}
	if len(runtime.removeRequests) != 1 {
		t.Fatalf("expected one runtime remove call, got %d", len(runtime.removeRequests))
	}
}

func TestControllerEnsureAttachedOverlayOnly(t *testing.T) {
	runtime := &fakeRuntime{}
	overlay := &fakeOverlay{}
	registry := NewRegistry()
	if err := registry.Register(&fakeProvider{overlay: overlay}); err != nil {
		t.Fatalf("register provider: %v", err)
	}
	controller := NewController(runtime, nil, registry)

	req := AttachmentRequest{
		BotID:       "bot-1",
		ContainerID: "workspace-bot-1",
		Runtime:     RuntimeNetworkRequest{ContainerID: "workspace-bot-1"},
		Overlay: BotOverlayConfig{
			Enabled:  true,
			Provider: "fake-overlay",
		},
		OverlayOnly: true,
	}

	status, err := controller.EnsureAttached(context.Background(), req)
	if err != nil {
		t.Fatalf("EnsureAttached failed: %v", err)
	}
	if len(runtime.ensureRequests) != 0 {
		t.Fatalf("expected no runtime ensure calls for OverlayOnly, got %d", len(runtime.ensureRequests))
	}
	if len(overlay.ensureRequests) != 1 {
		t.Fatalf("expected one overlay ensure call, got %d", len(overlay.ensureRequests))
	}
	if status.Overlay.Provider != "fake-overlay" || !status.Overlay.Attached {
		t.Fatalf("unexpected overlay status: %+v", status.Overlay)
	}
}

func TestControllerDetachOverlayOnly(t *testing.T) {
	runtime := &fakeRuntime{}
	overlay := &fakeOverlay{}
	registry := NewRegistry()
	if err := registry.Register(&fakeProvider{overlay: overlay}); err != nil {
		t.Fatalf("register provider: %v", err)
	}
	controller := NewController(runtime, nil, registry)

	req := AttachmentRequest{
		BotID:       "bot-1",
		ContainerID: "workspace-bot-1",
		Runtime:     RuntimeNetworkRequest{ContainerID: "workspace-bot-1"},
		Overlay: BotOverlayConfig{
			Enabled:  true,
			Provider: "fake-overlay",
		},
		OverlayOnly: true,
	}

	if err := controller.Detach(context.Background(), req); err != nil {
		t.Fatalf("Detach failed: %v", err)
	}
	if len(overlay.detachRequests) != 1 {
		t.Fatalf("expected one overlay detach call, got %d", len(overlay.detachRequests))
	}
	if len(runtime.removeRequests) != 0 {
		t.Fatalf("expected no runtime remove calls for OverlayOnly, got %d", len(runtime.removeRequests))
	}
}

func TestControllerStatusDelegatesRuntimeAndOverlay(t *testing.T) {
	runtime := &fakeRuntime{
		statusResult: RuntimeNetworkStatus{Attached: true, IP: "10.0.0.8"},
	}
	overlay := &fakeOverlay{
		statusResult: OverlayStatus{Provider: "fake-overlay", Attached: true, State: "ready"},
	}
	registry := NewRegistry()
	if err := registry.Register(&fakeProvider{overlay: overlay}); err != nil {
		t.Fatalf("register provider: %v", err)
	}
	controller := NewController(runtime, nil, registry)

	req := AttachmentRequest{
		BotID:       "bot-1",
		ContainerID: "workspace-bot-1",
		Runtime:     RuntimeNetworkRequest{ContainerID: "workspace-bot-1"},
		Overlay: BotOverlayConfig{
			Enabled:  true,
			Provider: "fake-overlay",
		},
	}

	status, err := controller.Status(context.Background(), req)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if !status.Runtime.Attached || status.Runtime.IP != "10.0.0.8" {
		t.Fatalf("unexpected runtime status: %+v", status.Runtime)
	}
	if !status.Overlay.Attached || status.Overlay.Provider != "fake-overlay" {
		t.Fatalf("unexpected overlay status: %+v", status.Overlay)
	}
}

func TestControllerEnsureAttachedRollsBackRuntimeOnOverlayError(t *testing.T) {
	runtime := &fakeRuntime{}
	overlay := &fakeOverlay{ensureErr: errors.New("overlay attach failed")}
	registry := NewRegistry()
	if err := registry.Register(&fakeProvider{overlay: overlay}); err != nil {
		t.Fatalf("register provider: %v", err)
	}
	controller := NewController(runtime, nil, registry)

	req := AttachmentRequest{
		BotID:       "bot-1",
		ContainerID: "workspace-bot-1",
		Runtime:     RuntimeNetworkRequest{ContainerID: "workspace-bot-1"},
		Overlay: BotOverlayConfig{
			Enabled:  true,
			Provider: "fake-overlay",
		},
	}

	if _, err := controller.EnsureAttached(context.Background(), req); err == nil {
		t.Fatal("expected EnsureAttached to fail")
	}
	if len(runtime.ensureRequests) != 1 {
		t.Fatalf("expected one runtime ensure call, got %d", len(runtime.ensureRequests))
	}
	if len(runtime.removeRequests) != 1 {
		t.Fatalf("expected one runtime rollback call, got %d", len(runtime.removeRequests))
	}
}
