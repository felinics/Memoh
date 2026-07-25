package network

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	ctr "github.com/memohai/memoh/domains/runtime/container"
)

func withWorkspace(st BotStatus, ws WorkspaceRuntimeStatus) BotStatus {
	w := ws
	st.Workspace = &w
	return st
}

func (s *Service) workspaceRuntimeStatus(ctx context.Context, botID string) WorkspaceRuntimeStatus {
	if _, err := uuid.Parse(strings.TrimSpace(botID)); err != nil {
		return WorkspaceRuntimeStatus{State: "unknown", Message: "invalid bot id"}
	}
	container, err := s.workspaceReader.GetWorkspaceContainer(ctx, botID)
	if err != nil {
		if errors.Is(err, ErrWorkspaceContainerMissing) {
			return WorkspaceRuntimeStatus{State: "workspace_missing"}
		}
		s.logger.WarnContext(ctx, "workspace runtime status db lookup failed",
			slog.String("bot_id", botID), slog.Any("error", err))
		return WorkspaceRuntimeStatus{State: "unknown", Message: "workspace status is unavailable"}
	}
	out := WorkspaceRuntimeStatus{
		State:       "task_stopped",
		ContainerID: strings.TrimSpace(container.ContainerID),
	}
	if s.runtime == nil {
		out.State = "runtime_unavailable"
		return out
	}
	task, err := s.runtime.GetTaskInfo(ctx, container.ContainerID)
	if err != nil {
		if ctr.IsNotFound(err) {
			out.State = "task_stopped"
			return out
		}
		s.logger.WarnContext(ctx, "workspace runtime status query failed",
			slog.String("bot_id", botID),
			slog.String("container_id", container.ContainerID),
			slog.Any("error", err))
		return WorkspaceRuntimeStatus{State: "unknown", Message: "runtime status is unavailable"}
	}
	out.TaskStatus = task.Status.String()
	out.PID = task.PID
	if task.Status == ctr.TaskStatusRunning && strings.TrimSpace(task.NetworkJoinTarget.Value) == "" {
		out.State = "runtime_unavailable"
		if s.overlayRuntimeUnsupported() {
			out.Message = "current runtime backend does not expose a joinable network target"
		} else {
			out.Message = "workspace task is running but network target metadata is unavailable"
		}
		return out
	}
	if strings.TrimSpace(task.NetworkJoinTarget.Value) != "" {
		out.State = "network_target_ready"
		out.NetworkTargetKind = task.NetworkJoinTarget.Kind
		out.NetworkTarget = task.NetworkJoinTarget.Value
		if s.controller != nil {
			checkReq := AttachmentRequest{
				ContainerID: out.ContainerID,
				Runtime: RuntimeNetworkRequest{
					ContainerID: out.ContainerID,
					JoinTarget: NetworkJoinTarget{
						Kind: task.NetworkJoinTarget.Kind,
						Path: task.NetworkJoinTarget.Value,
						PID:  task.NetworkJoinTarget.PID,
					},
				},
			}
			if st, err := s.controller.Status(ctx, checkReq); err == nil {
				out.NetworkAttached = st.Runtime.Attached
			}
		}
	} else {
		out.State = "task_stopped"
	}
	return out
}

func (s *Service) buildAttachmentRequest(ctx context.Context, botID string, cfg BotOverlayConfig) (AttachmentRequest, error) {
	container, err := s.workspaceReader.GetWorkspaceContainer(ctx, botID)
	if err != nil {
		return AttachmentRequest{}, err
	}
	req := AttachmentRequest{
		BotID:       botID,
		ContainerID: container.ContainerID,
		Runtime: RuntimeNetworkRequest{
			ContainerID: container.ContainerID,
		},
		Overlay: cfg,
	}
	if s.runtime == nil {
		return req, nil
	}
	task, err := s.runtime.GetTaskInfo(ctx, container.ContainerID)
	if err == nil && strings.TrimSpace(task.NetworkJoinTarget.Value) != "" {
		req.Runtime.JoinTarget = NetworkJoinTarget{
			Kind: task.NetworkJoinTarget.Kind,
			Path: task.NetworkJoinTarget.Value,
			PID:  task.NetworkJoinTarget.PID,
		}
		return req, nil
	}
	if err != nil && !ctr.IsNotFound(err) {
		return AttachmentRequest{}, err
	}
	return req, nil
}
