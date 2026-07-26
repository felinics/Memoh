package assembly

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

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

// ManagedService keeps container operations separate from backend lifecycle so
// command composition can register Start and Stop without inspecting the
// concrete implementation.
type ManagedService struct {
	Service container.Service
	start   func(context.Context) error
	stop    func(context.Context) error
}

// managedCloser adapts finite Close methods that do not accept a context. It
// starts close exactly once and lets each caller bound how long it waits. A
// caller that times out can retry and join the same close result.
type managedCloser struct {
	once    sync.Once
	done    chan struct{}
	closeFn func() error
	err     error
}

func newManagedCloser(closeFn func() error) *managedCloser {
	return &managedCloser{done: make(chan struct{}), closeFn: closeFn}
}

func (c *managedCloser) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	c.once.Do(func() {
		go func() {
			c.err = c.closeFn()
			close(c.done)
		}()
	})
	select {
	case <-c.done:
		return c.err
	default:
	}
	select {
	case <-c.done:
		return c.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *ManagedService) Start(ctx context.Context) error {
	if m.start == nil {
		return nil
	}
	return m.start(ctx)
}

func (m *ManagedService) Stop(ctx context.Context) error {
	if m.stop == nil {
		return nil
	}
	return m.stop(ctx)
}

// NewService constructs the selected container backend without starting any
// backend-managed process. Process startup belongs to ManagedService.Start.
func NewService(ctx context.Context, deps Deps) (*ManagedService, error) {
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	switch deps.Backend {
	case container.BackendApple:
		svc := appleadapter.NewService(log, appleadapter.ServiceConfig{
			SocketPath: deps.Apple.SocketPath,
			BinaryPath: deps.Apple.BinaryPath,
		})
		return &ManagedService{
			Service: svc,
			start:   svc.Start,
			stop: func(ctx context.Context) error {
				return svc.Close(ctx)
			},
		}, nil
	case container.BackendDocker:
		svc, err := dockeradapter.NewService(log, dockeradapter.Options{
			Host: deps.Docker.Host,
		})
		if err != nil {
			return nil, err
		}
		closer := newManagedCloser(svc.Close)
		return &ManagedService{Service: svc, stop: closer.Stop}, nil
	case container.BackendContainerd:
		client, err := containerdadapter.NewClient(ctx, deps.Containerd.SocketPath)
		if err != nil {
			return nil, fmt.Errorf("connect containerd: %w", err)
		}
		svc := containerdadapter.NewService(log, client, containerdadapter.Options{
			Namespace:    deps.Containerd.Namespace,
			RuntimeType:  deps.Containerd.RuntimeType,
			CNIBinaryDir: deps.Containerd.CNIBinaryDir,
			CNIConfigDir: deps.Containerd.CNIConfigDir,
		})
		closer := newManagedCloser(client.Close)
		return &ManagedService{Service: svc, stop: closer.Stop}, nil
	default:
		return nil, fmt.Errorf("unsupported container backend %q", deps.Backend)
	}
}
