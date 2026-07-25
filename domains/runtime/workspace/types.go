package workspace

import (
	"context"
	"errors"
	"time"

	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
	ctr "github.com/memohai/memoh/domains/runtime/container"
)

const (
	BotLabelKey                 = "memoh.bot_id"
	WorkspaceLabelKey           = "memoh.workspace"
	WorkspaceLabelValue         = "v3"
	WorkspaceCDIDevicesLabelKey = "memoh.workspace.cdi_devices"
	ContainerPrefix             = "workspace-"
	LegacyContainerPrefix       = "mcp-"
	DisplayRFBSocketName        = "display.rfb.sock"
	ACPToolsProxyHTTPURL        = bridge.ACPToolsProxyHTTPURL
)

// ErrContainerNotFound is returned when no container exists for a bot.
var ErrContainerNotFound = errors.New("workspace not found for bot")

// ContainerStatus combines DB records with live containerd state.
type ContainerStatus struct {
	ContainerID      string    `json:"container_id"`
	WorkspaceBackend string    `json:"workspace_backend"`
	RuntimeBackend   string    `json:"runtime_backend,omitempty"`
	Image            string    `json:"image"`
	Status           string    `json:"status"`
	Namespace        string    `json:"namespace"`
	ContainerPath    string    `json:"container_path"`
	CDIDevices       []string  `json:"cdi_devices,omitempty"`
	TaskRunning      bool      `json:"task_running"`
	HasPreservedData bool      `json:"has_preserved_data"`
	Legacy           bool      `json:"legacy"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type ContainerMetricsStatus struct {
	Exists      bool `json:"exists"`
	TaskRunning bool `json:"task_running"`
}

type ContainerStorageMetrics struct {
	Path      string `json:"path"`
	UsedBytes uint64 `json:"used_bytes"`
}

type ContainerMetricsResult struct {
	Supported         bool
	UnsupportedReason string
	Status            ContainerMetricsStatus
	SampledAt         time.Time
	CPU               *ctr.CPUMetrics
	Memory            *ctr.MemoryMetrics
	Storage           *ContainerStorageMetrics
}

// WorkspaceSetupDiagnostics records sanitized setup failures for Bot health
// checks without coupling the workspace package to the bots package.
type WorkspaceSetupDiagnostics interface {
	RecordContainerSetupFailure(ctx context.Context, botID, phase string, setupErr error) error
	ClearContainerSetupFailure(ctx context.Context, botID string) error
}
