package network

type BotActionRequest struct {
	Input map[string]any `json:"input,omitempty"`
}

// WorkspaceRuntimeStatus describes the bot workspace runtime state (foundation
// before SD-WAN overlay).
type WorkspaceRuntimeStatus struct {
	State             string `json:"state"` // workspace_missing | runtime_unavailable | task_stopped | network_target_ready | unknown
	ContainerID       string `json:"container_id,omitempty"`
	TaskStatus        string `json:"task_status,omitempty"`
	PID               uint32 `json:"pid,omitempty"`
	NetworkTargetKind string `json:"network_target_kind,omitempty"`
	NetworkTarget     string `json:"network_target,omitempty"`
	NetworkAttached   bool   `json:"network_attached,omitempty"`
	Message           string `json:"message,omitempty"`
}

type BotStatus struct {
	Provider     string                  `json:"provider,omitempty"`
	Attached     bool                    `json:"attached"`
	State        string                  `json:"state,omitempty"`
	Title        string                  `json:"title,omitempty"`
	Description  string                  `json:"description,omitempty"`
	Message      string                  `json:"message,omitempty"`
	NetworkIP    string                  `json:"network_ip,omitempty"`
	ProxyAddress string                  `json:"proxy_address,omitempty"`
	Details      map[string]any          `json:"details,omitempty"`
	Workspace    *WorkspaceRuntimeStatus `json:"workspace,omitempty"`
}

type NodeOption struct {
	ID          string         `json:"id"`
	Value       string         `json:"value"`
	DisplayName string         `json:"display_name"`
	Description string         `json:"description,omitempty"`
	Online      bool           `json:"online"`
	Addresses   []string       `json:"addresses,omitempty"`
	CanExitNode bool           `json:"can_exit_node"`
	Selected    bool           `json:"selected"`
	Details     map[string]any `json:"details,omitempty"`
}

type NodeListResponse struct {
	Provider string       `json:"provider,omitempty"`
	Items    []NodeOption `json:"items,omitempty"`
	Message  string       `json:"message,omitempty"`
}

type BotOverlayConfig struct {
	Enabled  bool           `json:"enabled"`
	Provider string         `json:"provider,omitempty"`
	Config   map[string]any `json:"config,omitempty"`
}

type AttachmentRequest struct {
	BotID       string
	ContainerID string
	Runtime     RuntimeNetworkRequest
	Overlay     BotOverlayConfig
	OverlayOnly bool // When true, skip runtime network setup/teardown (overlay-only mutation).
}

// AttachmentStatus reports the outcome of runtime network and overlay
// attachment.
type AttachmentStatus struct {
	Runtime RuntimeNetworkStatus
	Overlay OverlayStatus
}

// RuntimeNetworkRequest describes the container network attachment target for a
// runtime adapter.
type RuntimeNetworkRequest struct {
	ContainerID string
	JoinTarget  NetworkJoinTarget
}

type NetworkJoinTarget struct {
	Kind string
	Path string
	PID  uint32
}

// RuntimeNetworkStatus describes the current state of the container runtime
// network attachment.
type RuntimeNetworkStatus struct {
	Attached bool
	IP       string
}

type OverlayStatus struct {
	Provider     string         `json:"provider,omitempty"`
	Attached     bool           `json:"attached"`
	State        string         `json:"state,omitempty"`
	Message      string         `json:"message,omitempty"`
	NetworkIP    string         `json:"network_ip,omitempty"`
	ProxyAddress string         `json:"proxy_address,omitempty"`
	Details      map[string]any `json:"details,omitempty"`
}
