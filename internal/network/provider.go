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
	Kind         string               `json:"kind" validate:"required"`
	DisplayName  string               `json:"display_name" validate:"required"`
	Description  string               `json:"description,omitempty"`
	ConfigSchema ConfigSchema         `json:"config_schema" validate:"required"`
	Capabilities ProviderCapabilities `json:"capabilities" validate:"required"`
	Actions      []ProviderAction     `json:"actions,omitempty"`
}

type ProviderCapabilities struct {
	Mesh             bool `json:"mesh" validate:"required"`
	PrivateDNS       bool `json:"private_dns" validate:"required"`
	ServiceProxy     bool `json:"service_proxy" validate:"required"`
	EgressProxy      bool `json:"egress_proxy" validate:"required"`
	TransparentProxy bool `json:"transparent_proxy" validate:"required"`
	ExitNode         bool `json:"exit_node" validate:"required"`
	Userspace        bool `json:"userspace" validate:"required"`
	KernelTUN        bool `json:"kernel_tun" validate:"required"`
	NativeClient     bool `json:"native_client" validate:"required"`
	SidecarWorker    bool `json:"sidecar_worker" validate:"required"`
}
