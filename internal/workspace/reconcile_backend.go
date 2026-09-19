package workspace

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"

	"github.com/felinics/memoh/internal/botworkspace"
	"github.com/felinics/memoh/internal/config"
	ctr "github.com/felinics/memoh/internal/container"
)

// The Manager is the botworkspace.Backend for every native container runtime.
// Each step is idempotent: StartWithResolvedConfig reuses an existing
// container, PrepareImageForCreate skips a present image, CleanupBotContainer
// treats a missing container as success.
var _ botworkspace.Backend = (*Manager)(nil)

// Provision implements botworkspace.Backend. image overrides the bot's
// resolved image when non-empty. Failures carry the phase and whether the
// reconciler should retry them.
func (m *Manager) Provision(ctx context.Context, botID, image string, progress func(botworkspace.ProgressEvent)) error {
	emit := func(ev ContainerSetupEvent) {
		if progress != nil {
			progress(toProgressEvent(ev))
		}
	}
	return m.provisionWorkspace(ctx, botID, image, emit)
}

// Teardown implements botworkspace.Backend.
func (m *Manager) Teardown(ctx context.Context, botID string, preserve bool) error {
	return m.CleanupBotContainer(ctx, botID, preserve)
}

// Inspect implements botworkspace.Backend.
func (m *Manager) Inspect(ctx context.Context, botID string) (botworkspace.Inspection, error) {
	containerID := m.resolveContainerID(ctx, botID)
	info, err := m.service.GetContainer(ctx, containerID)
	if err != nil {
		if ctr.IsNotFound(err) {
			return botworkspace.Inspection{}, nil
		}
		return botworkspace.Inspection{}, err
	}
	return botworkspace.Inspection{
		Exists:  true,
		Running: m.isTaskRunning(ctx, containerID),
		Image:   config.NormalizeImageRef(strings.TrimSpace(info.Image)),
	}, nil
}

// provisionWorkspace runs the setup steps and attributes failures to phases.
// It is the only code path that creates a bot workspace.
func (m *Manager) provisionWorkspace(ctx context.Context, botID, imageOverride string, emit func(ContainerSetupEvent)) error {
	image := strings.TrimSpace(imageOverride)
	if image == "" {
		resolved, err := m.resolveWorkspaceImage(ctx, botID)
		if err != nil {
			m.logger.ErrorContext(ctx, "provision: resolve image failed", slog.String("bot_id", botID), slog.Any("error", err))
			return stepError(botworkspace.PhaseImagePrepare, err, true)
		}
		image = resolved
	} else {
		image = config.NormalizeImageRef(image)
	}

	emit(ContainerSetupEvent{Type: "pulling", Image: image})
	result, err := m.PrepareImageForCreate(ctx, image, &ctr.PullImageOptions{
		Unpack:        true,
		StorageDriver: m.cfg.Snapshotter,
		OnProgress: func(p ctr.PullProgress) {
			emit(ContainerSetupEvent{Type: "pull_progress", Layers: p.Layers})
		},
	})
	if err != nil {
		m.logger.ErrorContext(ctx, "provision: prepare image failed", slog.String("bot_id", botID), slog.String("image", image), slog.Any("error", err))
		// A registry that says the image does not exist will keep saying so;
		// only transport-level failures are worth retrying.
		return stepError(botworkspace.PhaseImagePrepare, err, isTransient(err))
	}
	if strings.TrimSpace(result.ImageRef) != "" {
		image = result.ImageRef
	}
	switch result.Mode {
	case ImagePrepareSkipped:
		emit(ContainerSetupEvent{Type: "pull_skipped", Image: image, Message: result.Message})
	case ImagePrepareDelegated:
		emit(ContainerSetupEvent{Type: "pull_delegated", Image: image, Message: result.Message})
	}

	gpu, err := m.resolveWorkspaceGPU(ctx, botID)
	if err != nil {
		return stepError(botworkspace.PhaseStart, err, true)
	}

	emit(ContainerSetupEvent{Type: "creating"})
	hadPreservedData := m.HasPreservedData(botID)
	if hadPreservedData {
		emit(ContainerSetupEvent{Type: "restoring"})
	}
	if err := m.StartWithResolvedConfig(ctx, botID, image, gpu); err != nil {
		m.logger.ErrorContext(ctx, "provision: start failed", slog.String("bot_id", botID), slog.Any("error", err))
		return stepError(botworkspace.PhaseStart, err, true)
	}
	if err := m.WaitForWorkspaceReady(ctx, botID); err != nil {
		m.logger.ErrorContext(ctx, "provision: bridge not ready", slog.String("bot_id", botID), slog.Any("error", err))
		return stepError(botworkspace.PhaseBridge, err, true)
	}
	if err := m.InitializeNativeWorkspace(ctx, botID); err != nil {
		m.logger.ErrorContext(ctx, "provision: workspace initialization failed", slog.String("bot_id", botID), slog.Any("error", err))
		// A template that cannot be written will not fix itself.
		return stepError(botworkspace.PhaseBootstrap, err, !errors.Is(err, ErrWorkspaceTemplateBootstrapFailed))
	}
	if err := m.RememberWorkspaceImage(ctx, botID, image); err != nil {
		m.logger.WarnContext(ctx, "provision: remember workspace image failed", slog.String("bot_id", botID), slog.String("image", image), slog.Any("error", err))
	}

	containerID := m.resolveContainerID(ctx, botID)
	m.upsertContainerRecord(ctx, botID, containerID, "running", image)
	event := ContainerSetupEvent{
		Type:             "complete",
		Image:            image,
		ContainerID:      containerID,
		WorkspaceBackend: workspaceBackendFromRecord(""),
		Snapshotter:      m.cfg.Snapshotter,
		Started:          true,
		DataRestored:     hadPreservedData && !m.HasPreservedData(botID),
		HasPreservedData: m.HasPreservedData(botID),
	}
	if status, err := m.GetContainerInfo(ctx, botID); err == nil {
		event.ContainerID = status.ContainerID
		event.WorkspaceBackend = status.WorkspaceBackend
		event.RuntimeBackend = status.RuntimeBackend
		event.ContainerPath = status.ContainerPath
		event.CDIDevices = status.CDIDevices
		event.HasPreservedData = status.HasPreservedData
	}
	emit(event)
	return nil
}

func stepError(phase string, err error, retryable bool) error {
	// Budget exhaustion is always worth another attempt on a fresh budget.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		retryable = true
	}
	return &botworkspace.StepError{Phase: phase, Retryable: retryable, Err: err}
}

// isTransient classifies runtime errors that a later attempt may not see
// again: timeouts, network errors, and control-plane conflicts such as an
// operation still in flight on the Cloud builtin backend.
func isTransient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.Is(err, ctr.ErrConflict) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{"timeout", "timed out", "connection refused", "connection reset", "temporarily unavailable", "eof", "no such host", "tls handshake"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

func toProgressEvent(ev ContainerSetupEvent) botworkspace.ProgressEvent {
	return botworkspace.ProgressEvent{
		Type:             ev.Type,
		Image:            ev.Image,
		Message:          ev.Message,
		Layers:           ev.Layers,
		ContainerID:      ev.ContainerID,
		WorkspaceBackend: ev.WorkspaceBackend,
		RuntimeBackend:   ev.RuntimeBackend,
		ContainerPath:    ev.ContainerPath,
		CDIDevices:       ev.CDIDevices,
		Snapshotter:      ev.Snapshotter,
		Started:          ev.Started,
		DataRestored:     ev.DataRestored,
		HasPreservedData: ev.HasPreservedData,
	}
}
