package netbird

import (
	"context"
	"fmt"
	"path/filepath"

	netctl "github.com/felinics/memoh/internal/network"
	"github.com/felinics/memoh/internal/network/overlay/internal/sidecar"
)

type Deps struct {
	SidecarRuntime sidecar.Runtime
	Runtime        netctl.RuntimeDescriptor
	StateRoot      string
}

type Provider struct {
	deps Deps
}

func NewProvider(deps Deps) netctl.Provider {
	return &Provider{deps: deps}
}

func (*Provider) Kind() string { return "netbird" }

func (p *Provider) Descriptor() netctl.ProviderDescriptor {
	return netctl.ProviderDescriptor{
		Kind:         p.Kind(),
		DisplayName:  "NetBird",
		Description:  "NetBird overlay for workspace networking.",
		ConfigSchema: schema(),
		Capabilities: netctl.ProviderCapabilities{
			Mesh:          true,
			PrivateDNS:    true,
			Userspace:     true,
			KernelTUN:     true,
			NativeClient:  true,
			SidecarWorker: true,
		},
		Actions: []netctl.ProviderAction{
			{
				ID:      "test_connection",
				Type:    netctl.ActionTypeTestConnection,
				Label:   "Test Connection",
				Primary: true,
			},
			{
				ID:    "logout",
				Label: "Log Out",
			},
		},
	}
}

func (*Provider) NormalizeConfig(raw map[string]any) (map[string]any, error) {
	config := netctl.NormalizeConfigBySchema(schema(), raw)
	if err := netctl.ValidateConfigBySchema(schema(), config); err != nil {
		return nil, fmt.Errorf("invalid netbird config: %w", err)
	}
	return config, nil
}

func (p *Provider) Status(_ context.Context, cfg netctl.BotOverlayConfig) (netctl.ProviderStatus, error) {
	if !cfg.Enabled {
		return netctl.ProviderStatus{
			State:       netctl.StatusStateNeedsConfig,
			Title:       "Disabled",
			Description: "This network provider is disabled.",
		}, nil
	}
	if _, err := p.NormalizeConfig(cfg.Config); err != nil {
		return netctl.ProviderStatus{
			State:       netctl.StatusStateNeedsConfig,
			Title:       "Config Required",
			Description: err.Error(),
		}, nil
	}
	return netctl.ProviderStatus{
		State:       netctl.StatusStateReady,
		Title:       "Ready",
		Description: "Provider configuration is valid.",
	}, nil
}

func (p *Provider) ExecuteAction(ctx context.Context, cfg netctl.BotOverlayConfig, actionID string, _ map[string]any) (netctl.ProviderActionExecution, error) {
	switch actionID {
	case "test_connection":
		status, err := p.Status(ctx, cfg)
		return netctl.ProviderActionExecution{
			ActionID: actionID,
			Status:   status,
			Output: map[string]any{
				"provider": cfg.Provider,
			},
		}, err
	default:
		return netctl.ProviderActionExecution{}, fmt.Errorf("unsupported network action %q", actionID)
	}
}

func (p *Provider) ListNodes(context.Context, string, netctl.BotOverlayConfig) ([]netctl.NodeOption, error) {
	return nil, fmt.Errorf("%s does not expose exit node discovery", p.Kind())
}

func (p *Provider) BuildDriver(cfg netctl.BotOverlayConfig) (netctl.OverlayDriver, error) {
	config, err := p.NormalizeConfig(cfg.Config)
	if err != nil {
		return nil, err
	}
	cfg.Config = config
	if p.deps.Runtime.Capabilities.SidecarWorker {
		return newNativeDriver(cfg, p.deps.SidecarRuntime, filepath.Join(p.deps.StateRoot, "network")), nil
	}
	return unsupportedDriver{kind: p.Kind(), message: "NetBird overlay is not supported by the current runtime backend."}, nil
}

type unsupportedDriver struct {
	kind    string
	message string
}

func (d unsupportedDriver) Kind() string { return d.kind }

func (d unsupportedDriver) EnsureAttached(context.Context, netctl.AttachmentRequest) (netctl.OverlayStatus, error) {
	return d.status(), nil
}

func (unsupportedDriver) Detach(context.Context, netctl.AttachmentRequest) error { return nil }

func (d unsupportedDriver) Status(context.Context, netctl.AttachmentRequest) (netctl.OverlayStatus, error) {
	return d.status(), nil
}

func (d unsupportedDriver) status() netctl.OverlayStatus {
	return netctl.OverlayStatus{Provider: d.kind, State: "unsupported", Message: d.message}
}
