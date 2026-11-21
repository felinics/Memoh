package sidecar

import (
	"context"
	"fmt"
	"os"
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
	// DataDir holds the fallback resolver config used when the host has no
	// systemd-resolved config to copy into Spec.ResolvConfPath.
	DataDir string
}

func (m Manager) SidecarID(req netctl.AttachmentRequest) string {
	return fmt.Sprintf("%s-net-%s", req.ContainerID, m.Kind)
}

func (m Manager) EnsureAttached(ctx context.Context, req netctl.AttachmentRequest) (netctl.OverlayStatus, error) {
	if !req.Overlay.Enabled {
		return netctl.OverlayStatus{Provider: m.Kind, State: netctl.OverlayStateDisabled}, nil
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
		return netctl.OverlayStatus{Provider: m.Kind, State: netctl.OverlayStateDisabled}, nil
	}
	sidecarID := m.SidecarID(req)
	task, err := m.Runtime.GetTaskInfo(ctx, sidecarID)
	if err != nil {
		if ctr.IsNotFound(err) {
			return netctl.OverlayStatus{
				Provider: m.Kind,
				State:    netctl.OverlayStateMissing,
				Message:  "overlay sidecar is not created",
			}, nil
		}
		return netctl.OverlayStatus{}, err
	}
	attached := task.Status == ctr.TaskStatusRunning
	state := netctl.OverlayStateStopped
	if attached {
		state = netctl.OverlayStateReady
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
				Mounts: containerMounts(spec),
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
	if err := m.writeResolvConf(spec); err != nil {
		return err
	}
	return m.Runtime.StartContainer(ctx, sidecarID, nil)
}

func containerMounts(spec Spec) []ctr.MountSpec {
	if spec.ResolvConfPath == "" {
		return spec.Mounts
	}
	return append(append([]ctr.MountSpec(nil), spec.Mounts...), ctr.MountSpec{
		Destination: "/etc/resolv.conf",
		Type:        "bind",
		Source:      spec.ResolvConfPath,
		Options:     []string{"rbind", "rw"},
	})
}

// writeResolvConf refills the sidecar's resolv.conf from the host resolver
// config. It runs only while the task is down, so it never overwrites DNS
// settings the client applied to a running sidecar.
func (m Manager) writeResolvConf(spec Spec) error {
	if spec.ResolvConfPath == "" {
		return nil
	}
	source, err := ctr.ResolveConfSource(m.DataDir)
	if err != nil {
		return fmt.Errorf("resolve %s sidecar resolv.conf source: %w", m.Kind, err)
	}
	data, err := os.ReadFile(source) //nolint:gosec // source is the host resolver config chosen by ResolveConfSource.
	if err != nil {
		return fmt.Errorf("read %s sidecar resolv.conf source: %w", m.Kind, err)
	}
	if err := os.WriteFile(spec.ResolvConfPath, data, 0o644); err != nil { //nolint:gosec // resolv.conf must be world-readable inside the sidecar.
		return fmt.Errorf("write %s sidecar resolv.conf: %w", m.Kind, err)
	}
	return nil
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
