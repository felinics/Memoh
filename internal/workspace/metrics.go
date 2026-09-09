package workspace

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/containerd/containerd/v2/core/mount"

	ctr "github.com/felinics/memoh/internal/container"
)

const unsupportedReasonBackend = "backend_not_supported"

func (m *Manager) GetContainerMetrics(ctx context.Context, botID string) (*ContainerMetricsResult, error) {
	result := &ContainerMetricsResult{
		Supported: true,
		Status: ContainerMetricsStatus{
			Exists: false,
		},
	}

	containerID, err := m.ContainerID(ctx, botID)
	if err != nil {
		if errors.Is(err, ErrContainerNotFound) {
			return result, nil
		}
		return nil, err
	}

	info, err := m.service.GetContainer(ctx, containerID)
	if err != nil {
		if ctr.IsNotFound(err) {
			return result, nil
		}
		return nil, err
	}

	result.Status.Exists = true

	taskInfo, err := m.service.GetTaskInfo(ctx, containerID)
	if err == nil {
		result.Status.TaskRunning = taskInfo.Status == ctr.TaskStatusRunning
	} else if !ctr.IsNotFound(err) {
		return nil, err
	}

	runtimeMetrics, err := m.service.GetContainerMetrics(ctx, containerID)
	switch {
	case err == nil:
		result.CPU = runtimeMetrics.CPU
		result.Memory = runtimeMetrics.Memory
		result.SampledAt = runtimeMetrics.SampledAt
	case errors.Is(err, ctr.ErrNotSupported):
		result.Supported = false
		result.UnsupportedReason = unsupportedReasonBackend
	case ctr.IsNotFound(err):
		if result.Status.TaskRunning {
			result.Supported = false
			result.UnsupportedReason = unsupportedReasonBackend
		}
	default:
		return nil, err
	}

	if result.Supported {
		storage, err := m.collectStorageMetrics(ctx, info)
		if err != nil {
			if errors.Is(err, ctr.ErrNotSupported) {
				return result, nil
			}
			return nil, err
		}
		result.Storage = storage
		if result.SampledAt.IsZero() {
			result.SampledAt = time.Now()
		}
	}

	return result, nil
}

func (m *Manager) collectStorageMetrics(ctx context.Context, info ctr.ContainerInfo) (*ContainerStorageMetrics, error) {
	mounts, err := m.snapshotMounts(ctx, info)
	if err != nil {
		if errors.Is(err, errMountNotSupported) {
			return nil, ctr.ErrNotSupported
		}
		return nil, err
	}

	var usedBytes uint64
	if err := mount.WithReadonlyTempMount(ctx, mounts, func(root string) error {
		if _, statErr := os.Stat(root); statErr != nil {
			if os.IsNotExist(statErr) {
				return nil
			}
			return statErr
		}

		size, sizeErr := dirSize(root)
		if sizeErr != nil {
			return sizeErr
		}
		usedBytes = size
		return nil
	}); err != nil {
		return nil, err
	}

	return &ContainerStorageMetrics{
		Path:      "/",
		UsedBytes: usedBytes,
	}, nil
}

func dirSize(root string) (uint64, error) {
	var size uint64
	err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}
		fileSize := info.Size()
		if fileSize > 0 {
			size += uint64(fileSize) //nolint:gosec // file sizes are checked to be positive before conversion
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return size, nil
}
