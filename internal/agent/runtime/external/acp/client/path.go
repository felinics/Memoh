package client

import (
	"github.com/felinics/memoh/internal/workspace/vpath"
)

const dataMountPath = vpath.DataMount

// ResolvePathUnderVirtualRoot resolves raw under root without consulting the
// host filesystem. Use this for container paths, where the server process
// cannot evaluate symlinks inside the workspace filesystem directly. The
// clamping semantics live in vpath so the workdir service applies the exact
// same rules when it validates a working directory.
func ResolvePathUnderVirtualRoot(root, raw string) (string, error) {
	return vpath.ResolveUnderRoot(root, raw)
}
