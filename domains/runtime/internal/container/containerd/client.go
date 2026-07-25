package containerd

import (
	"context"

	containerdclient "github.com/containerd/containerd/v2/client"

	containerapi "github.com/memohai/memoh/domains/runtime/container"
)

func NewClient(_ context.Context, socketPath string) (*containerdclient.Client, error) {
	if socketPath == "" {
		socketPath = containerapi.DefaultSocketPath
	}
	return containerdclient.New(socketPath)
}
