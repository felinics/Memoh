package workspace

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/felinics/memoh/internal/config"
	ctr "github.com/felinics/memoh/internal/container"
	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	netctl "github.com/felinics/memoh/internal/network"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// ---------------------------------------------------------------------------
// Container ID resolution
// ---------------------------------------------------------------------------

// ContainerID resolves the containerd container ID for a bot.
// Resolution order: DB lookup → label search → full container scan.
func (m *Manager) ContainerID(ctx context.Context, botID string) (string, error) {
	if m.queries != nil {
		pgBotID, err := db.ParseUUID(botID)
		if err == nil {
			row, dbErr := m.queries.GetContainerByBotID(ctx, pgBotID)
			if dbErr == nil &&
				strings.EqualFold(strings.TrimSpace(row.WorkspaceBackend), bridge.WorkspaceBackendContainer) &&
				strings.TrimSpace(row.ContainerID) != "" {
				return row.ContainerID, nil
			}
			if dbErr != nil && !errors.Is(dbErr, pgx.ErrNoRows) {
				m.logger.Warn("ContainerID: db lookup failed",
					slog.String("bot_id", botID), slog.Any("error", dbErr))
			}
		}
	}

	containers, err := m.service.ListContainersByLabel(ctx, BotLabelKey, botID)
	if err != nil {
		return "", err
	}
	if id, ok := newestContainerID(containers); ok {
		return id, nil
	}

	containers, err = m.service.ListContainers(ctx)
	if err != nil {
		return "", err
	}
	matched := make([]ctr.ContainerInfo, 0, len(containers))
	for _, info := range containers {
		resolvedBotID, ok := BotIDFromContainerInfo(info)
		if !ok || resolvedBotID != botID {
			continue
		}
		matched = append(matched, info)
	}
	if id, ok := newestContainerID(matched); ok {
		return id, nil
	}

	return "", ErrContainerNotFound
}

func newestContainerID(containers []ctr.ContainerInfo) (string, bool) {
	bestID := ""
	var bestUpdated time.Time
	for _, info := range containers {
		if bestID == "" || info.UpdatedAt.After(bestUpdated) {
			bestID = info.ID
			bestUpdated = info.UpdatedAt
		}
	}
	return bestID, bestID != ""
}

// ---------------------------------------------------------------------------
// Task & network helpers
// ---------------------------------------------------------------------------

func (m *Manager) isTaskRunning(ctx context.Context, containerID string) bool {
	task, err := m.service.GetTaskInfo(ctx, containerID)
	return err == nil && task.Status == ctr.TaskStatusRunning
}

func (m *Manager) networkAttachmentRequest(ctx context.Context, botID, containerID string) netctl.AttachmentRequest {
	runtimeReq := netctl.RuntimeNetworkRequest{
		ContainerID: containerID,
	}
	if task, err := m.service.GetTaskInfo(ctx, containerID); err == nil {
		runtimeReq.JoinTarget = netctl.NetworkJoinTarget{
			Kind: task.NetworkJoinTarget.Kind,
			Path: task.NetworkJoinTarget.Value,
			PID:  task.NetworkJoinTarget.PID,
		}
	}
	return netctl.AttachmentRequest{
		BotID:       botID,
		ContainerID: containerID,
		Runtime:     runtimeReq,
	}
}

func (m *Manager) ensureContainerNetworkAndGetIP(ctx context.Context, botID, containerID string) (string, error) {
	var lastErr error
	for attempt := range 2 {
		result, err := m.networkController.EnsureAttached(ctx, m.networkAttachmentRequest(ctx, botID, containerID))
		if err != nil {
			lastErr = err
			m.logger.Warn("network setup attempt failed",
				slog.String("container_id", containerID),
				slog.Int("attempt", attempt+1),
				slog.Any("error", err))
			continue
		}
		if strings.TrimSpace(result.Runtime.IP) == "" {
			lastErr = fmt.Errorf("network setup returned no IP for %s", containerID)
			continue
		}
		return result.Runtime.IP, nil
	}
	return "", fmt.Errorf("network setup failed for workspace runtime %s: %w", containerID, lastErr)
}

func (m *Manager) ensureContainerNetwork(ctx context.Context, containerID, botID string) error {
	ip, err := m.ensureContainerNetworkAndGetIP(ctx, botID, containerID)
	if err != nil {
		return err
	}
	if m.IsLegacyContainer(ctx, containerID) {
		m.SetLegacyIP(botID, ip)
	}
	return nil
}

func (m *Manager) removeContainerNetwork(ctx context.Context, botID, containerID string) error {
	return m.networkController.Detach(ctx, m.networkAttachmentRequest(ctx, botID, containerID))
}

func (m *Manager) startTaskAndEnsureNetwork(ctx context.Context, botID, containerID string) error {
	if err := m.service.StartContainer(ctx, containerID, nil); err != nil {
		if !m.waitTaskRunning(ctx, containerID, 5*time.Second) {
			return err
		}
	}
	return m.ensureContainerNetwork(ctx, containerID, botID)
}

func (m *Manager) waitTaskRunning(ctx context.Context, containerID string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		task, err := m.service.GetTaskInfo(ctx, containerID)
		if err == nil && task.Status == ctr.TaskStatusRunning {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// ---------------------------------------------------------------------------
// Lifecycle: ensure / stop / info
// ---------------------------------------------------------------------------

// EnsureRunning verifies the container exists and its task is running.
// If the container is missing, it rebuilds via SetupBotContainer.
// If the task is stopped, it restarts and sets up networking.
func (m *Manager) EnsureRunning(ctx context.Context, botID string) error {
	if m.remote != nil {
		primary, err := m.remote.EnsurePrimaryReady(ctx, botID)
		if primary || err != nil {
			return err
		}
	}
	return m.EnsureNativeRunning(ctx, botID)
}

// EnsureNativeRunning manages only the server-owned container workspace,
// regardless of which target is currently Primary.
func (m *Manager) EnsureNativeRunning(ctx context.Context, botID string) error {
	containerID, err := m.ContainerID(ctx, botID)
	if err != nil {
		if errors.Is(err, ErrContainerNotFound) {
			m.logger.Warn("container missing, rebuilding", slog.String("bot_id", botID))
			return m.SetupBotContainer(ctx, botID)
		}
		return err
	}

	_, err = m.service.GetContainer(ctx, containerID)
	if err != nil {
		if !ctr.IsNotFound(err) {
			return err
		}
		m.logger.Warn("container missing in containerd, rebuilding",
			slog.String("bot_id", botID), slog.String("container_id", containerID))
		return m.SetupBotContainer(ctx, botID)
	}

	taskInfo, err := m.service.GetTaskInfo(ctx, containerID)
	if err == nil {
		if taskInfo.Status == ctr.TaskStatusRunning {
			return m.ensureContainerNetwork(ctx, containerID, botID)
		}
		if err := m.service.DeleteTask(ctx, containerID, &ctr.DeleteTaskOptions{Force: true}); err != nil {
			if !ctr.IsNotFound(err) {
				m.logger.Warn("cleanup: delete task failed",
					slog.String("container_id", containerID), slog.Any("error", err))
				return err
			}
		}
	} else if !ctr.IsNotFound(err) {
		return err
	}

	return m.startTaskAndEnsureNetwork(ctx, botID, containerID)
}

// StopBot stops the container task for a bot and marks it stopped in DB.
func (m *Manager) StopBot(ctx context.Context, botID string) error {
	containerID, err := m.ContainerID(ctx, botID)
	if err != nil {
		return err
	}

	if err := m.service.StopContainer(ctx, containerID, &ctr.StopTaskOptions{
		Timeout: 10 * time.Second,
		Force:   true,
	}); err != nil && !ctr.IsNotFound(err) {
		return err
	}
	if err := m.service.DeleteTask(ctx, containerID, &ctr.DeleteTaskOptions{Force: true}); err != nil {
		m.logger.Warn("cleanup: delete task failed",
			slog.String("container_id", containerID), slog.Any("error", err))
	}
	if err := m.removeContainerNetwork(ctx, botID, containerID); err != nil {
		m.logger.Warn("cleanup: remove network failed",
			slog.String("container_id", containerID), slog.Any("error", err))
	}

	m.markContainerStopped(ctx, botID)
	return nil
}

// GetContainerInfo returns current container status for a bot,
// combining DB records with live containerd state.
func (m *Manager) GetContainerInfo(ctx context.Context, botID string) (*ContainerStatus, error) {
	if m.queries != nil {
		pgBotID, parseErr := db.ParseUUID(botID)
		if parseErr == nil {
			row, dbErr := m.queries.GetContainerByBotID(ctx, pgBotID)
			if dbErr == nil {
				cdiDevices := []string(nil)
				runtimeBackend := ""
				if liveInfo, liveErr := m.service.GetContainer(ctx, row.ContainerID); liveErr == nil {
					cdiDevices = workspaceCDIDevicesFromLabels(liveInfo.Labels)
					runtimeBackend = liveInfo.Runtime.Name
				} else if ctr.IsNotFound(liveErr) {
					// The DB still has a record but the runtime container is gone
					// (removed out-of-band, pruned, or a half-failed create left the
					// row behind). Report it as missing so callers fall back to the
					// recreate path instead of trusting a stale record whose live
					// operations (metrics, snapshots, start/stop) all fail. Transient
					// runtime errors are intentionally tolerated: only a definitive
					// not-found invalidates the record.
					return nil, ErrContainerNotFound
				}
				createdAt := time.Time{}
				if row.CreatedAt.Valid {
					createdAt = row.CreatedAt.Time
				}
				updatedAt := time.Time{}
				if row.UpdatedAt.Valid {
					updatedAt = row.UpdatedAt.Time
				}
				taskRunning := m.isTaskRunning(ctx, row.ContainerID)
				status := row.Status
				if taskRunning {
					status = "running"
				}
				return &ContainerStatus{
					ContainerID:      row.ContainerID,
					WorkspaceBackend: workspaceBackendFromRecord(row.WorkspaceBackend),
					RuntimeBackend:   runtimeBackend,
					Image:            row.Image,
					Status:           status,
					Namespace:        row.Namespace,
					ContainerPath:    row.ContainerPath,
					CDIDevices:       cdiDevices,
					TaskRunning:      taskRunning,
					HasPreservedData: m.HasPreservedData(botID),
					Legacy:           m.IsLegacyContainer(ctx, row.ContainerID),
					CreatedAt:        createdAt,
					UpdatedAt:        updatedAt,
				}, nil
			}
		}
	}

	containerID, err := m.ContainerID(ctx, botID)
	if err != nil {
		return nil, err
	}
	info, err := m.service.GetContainer(ctx, containerID)
	if err != nil {
		if ctr.IsNotFound(err) {
			return nil, ErrContainerNotFound
		}
		return nil, err
	}
	taskRunning := m.isTaskRunning(ctx, containerID)
	status := "unknown"
	if taskRunning {
		status = "running"
	}
	return &ContainerStatus{
		ContainerID:      info.ID,
		WorkspaceBackend: workspaceBackendFromRecord(""),
		RuntimeBackend:   info.Runtime.Name,
		Image:            info.Image,
		Status:           status,
		Namespace:        m.namespace,
		CDIDevices:       workspaceCDIDevicesFromLabels(info.Labels),
		TaskRunning:      taskRunning,
		HasPreservedData: m.HasPreservedData(botID),
		Legacy:           m.IsLegacyContainer(ctx, containerID),
		CreatedAt:        info.CreatedAt,
		UpdatedAt:        info.UpdatedAt,
	}, nil
}

// ---------------------------------------------------------------------------
// Container lifecycle (bots.ContainerLifecycle interface)
// ---------------------------------------------------------------------------

type ContainerSetupEvent struct {
	Type             string
	Image            string
	Message          string
	Layers           []ctr.LayerStatus
	ContainerID      string
	WorkspaceBackend string
	RuntimeBackend   string
	ContainerPath    string
	CDIDevices       []string
	Started          bool
	DataRestored     bool
	HasPreservedData bool
}

type ContainerSetupProgress func(ContainerSetupEvent)

// SetupBotContainer creates/starts the container and upserts the DB record.
func (m *Manager) SetupBotContainer(ctx context.Context, botID string) error {
	return m.setupBotContainer(ctx, botID, nil)
}

func (m *Manager) SetupBotContainerWithProgress(ctx context.Context, botID string, progress ContainerSetupProgress) error {
	return m.setupBotContainer(ctx, botID, progress)
}

func (m *Manager) setupBotContainer(ctx context.Context, botID string, progress ContainerSetupProgress) error {
	emit := func(event ContainerSetupEvent) {
		if progress != nil {
			progress(event)
		}
	}

	image, err := m.resolveWorkspaceImage(ctx, botID)
	if err != nil {
		m.logger.Error("setup bot container: resolve image failed",
			slog.String("bot_id", botID),
			slog.Any("error", err))
		return err
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
		m.logger.Error("setup bot container: prepare image failed",
			slog.String("bot_id", botID),
			slog.String("image", image),
			slog.Any("error", err))
		return err
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
		return err
	}

	emit(ContainerSetupEvent{Type: "creating"})
	hadPreservedData := m.HasPreservedData(botID)
	if hadPreservedData {
		emit(ContainerSetupEvent{Type: "restoring"})
	}

	if err := m.StartWithResolvedConfig(ctx, botID, image, gpu); err != nil {
		m.logger.Error("setup bot container: start failed",
			slog.String("bot_id", botID),
			slog.Any("error", err))
		return err
	}
	if err := m.WaitForWorkspaceReady(ctx, botID); err != nil {
		m.logger.Error("setup bot container: bridge not ready",
			slog.String("bot_id", botID),
			slog.Any("error", err))
		return err
	}
	if err := m.InitializeNativeWorkspace(ctx, botID); err != nil {
		m.logger.Error("setup bot container: workspace initialization failed",
			slog.String("bot_id", botID),
			slog.Any("error", err))
		return err
	}
	if err := m.RememberWorkspaceImage(ctx, botID, image); err != nil {
		m.logger.Warn("setup bot container: remember workspace image failed",
			slog.String("bot_id", botID),
			slog.String("image", image),
			slog.Any("error", err))
	}

	containerID := m.resolveContainerID(ctx, botID)
	m.upsertContainerRecord(ctx, botID, containerID, "running", image)
	event := ContainerSetupEvent{
		Type:             "complete",
		Image:            image,
		ContainerID:      containerID,
		WorkspaceBackend: workspaceBackendFromRecord(""),
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

// CleanupBotContainer removes the container and DB record for a bot.
// When preserveData is true, /data is exported to a backup archive before deletion.
func (m *Manager) CleanupBotContainer(ctx context.Context, botID string, preserveData bool) error {
	if err := m.Delete(ctx, botID, preserveData); err != nil {
		if preserveData {
			// When preserving data, any error (including NotFound) must
			// block the workflow — we cannot delete the DB record if we
			// failed to preserve data.
			return err
		}
		if !ctr.IsNotFound(err) {
			return err
		}
		m.logger.Warn("cleanup: container not found in containerd, continuing",
			slog.String("bot_id", botID))
	}

	m.deleteContainerRecord(ctx, botID)
	return nil
}

// ---------------------------------------------------------------------------
// Reconciliation
// ---------------------------------------------------------------------------

// ReconcileContainers compares the DB containers table against actual containerd
// state on startup. For each auto_start container in DB it verifies the container
// and task exist; if missing they are rebuilt.
func (m *Manager) ReconcileContainers(ctx context.Context) {
	if m.queries == nil {
		return
	}
	rows, err := m.queries.ListAutoStartContainers(ctx)
	if err != nil {
		m.logger.Error("reconcile: failed to list containers from DB", slog.Any("error", err))
		return
	}
	if len(rows) == 0 {
		m.logger.Info("reconcile: no auto-start containers in DB")
		return
	}

	m.logger.Info("reconcile: checking containers", slog.Int("count", len(rows)))
	for _, row := range rows {
		containerID := row.ContainerID
		botID := uuid.UUID(row.BotID.Bytes).String()
		_, err := m.service.GetContainer(ctx, containerID)
		if err != nil {
			if !ctr.IsNotFound(err) {
				m.logger.Error("reconcile: failed to get container",
					slog.String("container_id", containerID), slog.Any("error", err))
				continue
			}
			// Container missing in containerd — rebuild.
			m.logger.Warn("reconcile: container missing, rebuilding",
				slog.String("bot_id", botID), slog.String("container_id", containerID))
			if setupErr := m.SetupBotContainer(ctx, botID); setupErr != nil {
				m.logger.Error("reconcile: rebuild failed",
					slog.String("bot_id", botID), slog.Any("error", setupErr))
				m.markContainerStatus(ctx, botID, "error")
			}
			continue
		}

		// --- legacy container support (mcp- prefix, TCP gRPC) ---
		// Remove when all deployments have migrated to workspace- containers.
		if m.IsLegacyContainer(ctx, containerID) {
			m.logger.Warn("reconcile: legacy container (pre-bridge), using TCP fallback",
				slog.String("bot_id", botID), slog.String("container_id", containerID))

			running := m.isTaskRunning(ctx, containerID)
			if !running {
				if err := m.EnsureNativeRunning(ctx, botID); err != nil {
					m.logger.Error("reconcile: failed to start legacy container",
						slog.String("bot_id", botID), slog.Any("error", err))
					continue
				}
			}
			if ip, netErr := m.ensureContainerNetworkAndGetIP(ctx, botID, containerID); netErr != nil {
				m.logger.Error("reconcile: network setup failed for legacy container",
					slog.String("bot_id", botID), slog.Any("error", netErr))
			} else {
				m.SetLegacyIP(botID, ip)
				m.logger.Info("reconcile: legacy container reachable via TCP",
					slog.String("bot_id", botID), slog.String("ip", ip))
			}
			continue
		}

		// Container exists — ensure the task is running.
		running := m.isTaskRunning(ctx, containerID)
		if running {
			if row.Status != "running" {
				m.markContainerStarted(ctx, botID)
			}
			if netErr := m.ensureContainerNetwork(ctx, containerID, botID); netErr != nil {
				m.logger.Error("reconcile: network setup failed for running task, container unreachable",
					slog.String("bot_id", botID),
					slog.String("container_id", containerID),
					slog.Any("error", netErr))
			} else {
				m.logger.Info("reconcile: container healthy",
					slog.String("bot_id", botID), slog.String("container_id", containerID))
				m.reconcileNativeWorkspace(ctx, botID)
			}
			continue
		}

		// Task not running — try to start it.
		m.logger.Warn("reconcile: task not running, starting",
			slog.String("bot_id", botID), slog.String("container_id", containerID))
		if err := m.EnsureNativeRunning(ctx, botID); err != nil {
			m.logger.Error("reconcile: failed to start task",
				slog.String("bot_id", botID), slog.Any("error", err))
			m.markContainerStopped(ctx, botID)
		} else {
			m.markContainerStarted(ctx, botID)
			m.reconcileNativeWorkspace(ctx, botID)
		}
	}
	m.logger.Info("reconcile: completed")
}

func (m *Manager) reconcileNativeWorkspace(ctx context.Context, botID string) {
	if err := m.WaitForWorkspaceReady(ctx, botID); err != nil {
		m.recordReconcileSetupFailure(ctx, botID, "reconcile_ready", err)
		return
	}
	if err := m.InitializeNativeWorkspace(ctx, botID); err != nil {
		m.recordReconcileSetupFailure(ctx, botID, "reconcile_initialize", err)
		return
	}
	if m.setupDiagnostics != nil {
		if err := m.setupDiagnostics.ClearContainerSetupFailure(ctx, botID); err != nil {
			m.logger.Warn("reconcile: clear workspace setup diagnostic failed",
				slog.String("bot_id", botID),
				slog.Any("error", err),
			)
		}
	}
}

func (m *Manager) recordReconcileSetupFailure(ctx context.Context, botID, phase string, setupErr error) {
	m.logger.Warn("reconcile: workspace initialization degraded",
		slog.String("bot_id", botID),
		slog.String("phase", phase),
		slog.Any("error", setupErr),
	)
	if m.setupDiagnostics == nil {
		return
	}
	if err := m.setupDiagnostics.RecordContainerSetupFailure(ctx, botID, phase, setupErr); err != nil {
		m.logger.Warn("reconcile: record workspace setup diagnostic failed",
			slog.String("bot_id", botID),
			slog.Any("error", err),
		)
	}
}

// RecordContainerRunning upserts a DB record marking the resolved container as running.
// This is exported for the HTTP handler's SSE-based creation flow, where the
// pull + start happen in the handler but the DB write belongs to Manager.
func (m *Manager) RecordContainerRunning(ctx context.Context, botID, containerID, image string) {
	m.upsertContainerRecord(ctx, botID, containerID, "running", image)
}

// ---------------------------------------------------------------------------
// DB record helpers (unexported)
// ---------------------------------------------------------------------------

func (m *Manager) upsertContainerRecord(ctx context.Context, botID, containerID, status, image string) {
	if m.queries == nil {
		return
	}
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return
	}
	ns := strings.TrimSpace(m.namespace)
	if ns == "" {
		ns = "default"
	}
	if dbErr := m.queries.UpsertContainer(ctx, dbsqlc.UpsertContainerParams{
		BotID:            pgBotID,
		ContainerID:      containerID,
		ContainerName:    containerID,
		Image:            image,
		Status:           status,
		Namespace:        ns,
		AutoStart:        true,
		ContainerPath:    config.DefaultDataMount,
		WorkspaceBackend: bridge.WorkspaceBackendContainer,
	}); dbErr != nil {
		m.logger.Error("failed to upsert container record",
			slog.String("bot_id", botID), slog.Any("error", dbErr))
	}
	if status == "running" {
		m.markContainerStarted(ctx, botID)
	}
}

func workspaceBackendFromRecord(recordValue string) string {
	switch strings.ToLower(strings.TrimSpace(recordValue)) {
	case bridge.WorkspaceBackendRemote:
		return bridge.WorkspaceBackendRemote
	case bridge.WorkspaceBackendContainer:
		return bridge.WorkspaceBackendContainer
	}
	return bridge.WorkspaceBackendContainer
}

func (m *Manager) deleteContainerRecord(ctx context.Context, botID string) {
	if m.queries == nil {
		return
	}
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return
	}
	if dbErr := m.queries.DeleteContainerByBotID(ctx, pgBotID); dbErr != nil {
		m.logger.Error("failed to delete container record",
			slog.String("bot_id", botID), slog.Any("error", dbErr))
	}
}

func (m *Manager) markContainerStarted(ctx context.Context, botID string) {
	if m.queries == nil {
		return
	}
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return
	}
	if dbErr := m.queries.UpdateContainerStarted(ctx, pgBotID); dbErr != nil {
		m.logger.Error("failed to update container started status",
			slog.String("bot_id", botID), slog.Any("error", dbErr))
	}
}

func (m *Manager) markContainerStopped(ctx context.Context, botID string) {
	if m.queries == nil {
		return
	}
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return
	}
	if dbErr := m.queries.UpdateContainerStopped(ctx, pgBotID); dbErr != nil {
		m.logger.Error("failed to update container stopped status",
			slog.String("bot_id", botID), slog.Any("error", dbErr))
	}
}

func (m *Manager) markContainerStatus(ctx context.Context, botID, status string) {
	if m.queries == nil {
		return
	}
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return
	}
	if dbErr := m.queries.UpdateContainerStatus(ctx, dbsqlc.UpdateContainerStatusParams{
		Status: status,
		BotID:  pgBotID,
	}); dbErr != nil {
		m.logger.Error("failed to update container status",
			slog.String("bot_id", botID), slog.Any("error", dbErr))
	}
}
