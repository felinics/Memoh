package containerd

import containerapi "github.com/felinics/memoh/internal/container"

var (
	ErrInvalidArgument = containerapi.ErrInvalidArgument
	ErrNotSupported    = containerapi.ErrNotSupported
	ErrNotFound        = containerapi.ErrNotFound
	ErrAlreadyExists   = containerapi.ErrAlreadyExists
	ErrRuntime         = containerapi.ErrRuntime
)

type (
	PullImageOptions       = containerapi.PullImageOptions
	DeleteImageOptions     = containerapi.DeleteImageOptions
	CreateContainerRequest = containerapi.CreateContainerRequest
	DeleteContainerOptions = containerapi.DeleteContainerOptions
	StartTaskOptions       = containerapi.StartTaskOptions
	StopTaskOptions        = containerapi.StopTaskOptions
	DeleteTaskOptions      = containerapi.DeleteTaskOptions
	ListTasksOptions       = containerapi.ListTasksOptions
)

type (
	ImageInfo              = containerapi.ImageInfo
	ContainerInfo          = containerapi.ContainerInfo
	RuntimeInfo            = containerapi.RuntimeInfo
	TaskInfo               = containerapi.TaskInfo
	TaskStatus             = containerapi.TaskStatus
	ContainerMetrics       = containerapi.ContainerMetrics
	CPUMetrics             = containerapi.CPUMetrics
	MemoryMetrics          = containerapi.MemoryMetrics
	ResourceLimits         = containerapi.ResourceLimits
	SnapshotUsage          = containerapi.SnapshotUsage
	SnapshotInfo           = containerapi.SnapshotInfo
	MountInfo              = containerapi.MountInfo
	MountSpec              = containerapi.MountSpec
	ContainerSpec          = containerapi.ContainerSpec
	StorageRef             = containerapi.StorageRef
	SnapshotRef            = containerapi.SnapshotRef
	CommitSnapshotRequest  = containerapi.CommitSnapshotRequest
	ListSnapshotsRequest   = containerapi.ListSnapshotsRequest
	PrepareSnapshotRequest = containerapi.PrepareSnapshotRequest
	NetworkJoinTarget      = containerapi.NetworkJoinTarget
	PullProgress           = containerapi.PullProgress
	LayerStatus            = containerapi.LayerStatus
	NetworkRequest         = containerapi.NetworkRequest
	NetworkResult          = containerapi.NetworkResult
)

const (
	DefaultSocketPath = containerapi.DefaultSocketPath
	DefaultNamespace  = containerapi.DefaultNamespace

	TaskStatusUnknown = containerapi.TaskStatusUnknown
	TaskStatusCreated = containerapi.TaskStatusCreated
	TaskStatusRunning = containerapi.TaskStatusRunning
	TaskStatusStopped = containerapi.TaskStatusStopped
	TaskStatusPaused  = containerapi.TaskStatusPaused
)
