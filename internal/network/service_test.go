package network

import (
	"context"
	"errors"
	"testing"
)

type validatingProvider struct{}

func (validatingProvider) Kind() string { return "tailscale" }

func (validatingProvider) Descriptor() ProviderDescriptor {
	return ProviderDescriptor{Kind: "tailscale", DisplayName: "Tailscale", ConfigSchema: ConfigSchema{Version: 1}}
}

func (validatingProvider) NormalizeConfig(raw map[string]any) (map[string]any, error) {
	return cloneMap(raw), nil
}

func (validatingProvider) Status(context.Context, BotOverlayConfig) (ProviderStatus, error) {
	return ProviderStatus{State: OverlayStateReady}, nil
}

func (validatingProvider) ExecuteAction(context.Context, BotOverlayConfig, string, map[string]any) (ProviderActionExecution, error) {
	return ProviderActionExecution{}, nil
}

func (validatingProvider) ListNodes(context.Context, string, BotOverlayConfig) ([]NodeOption, error) {
	return nil, nil
}

func (validatingProvider) BuildDriver(cfg BotOverlayConfig) (OverlayDriver, error) {
	userspace, _ := cfg.Config["userspace"].(bool)
	exitNode, _ := cfg.Config["exit_node"].(string)
	if userspace && exitNode != "" {
		return nil, errors.New("tailscale transparent egress via exit node requires userspace=false")
	}
	return NoopOverlayDriver{}, nil
}

func TestPrepareBotConfigForWriteAllowsDisabledInvalidProviderDraft(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(validatingProvider{}); err != nil {
		t.Fatalf("register provider: %v", err)
	}
	svc := &Service{registry: registry}

	cfg, err := svc.PrepareBotConfigForWrite(BotOverlayConfig{
		Enabled:  false,
		Provider: "tailscale",
		// Missing auth_key, which would be invalid when enabled.
		Config: map[string]any{},
	})
	if err != nil {
		t.Fatalf("PrepareBotConfigForWrite returned error: %v", err)
	}
	if cfg.Provider != "tailscale" {
		t.Fatalf("expected provider draft to be preserved, got %+v", cfg)
	}
}

func TestPrepareBotConfigForWriteRejectsExitNodeWithUserspaceEnabled(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(validatingProvider{}); err != nil {
		t.Fatalf("register provider: %v", err)
	}
	svc := &Service{registry: registry}

	_, err := svc.PrepareBotConfigForWrite(BotOverlayConfig{
		Enabled:  true,
		Provider: "tailscale",
		Config: map[string]any{
			"auth_key":  "tskey-test",
			"userspace": true,
			"exit_node": "100.64.0.10",
		},
	})
	if err == nil {
		t.Fatal("expected exit node + userspace config to be rejected")
	}
}

func TestPrepareBotConfigForWriteAllowsExitNodeWithKernelTUN(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(validatingProvider{}); err != nil {
		t.Fatalf("register provider: %v", err)
	}
	svc := &Service{registry: registry}

	cfg, err := svc.PrepareBotConfigForWrite(BotOverlayConfig{
		Enabled:  true,
		Provider: "tailscale",
		Config: map[string]any{
			"auth_key":  "tskey-test",
			"userspace": false,
			"exit_node": "100.64.0.10",
		},
	})
	if err != nil {
		t.Fatalf("PrepareBotConfigForWrite returned error: %v", err)
	}
	if cfg.Provider != "tailscale" {
		t.Fatalf("unexpected provider: %+v", cfg)
	}
}
