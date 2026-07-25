package assembly

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/memohai/memoh/domains/runtime/container"
	appleadapter "github.com/memohai/memoh/domains/runtime/internal/container/apple"
	containerdadapter "github.com/memohai/memoh/domains/runtime/internal/container/containerd"
	dockeradapter "github.com/memohai/memoh/domains/runtime/internal/container/docker"
)

// AppleOptions configures the Apple/Socktainer backend.
type AppleOptions struct {
	SocketPath string
	BinaryPath string
}

// DockerOptions configures the Docker backend.
type DockerOptions struct {
	Host string
}

// ContainerdOptions configures the containerd backend.
type ContainerdOptions struct {
	SocketPath   string
	Namespace    string
	RuntimeType  string
	CNIBinaryDir string
	CNIConfigDir string
}

// Deps are the explicit public inputs required to assemble a container Service.
type Deps struct {
	Log        *slog.Logger
	Backend    string
	Apple      AppleOptions
	Docker     DockerOptions
	Containerd ContainerdOptions
}

// NewService constructs the workspace-facing container runtime Service for the
// selected backend. The returned cleanup must be called on process shutdown.
func NewService(ctx context.Context, deps Deps) (container.Service, func(), error) {
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	switch deps.Backend {
	case container.BackendApple:
		svc, err := appleadapter.NewService(ctx, log, appleadapter.ServiceConfig{
			SocketPath: deps.Apple.SocketPath,
			BinaryPath: deps.Apple.BinaryPath,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("create apple container service: %w", err)
		}
		return svc, func() { _ = svc.Close() }, nil
	case container.BackendDocker:
		svc, err := dockeradapter.NewService(log, dockeradapter.Options{
			Host: deps.Docker.Host,
		})
		if err != nil {
			return nil, nil, err
		}
		return svc, func() { _ = svc.Close() }, nil
	case container.BackendContainerd:
		client, err := containerdadapter.NewClient(ctx, deps.Containerd.SocketPath)
		if err != nil {
			return nil, nil, fmt.Errorf("connect containerd: %w", err)
		}
		svc := containerdadapter.NewService(log, client, containerdadapter.Options{
			Namespace:    deps.Containerd.Namespace,
			RuntimeType:  deps.Containerd.RuntimeType,
			CNIBinaryDir: deps.Containerd.CNIBinaryDir,
			CNIConfigDir: deps.Containerd.CNIConfigDir,
		})
		return svc, func() { _ = client.Close() }, nil
	default:
		return nil, nil, fmt.Errorf("unsupported container backend %q", deps.Backend)
	}
}
