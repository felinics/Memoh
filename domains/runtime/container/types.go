package container

import (
	"errors"
	"time"
)

var (
	ErrInvalidArgument = errors.New("invalid argument")
	ErrNotSupported    = errors.New("operation not supported on this backend")
	ErrNotFound        = errors.New("not found")
	ErrAlreadyExists   = errors.New("already exists")
	ErrConflict        = errors.New("conflict")
	ErrRuntime         = errors.New("runtime operation failed")
)

func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

func IsAlreadyExists(err error) bool {
	return errors.Is(err, ErrAlreadyExists)
}

func IsConflict(err error) bool {
	return errors.Is(err, ErrConflict)
}

func IsRuntime(err error) bool {
	return errors.Is(err, ErrRuntime)
}

const (
	// StorageKeyLabel stores the runtime-specific active storage key on backends
	// whose container metadata does not expose one natively.
	StorageKeyLabel = "memoh.storage_key"
)

type TaskStatus int

const (
	TaskStatusUnknown TaskStatus = iota
	TaskStatusCreated
	TaskStatusRunning
	TaskStatusStopped
	TaskStatusPaused
)

func (s TaskStatus) String() string {
	switch s {
	case TaskStatusCreated:
		return "CREATED"
	case TaskStatusRunning:
		return "RUNNING"
	case TaskStatusStopped:
		return "STOPPED"
	case TaskStatusPaused:
		return "PAUSED"
	default:
		return "UNKNOWN"
	}
}

type ContainerInfo struct {
	ID         string
	Image      string
	Labels     map[string]string
	StorageRef StorageRef
	Runtime    RuntimeInfo
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type StorageRef struct {
	Driver string
	Key    string
	Kind   string
}

type SnapshotRef struct {
	Driver string
	Key    string
	Kind   string
}

type CommitSnapshotRequest struct {
	Source StorageRef
	Target SnapshotRef
}

type ListSnapshotsRequest struct {
	Driver string
}

type PrepareSnapshotRequest struct {
	Target StorageRef
	Parent SnapshotRef
}

type RuntimeInfo struct {
	Name string
}

type ImageInfo struct {
	Name string
	ID   string
	Tags []string
}

type TaskInfo struct {
	ContainerID       string
	ID                string
	PID               uint32
	NetworkJoinTarget NetworkJoinTarget
	Status            TaskStatus
	ExitCode          uint32
}

type ContainerMetrics struct {
	SampledAt time.Time
	CPU       *CPUMetrics
	Memory    *MemoryMetrics
}

type CPUMetrics struct {
	UsagePercent      float64
	UsageNanoseconds  uint64
	UsageNanocores    uint64
	UserNanoseconds   uint64
	KernelNanoseconds uint64
}

type MemoryMetrics struct {
	UsageBytes   uint64
	LimitBytes   uint64
	UsagePercent float64
}

type SnapshotUsage struct {
	SizeBytes uint64
	Inodes    uint64
}

type SnapshotInfo struct {
	Name    string
	Parent  string
	Kind    string
	Created time.Time
	Updated time.Time
	Labels  map[string]string
}

// ResourceLimits contains desired hard/soft workspace resource limits.
// A zero value for a field means unlimited.
type ResourceLimits struct {
	CPUMillicores int64
	MemoryBytes   int64
	StorageBytes  int64
}

type MountInfo struct {
	Type    string
	Source  string
	Target  string
	Options []string
}

type MountSpec struct {
	Destination string
	Type        string
	Source      string
	Options     []string
}

type ContainerSpec struct {
	Cmd               []string
	Env               []string
	WorkDir           string
	User              string
	Mounts            []MountSpec
	DNS               []string
	NetworkJoinTarget NetworkJoinTarget
	AddedCapabilities []string
	// CDIDevices contains fully-qualified CDI device names such as
	// "nvidia.com/gpu=0" or "amd.com/gpu=0".
	CDIDevices []string
	TTY        bool
}

// NetworkJoinTarget is an adapter-provided handle for runtimes that can attach
// another workload to the same network stack.
type NetworkJoinTarget struct {
	Kind  string
	Value string
	PID   uint32
}

type LayerStatus struct {
	Ref    string `json:"ref"`
	Offset int64  `json:"offset"`
	Total  int64  `json:"total"`
}

type PullProgress struct {
	Layers []LayerStatus `json:"layers"`
}

// NetworkRequest describes a runtime-level network readiness request for a
// workspace container. Backend-specific details such as CNI configuration live
// inside the adapter.
type NetworkRequest struct {
	ContainerID string
	JoinTarget  NetworkJoinTarget
}

// NetworkResult captures the outcome of attaching the container to the default
// container network.
type NetworkResult struct {
	IP string
}
