package network

import (
	"context"
	"errors"
	"log/slog"
	"testing"
)

type configReaderStub func(context.Context, string) (BotOverlayConfig, error)

func (f configReaderStub) GetBotOverlayConfig(ctx context.Context, botID string) (BotOverlayConfig, error) {
	return f(ctx, botID)
}

type workspaceReaderStub func(context.Context, string) (WorkspaceContainer, error)

func (f workspaceReaderStub) GetWorkspaceContainer(ctx context.Context, botID string) (WorkspaceContainer, error) {
	return f(ctx, botID)
}

type validatingProvider struct{}

func (validatingProvider) Kind() string { return "tailscale" }

func (validatingProvider) Descriptor() ProviderDescriptor {
	return ProviderDescriptor{Kind: "tailscale", DisplayName: "Tailscale", ConfigSchema: ConfigSchema{Version: 1}}
}

func (validatingProvider) NormalizeConfig(raw map[string]any) (map[string]any, error) {
	return cloneMap(raw), nil
}

func (validatingProvider) Status(context.Context, BotOverlayConfig) (ProviderStatus, error) {
	return ProviderStatus{State: StatusStateReady}, nil
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

func TestNewWiresControllerWithoutSetter(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(validatingProvider{}); err != nil {
		t.Fatalf("register provider: %v", err)
	}
	configReader := configReaderStub(func(context.Context, string) (BotOverlayConfig, error) {
		return BotOverlayConfig{Enabled: true, Provider: "tailscale", Config: map[string]any{}}, nil
	})
	svc, ctrl := New(slog.Default(), Persistence{Config: configReader}, registry, nil, nil, "containerd", "", "", "")
	if svc == nil || ctrl == nil {
		t.Fatal("expected service and controller")
	}
	if svc.controller == nil {
		t.Fatal("expected controller wired on service")
	}
	cfg, err := ctrl.(*controller).resolver.Resolve(t.Context(), "bot")
	if err != nil {
		t.Fatalf("resolver via controller: %v", err)
	}
	if cfg.Provider != "tailscale" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
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

func TestGetBotConfigUsesPersistenceNeutralStoreAndNormalizesConfig(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(validatingProvider{}); err != nil {
		t.Fatalf("register provider: %v", err)
	}
	configReader := configReaderStub(func(_ context.Context, botID string) (BotOverlayConfig, error) {
		if botID != "11111111-1111-4111-8111-111111111111" {
			t.Fatalf("GetBotOverlayConfig botID = %q", botID)
		}
		return BotOverlayConfig{
			Enabled: true, Provider: " tailscale ",
			Config: map[string]any{"userspace": false, "exit_node": "100.64.0.10"},
		}, nil
	})
	svc := NewServiceWithPersistence(slog.Default(), Persistence{Config: configReader}, registry, nil, "containerd", "", "", "")

	config, err := svc.GetBotConfig(t.Context(), "11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("GetBotConfig() error = %v", err)
	}
	if config.Provider != "tailscale" || !config.Enabled || config.Config["exit_node"] != "100.64.0.10" {
		t.Fatalf("GetBotConfig() = %#v", config)
	}
}

func TestWorkspaceRuntimeStatusPreservesMissingAndInvalidBotStates(t *testing.T) {
	workspaceReader := workspaceReaderStub(func(_ context.Context, botID string) (WorkspaceContainer, error) {
		if botID != "11111111-1111-4111-8111-111111111111" {
			t.Fatalf("GetWorkspaceContainer botID = %q", botID)
		}
		return WorkspaceContainer{}, ErrWorkspaceContainerMissing
	})
	svc := NewServiceWithPersistence(slog.Default(), Persistence{Workspaces: workspaceReader}, nil, nil, "containerd", "", "", "")

	missing := svc.workspaceRuntimeStatus(t.Context(), "11111111-1111-4111-8111-111111111111")
	if missing.State != "workspace_missing" || missing.Message != "" {
		t.Fatalf("missing workspace status = %#v", missing)
	}
	invalid := svc.workspaceRuntimeStatus(t.Context(), "not-a-uuid")
	if invalid.State != "unknown" || invalid.Message != "invalid bot id" {
		t.Fatalf("invalid bot status = %#v", invalid)
	}
}

func TestBuildAttachmentRequestPreservesWorkspaceMissingError(t *testing.T) {
	workspaceReader := workspaceReaderStub(func(context.Context, string) (WorkspaceContainer, error) {
		return WorkspaceContainer{}, ErrWorkspaceContainerMissing
	})
	svc := NewServiceWithPersistence(slog.Default(), Persistence{Workspaces: workspaceReader}, nil, nil, "containerd", "", "", "")

	_, err := svc.buildAttachmentRequest(t.Context(), "11111111-1111-4111-8111-111111111111", BotOverlayConfig{})
	if !errors.Is(err, ErrWorkspaceContainerMissing) {
		t.Fatalf("buildAttachmentRequest() error = %v, want ErrWorkspaceContainerMissing", err)
	}
}
