package network

import (
	"context"
	"testing"
)

type testProvider struct {
	kind string
	desc ProviderDescriptor
}

func (p testProvider) Kind() string { return p.kind }

func (p testProvider) Descriptor() ProviderDescriptor { return p.desc }

func (testProvider) NormalizeConfig(raw map[string]any) (map[string]any, error) {
	return raw, nil
}

func (testProvider) Status(_ context.Context, _ BotOverlayConfig) (ProviderStatus, error) {
	return ProviderStatus{State: StatusStateReady}, nil
}

func (testProvider) ExecuteAction(_ context.Context, _ BotOverlayConfig, _ string, _ map[string]any) (ProviderActionExecution, error) {
	return ProviderActionExecution{}, nil
}

func (testProvider) ListNodes(_ context.Context, _ string, _ BotOverlayConfig) ([]NodeOption, error) {
	return nil, nil
}

func (testProvider) BuildDriver(_ BotOverlayConfig) (OverlayDriver, error) {
	return NoopOverlayDriver{}, nil
}

func TestRegistryDescriptorAndCapabilities(t *testing.T) {
	registry := NewRegistry()
	provider := testProvider{
		kind: "SDWAN",
		desc: ProviderDescriptor{
			Kind:        "sdwan",
			DisplayName: "SD-WAN",
			Capabilities: ProviderCapabilities{
				Mesh:        true,
				EgressProxy: true,
			},
		},
	}

	if err := registry.Register(provider); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	desc, ok := registry.GetDescriptor("sdwan")
	if !ok {
		t.Fatal("expected descriptor to be found")
	}
	if desc.DisplayName != "SD-WAN" {
		t.Fatalf("unexpected display name: %q", desc.DisplayName)
	}

	capabilities, ok := registry.GetCapabilities("SDWAN")
	if !ok {
		t.Fatal("expected capabilities to be found")
	}
	if !capabilities.Mesh || !capabilities.EgressProxy {
		t.Fatalf("unexpected capabilities: %+v", capabilities)
	}
}
