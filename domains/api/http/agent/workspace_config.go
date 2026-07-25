package agent

import (
	"context"

	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
)

// ACPWorkspaceConfigProvider is the workspace surface ACP OAuth handlers need.
type ACPWorkspaceConfigProvider interface {
	bridge.Provider
	WorkspaceInfo(ctx context.Context, botID string) (bridge.WorkspaceInfo, error)
}
