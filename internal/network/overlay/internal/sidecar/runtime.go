package sidecar

import (
	"context"

	ctr "github.com/felinics/memoh/internal/container"
)

// Runtime is the container runtime surface needed to manage overlay sidecars.
type Runtime interface {
	CreateContainer(ctx context.Context, req ctr.CreateContainerRequest) (ctr.ContainerInfo, error)
	GetContainer(ctx context.Context, id string) (ctr.ContainerInfo, error)
	DeleteContainer(ctx context.Context, id string, opts *ctr.DeleteContainerOptions) error
	StartContainer(ctx context.Context, containerID string, opts *ctr.StartTaskOptions) error
	StopContainer(ctx context.Context, containerID string, opts *ctr.StopTaskOptions) error
	DeleteTask(ctx context.Context, containerID string, opts *ctr.DeleteTaskOptions) error
	GetTaskInfo(ctx context.Context, containerID string) (ctr.TaskInfo, error)
}
