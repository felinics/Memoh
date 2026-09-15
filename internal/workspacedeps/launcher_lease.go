package workspacedeps

import (
	"context"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/workspace/payloadlease"
)

// AcquireLauncher leases before discovery so collection cannot delete a path
// handed to a caller which has not started its process yet. It never installs.
func (s *Service) AcquireLauncher(ctx context.Context, botID, depID string) (external.Launcher, *payloadlease.Lease, error) {
	client, root, err := s.target(ctx, botID)
	if err != nil {
		return external.Launcher{}, nil, err
	}
	if !isPlainFileName(depID) {
		return external.Launcher{}, nil, ErrDependencyNotFound
	}
	lease, err := payloadlease.Acquire(ctx, client, payloadlease.LockPath(root, depID))
	if err != nil {
		return external.Launcher{}, nil, err
	}
	launcher, err := s.ResolveLauncher(ctx, botID, depID)
	if err != nil {
		lease.Release()
		return external.Launcher{}, nil, err
	}
	launcher.LeasePath, launcher.WorkspaceEpoch = payloadlease.EntrypointLockPath(root, depID, launcher.Path), lease.Epoch
	return launcher, lease, nil
}
