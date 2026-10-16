package external

import (
	"errors"
	"fmt"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

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
