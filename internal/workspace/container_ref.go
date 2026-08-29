package workspace

import (
	"context"

	ctr "github.com/felinics/memoh/internal/container"
)

// lockedContainerRef holds a per-container lock together with the latest
// container metadata so mutation workflows do not have to repeat the same
// resolve -> lock -> load sequence.
type lockedContainerRef struct {
	manager     *Manager
	botID       string
	containerID string
	info        ctr.ContainerInfo
	unlock      func()
}

func (m *Manager) loadLockedContainer(ctx context.Context, botID string) (*lockedContainerRef, error) {
	containerID := m.resolveContainerID(ctx, botID)
	unlock := m.lockContainer(containerID)

	info, err := m.service.GetContainer(ctx, containerID)
	if err != nil {
		unlock()
		return nil, err
	}

	return &lockedContainerRef{
		manager:     m,
		botID:       botID,
		containerID: containerID,
		info:        info,
		unlock:      unlock,
	}, nil
}

func (r *lockedContainerRef) Close() {
	if r == nil || r.unlock == nil {
		return
	}
	r.unlock()
	r.unlock = nil
}

func (r *lockedContainerRef) StopTaskForMutation(ctx context.Context) (func(), error) {
	return r.manager.stopTaskForMutation(ctx, r.botID, r.containerID)
}

func (r *lockedContainerRef) EnsureDBRecords(ctx context.Context) error {
	_, err := r.manager.ensureDBRecords(ctx, r.botID, r.info.ID, r.info.Runtime.Name, r.info.Image)
	return err
}
