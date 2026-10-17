package workspace

import (
	"context"
	"log/slog"
	"strings"

	"github.com/felinics/memoh/internal/hooks"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

func (m *Manager) runWorkspaceHook(ctx context.Context, botID, eventName string, extra map[string]any) error {
	if m == nil || m.hookService == nil {
		return nil
	}
	info := hooks.WorkspaceInfo{
		CWD:     hooks.DefaultWorkDir,
		Runtime: bridge.WorkspaceBackendContainer,
	}
	if workspaceInfo, err := m.WorkspaceInfo(ctx, botID); err == nil {
		if strings.TrimSpace(workspaceInfo.DefaultWorkDir) != "" {
			info.CWD = workspaceInfo.DefaultWorkDir
		}
		if strings.TrimSpace(workspaceInfo.Backend) != "" {
			info.Runtime = workspaceInfo.Backend
		}
	}
	req := hooks.Request{
		Version:   1,
		Event:     eventName,
		BotID:     strings.TrimSpace(botID),
		Workspace: info,
		Extra:     extra,
	}
	if _, err := m.hookService.Run(ctx, req, nil); err != nil {
		return err
	}
	return nil
}

// emitWorkspaceStartHook fires WorkspaceStart for a workspace whose task was
// started outside the provisioning path (restart recovery, container start,
// lazy ensure). startWithResolvedConfig emits its own hook with the image it
// just provisioned, so this one reports a restart of the existing container.
func (m *Manager) emitWorkspaceStartHook(ctx context.Context, botID, containerID string) {
	extra := map[string]any{
		"backend": bridge.WorkspaceBackendContainer,
	}
	if info, err := m.service.GetContainer(ctx, containerID); err == nil {
		if image := strings.TrimSpace(info.Image); image != "" {
			extra["image"] = image
		}
	}
	if err := m.runWorkspaceHook(context.WithoutCancel(ctx), botID, hooks.EventWorkspaceStart, extra); err != nil {
		m.logWorkspaceHookError(hooks.EventWorkspaceStart, botID, err)
	}
}

func (m *Manager) logWorkspaceHookError(eventName, botID string, err error) {
	if m == nil || m.logger == nil || err == nil {
		return
	}
	m.logger.Warn("workspace hook failed",
		slog.String("event", eventName),
		slog.String("bot_id", botID),
		slog.Any("error", err),
	)
}
