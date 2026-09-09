package network

import (
	"context"
	"errors"
	"strings"

	ctr "github.com/felinics/memoh/internal/container"
)

type containerRuntimeService interface {
	SetupNetwork(ctx context.Context, req ctr.NetworkRequest) (ctr.NetworkResult, error)
	RemoveNetwork(ctx context.Context, req ctr.NetworkRequest) error
	CheckNetwork(ctx context.Context, req ctr.NetworkRequest) error
}

type containerRuntime struct {
	svc  containerRuntimeService
	desc RuntimeDescriptor
}

// NewContainerRuntimeFromBackend wraps the current runtime network service with
// the network package's runtime abstraction.
func NewContainerRuntimeFromBackend(backend string, svc containerRuntimeService) Runtime {
	return &containerRuntime{
		svc:  svc,
		desc: descriptorForBackend(backend),
	}
}

func (r *containerRuntime) Kind() string {
	return r.desc.Kind
}

func (r *containerRuntime) Descriptor() RuntimeDescriptor {
	return r.desc
}

func (r *containerRuntime) EnsureNetwork(ctx context.Context, req RuntimeNetworkRequest) (RuntimeNetworkStatus, error) {
	result, err := r.svc.SetupNetwork(ctx, ctr.NetworkRequest{
		ContainerID: req.ContainerID,
		JoinTarget: ctr.NetworkJoinTarget{
			Kind:  req.JoinTarget.Kind,
			Value: req.JoinTarget.Path,
			PID:   req.JoinTarget.PID,
		},
	})
	if err != nil {
		return RuntimeNetworkStatus{}, err
	}
	ip := strings.TrimSpace(result.IP)
	return RuntimeNetworkStatus{
		Attached: ip != "",
		IP:       ip,
	}, nil
}

func (r *containerRuntime) RemoveNetwork(ctx context.Context, req RuntimeNetworkRequest) error {
	return r.svc.RemoveNetwork(ctx, ctr.NetworkRequest{
		ContainerID: req.ContainerID,
		JoinTarget: ctr.NetworkJoinTarget{
			Kind:  req.JoinTarget.Kind,
			Value: req.JoinTarget.Path,
			PID:   req.JoinTarget.PID,
		},
	})
}

func (r *containerRuntime) StatusNetwork(ctx context.Context, req RuntimeNetworkRequest) (RuntimeNetworkStatus, error) {
	err := r.svc.CheckNetwork(ctx, ctr.NetworkRequest{
		ContainerID: req.ContainerID,
		JoinTarget: ctr.NetworkJoinTarget{
			Kind:  req.JoinTarget.Kind,
			Value: req.JoinTarget.Path,
			PID:   req.JoinTarget.PID,
		},
	})
	if err != nil {
		if errors.Is(err, ctr.ErrNotSupported) {
			return RuntimeNetworkStatus{}, ErrNotSupported
		}
		return RuntimeNetworkStatus{}, err
	}
	return RuntimeNetworkStatus{Attached: true}, nil
}

func descriptorForBackend(backend string) RuntimeDescriptor {
	switch normalizeKind(backend) {
	case "", "containerd":
		return RuntimeDescriptor{
			Kind:        "containerd",
			DisplayName: "containerd",
			Capabilities: RuntimeCapabilities{
				SidecarWorker:       true,
				RuntimeNetworkSetup: true,
				JoinNamespacePath:   true,
				CNI:                 true,
				Devices:             true,
				Capabilities:        true,
				Privileged:          true,
			},
		}
	case "docker":
		return RuntimeDescriptor{
			Kind:        "docker",
			DisplayName: "Docker",
			Capabilities: RuntimeCapabilities{
				JoinContainerNetwork: true,
			},
		}
	case "apple":
		return RuntimeDescriptor{
			Kind:         "apple",
			DisplayName:  "Apple Container",
			Capabilities: RuntimeCapabilities{},
		}
	default:
		return RuntimeDescriptor{
			Kind:         normalizeKind(backend),
			DisplayName:  backend,
			Capabilities: RuntimeCapabilities{},
		}
	}
}
