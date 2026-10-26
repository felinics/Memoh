package external

import (
	"context"
	"errors"
	"fmt"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

// Workspaces gives a runtime the bridge into a bot's workspace; the workspace
// manager implements it.
type Workspaces interface {
	bridge.Provider
	bridge.WorkspaceInfoProvider
}

// ContainerWorkspace returns the bot's workspace, refusing one that is not a
// container: a runtime's process environment and paths assume that layout.
// A workspace that cannot be looked up is the runtime being unavailable.
func ContainerWorkspace(ctx context.Context, workspaces Workspaces, botID, runtime string) (bridge.WorkspaceInfo, error) {
	info, err := workspaces.WorkspaceInfo(ctx, botID)
	if err != nil {
		return bridge.WorkspaceInfo{}, Unavailable(err)
	}
	if err := RequireContainerWorkspace(info, runtime); err != nil {
		return bridge.WorkspaceInfo{}, err
	}
	return info, nil
}

// ErrContainerWorkspaceRequired reports a direct runtime asked to start in a
// workspace that is not a container.
var ErrContainerWorkspaceRequired = errors.New("direct agent runtime requires a container workspace")

// RequireContainerWorkspace guards drivers whose executable environment and
// credential paths assume the container layout. The remote bridge maps file
// RPC paths, but it does not translate paths embedded in process environments.
func RequireContainerWorkspace(info bridge.WorkspaceInfo, runtime string) error {
	if info.Backend != bridge.WorkspaceBackendRemote {
		return nil
	}
	return fmt.Errorf("%w: %s on workspace backend %q", ErrContainerWorkspaceRequired, runtime, info.Backend)
}
