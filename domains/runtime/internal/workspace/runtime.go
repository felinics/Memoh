package workspace

import ctr "github.com/memohai/memoh/domains/runtime/container"

// runtimeService is the workspace-facing façade over the container runtime.
// Workspace depends only on the capability groups it actually consumes.
type runtimeService interface {
	ctr.ContainerService
	ctr.WorkloadService
	ctr.NetworkService
	ctr.SnapshotService
}
