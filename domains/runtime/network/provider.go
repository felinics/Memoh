package network

import "context"

// Provider describes a supported network backend family such as Tailscale or
// NetBird. It is registered globally and later instantiated from stored
// provider records.
type Provider interface {
	Kind() string
	Descriptor() ProviderDescriptor
	NormalizeConfig(raw map[string]any) (map[string]any, error)
	Status(ctx context.Context, cfg BotOverlayConfig) (ProviderStatus, error)
	ExecuteAction(ctx context.Context, cfg BotOverlayConfig, actionID string, input map[string]any) (ProviderActionExecution, error)
	ListNodes(ctx context.Context, botID string, cfg BotOverlayConfig) ([]NodeOption, error)
	BuildDriver(cfg BotOverlayConfig) (OverlayDriver, error)
}

// OverlayDriver is the runtime surface for a concrete configured provider
// instance bound to a bot.
type OverlayDriver interface {
	Kind() string
	EnsureAttached(ctx context.Context, req AttachmentRequest) (OverlayStatus, error)
	Detach(ctx context.Context, req AttachmentRequest) error
	Status(ctx context.Context, req AttachmentRequest) (OverlayStatus, error)
}

type ProviderDescriptor struct {
	Kind         string               `json:"kind"`
	DisplayName  string               `json:"display_name"`
	Description  string               `json:"description,omitempty"`
	ConfigSchema ConfigSchema         `json:"config_schema"`
	Capabilities ProviderCapabilities `json:"capabilities"`
	Actions      []ProviderAction     `json:"actions,omitempty"`
}

type ProviderCapabilities struct {
	Mesh             bool `json:"mesh"`
	PrivateDNS       bool `json:"private_dns"`
	ServiceProxy     bool `json:"service_proxy"`
	EgressProxy      bool `json:"egress_proxy"`
	TransparentProxy bool `json:"transparent_proxy"`
	ExitNode         bool `json:"exit_node"`
	Userspace        bool `json:"userspace"`
	KernelTUN        bool `json:"kernel_tun"`
	NativeClient     bool `json:"native_client"`
	SidecarWorker    bool `json:"sidecar_worker"`
}
