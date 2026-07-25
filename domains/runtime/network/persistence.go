package network

import "context"

// ConfigReader is Runtime's narrow projection of API-owned Bot network
// settings. The API owner remains the write authority.
type ConfigReader interface {
	GetBotOverlayConfig(context.Context, string) (BotOverlayConfig, error)
}

// WorkspaceReader reads Runtime-owned workspace lifecycle state.
type WorkspaceReader interface {
	GetWorkspaceContainer(context.Context, string) (WorkspaceContainer, error)
}

// Persistence keeps API-owned configuration and Runtime-owned workspace state
// as independently replaceable ports.
type Persistence struct {
	Config     ConfigReader
	Workspaces WorkspaceReader
}

// WorkspaceContainer is the narrow durable view needed to inspect and attach
// a bot workspace network.
type WorkspaceContainer struct {
	ContainerID string
}
