package sidecar

import (
	"context"
	"fmt"
	"strings"
	"syscall"
	"time"

	ctr "github.com/felinics/memoh/internal/container"
	netctl "github.com/felinics/memoh/internal/network"
)

type BuildSpecFunc func(netctl.AttachmentRequest) (Spec, error)

type EnrichStatusFunc func(context.Context, netctl.AttachmentRequest, *netctl.OverlayStatus)

type Manager struct {
	Kind         string
	Runtime      Runtime
	BuildSpec    BuildSpecFunc
	EnrichStatus EnrichStatusFunc
}

func (m Manager) SidecarID(req netctl.AttachmentRequest) string {
	return fmt.Sprintf("%s-net-%s", req.ContainerID, m.Kind)
}

func (m Manager) EnsureAttached(ctx context.Context, req netctl.AttachmentRequest) (netctl.OverlayStatus, error) {
	if !req.Overlay.Enabled {
		return netctl.OverlayStatus{Provider: m.Kind, State: "disabled"}, nil
	}
	if m.Runtime == nil {
		return netctl.OverlayStatus{}, fmt.Errorf("%s overlay runtime is not configured", m.Kind)
	}
	if strings.TrimSpace(req.Runtime.JoinTarget.Path) == "" {
		return netctl.OverlayStatus{}, fmt.Errorf("%s overlay requires workspace network join target", m.Kind)
	}
	if m.BuildSpec == nil {
		return netctl.OverlayStatus{}, fmt.Errorf("%s sidecar spec builder is not configured", m.Kind)
	}
	spec, err := m.BuildSpec(req)
	if err != nil {
		return netctl.OverlayStatus{}, err
	}
	sidecarID := m.SidecarID(req)
	if err := m.ensureSidecarContainer(ctx, sidecarID, req, spec); err != nil {
		return netctl.OverlayStatus{}, err
	}
	return m.Status(ctx, req)
}

func (m Manager) Detach(ctx context.Context, req netctl.AttachmentRequest) error {
	if m.Runtime == nil {
		return nil
	}
	sidecarID := m.SidecarID(req)
	if err := m.Runtime.StopContainer(ctx, sidecarID, &ctr.StopTaskOptions{
		Signal:  syscall.SIGTERM,
		Timeout: 10 * time.Second,
		Force:   true,
	}); err != nil && !ctr.IsNotFound(err) {
		return err
	}
	if err := m.Runtime.DeleteTask(ctx, sidecarID, &ctr.DeleteTaskOptions{Force: true}); err != nil && !ctr.IsNotFound(err) {
		return err
	}
	if err := m.Runtime.DeleteContainer(ctx, sidecarID, &ctr.DeleteContainerOptions{CleanupSnapshot: true}); err != nil && !ctr.IsNotFound(err) {
		return err
	}
	return nil
}

func (m Manager) Status(ctx context.Context, req netctl.AttachmentRequest) (netctl.OverlayStatus, error) {
	if !req.Overlay.Enabled {
		return netctl.OverlayStatus{Provider: m.Kind, State: "disabled"}, nil
	}
	sidecarID := m.SidecarID(req)
	task, err := m.Runtime.GetTaskInfo(ctx, sidecarID)
	if err != nil {
		if ctr.IsNotFound(err) {
			return netctl.OverlayStatus{
				Provider: m.Kind,
				State:    "missing",
				Message:  "overlay sidecar is not created",
			}, nil
		}
		return netctl.OverlayStatus{}, err
	}
	attached := task.Status == ctr.TaskStatusRunning
	state := "stopped"
	if attached {
		state = "ready"
	}
	status := netctl.OverlayStatus{
		Provider: m.Kind,
		Attached: attached,
		State:    state,
		Details: map[string]any{
			"sidecar_id": sidecarID,
			"pid":        task.PID,
		},
	}
	if m.BuildSpec != nil {
		if spec, specErr := m.BuildSpec(req); specErr == nil {
			status.ProxyAddress = spec.ProxyAddress
			status.Details = MergeStatusDetails(status.Details, spec.Details)
		}
	}
	if m.EnrichStatus != nil && attached {
		m.EnrichStatus(ctx, req, &status)
	}
	return status, nil
}

func (m Manager) ensureSidecarContainer(ctx context.Context, sidecarID string, req netctl.AttachmentRequest, spec Spec) error {
	configHash := ConfigHash(spec, req.Runtime.JoinTarget.Path)
	needsCreate := false

	existing, err := m.Runtime.GetContainer(ctx, sidecarID)
	if err != nil {
		if !ctr.IsNotFound(err) {
			return err
		}
		needsCreate = true
	} else if existing.Labels[LabelConfigHash] != configHash {
		_ = m.Runtime.StopContainer(ctx, sidecarID, &ctr.StopTaskOptions{Signal: syscall.SIGTERM, Timeout: 5 * time.Second, Force: true})
		_ = m.Runtime.DeleteTask(ctx, sidecarID, &ctr.DeleteTaskOptions{Force: true})
		if err := m.Runtime.DeleteContainer(ctx, sidecarID, &ctr.DeleteContainerOptions{CleanupSnapshot: true}); err != nil && !ctr.IsNotFound(err) {
			return err
		}
		needsCreate = true
	}

	if needsCreate {
		if _, err := m.Runtime.CreateContainer(ctx, ctr.CreateContainerRequest{
			ID:       sidecarID,
			ImageRef: spec.Image,
			Labels: map[string]string{
				LabelManaged:    "true",
				LabelBotID:      req.BotID,
				LabelKind:       m.Kind,
				LabelConfigHash: configHash,
			},
			Spec: ctr.ContainerSpec{
				Cmd:    spec.Cmd,
				Env:    spec.Env,
				User:   "0",
				Mounts: spec.Mounts,
				NetworkJoinTarget: ctr.NetworkJoinTarget{
					Kind:  req.Runtime.JoinTarget.Kind,
					Value: req.Runtime.JoinTarget.Path,
					PID:   req.Runtime.JoinTarget.PID,
				},
				AddedCapabilities: spec.AddedCaps,
			},
		}); err != nil {
			return err
		}
	}

	task, err := m.Runtime.GetTaskInfo(ctx, sidecarID)
	if err == nil && task.Status == ctr.TaskStatusRunning {
		return nil
	}
	if err == nil {
		if err := m.Runtime.DeleteTask(ctx, sidecarID, &ctr.DeleteTaskOptions{Force: true}); err != nil && !ctr.IsNotFound(err) {
			return err
		}
	}
	return m.Runtime.StartContainer(ctx, sidecarID, nil)
}

func MergeStatusDetails(base map[string]any, extra map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range extra {
		out[key] = value
	}
	return out
}
