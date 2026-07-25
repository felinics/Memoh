package workspace

import "github.com/memohai/memoh/domains/runtime/container"

const (
	WorkspaceResourceCPUMillicoresLabelKey = "memoh.workspace.resource.cpu_millicores"
	WorkspaceResourceMemoryBytesLabelKey   = "memoh.workspace.resource.memory_bytes"

	ResourceLimitStatusApplied         = "applied"
	ResourceLimitStatusNotCreated      = "not_created"
	ResourceLimitStatusPendingRecreate = "pending_recreate"
	ResourceLimitStatusUnsupported     = "unsupported"
)

type ResourceLimitCapability struct {
	HardLimitSupported bool
	SoftLimitSupported bool
}

type ResourceLimitCapabilities struct {
	CPU     ResourceLimitCapability
	Memory  ResourceLimitCapability
	Storage ResourceLimitCapability
}

type ResourceLimitObserved struct {
	CPUUsagePercent      float64
	MemoryUsageBytes     uint64
	MemoryLimitBytes     uint64
	StorageUsedBytes     uint64
	StorageOverSoftLimit bool
}

type ResourceLimitsResult struct {
	Desired          container.ResourceLimits
	Applied          container.ResourceLimits
	Capabilities     ResourceLimitCapabilities
	Observed         ResourceLimitObserved
	Status           string
	RequiresRecreate bool
	WorkspaceBackend string
	RuntimeBackend   string
}
