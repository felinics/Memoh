package workspacedeps

import (
	"context"
	"errors"
	"fmt"

	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// WorkspaceState describes the bot's isolated workspace without starting it.
type WorkspaceState string

const (
	WorkspaceRunning    WorkspaceState = "running"
	WorkspaceNotRunning WorkspaceState = "not_running"
	WorkspaceMissing    WorkspaceState = "missing"
)

// WorkspaceAccess only exposes the bot's isolated workspace. Device selection
// belongs to interactive workspace operations, never software management.
type WorkspaceAccess interface {
	Client(ctx context.Context, botID string) (*bridge.Client, error)
	DataRoot(ctx context.Context, botID string) (string, error)
	State(ctx context.Context, botID string) (WorkspaceState, error)
	EnsureRunning(ctx context.Context, botID string) error
	OnBridgeReset(fn func(botID string))
}

type managerWorkspaceAccess struct{ manager *workspace.Manager }

func NewManagerWorkspaceAccess(m *workspace.Manager) WorkspaceAccess {
	if m == nil {
		panic("workspacedeps: workspace manager is nil")
	}
	return &managerWorkspaceAccess{manager: m}
}

func (a *managerWorkspaceAccess) Client(ctx context.Context, botID string) (*bridge.Client, error) {
	return a.manager.NativeMCPClient(ctx, botID)
}

func (*managerWorkspaceAccess) DataRoot(_ context.Context, _ string) (string, error) {
	return config.DefaultDataMount, nil
}

func (a *managerWorkspaceAccess) State(ctx context.Context, botID string) (WorkspaceState, error) {
	info, err := a.manager.GetContainerInfo(ctx, botID)
	if errors.Is(err, workspace.ErrContainerNotFound) {
		return WorkspaceMissing, nil
	}
	if err != nil {
		return "", fmt.Errorf("workspacedeps: inspect native workspace: %w", err)
	}
	if info != nil && info.TaskRunning {
		return WorkspaceRunning, nil
	}
	return WorkspaceNotRunning, nil
}

func (a *managerWorkspaceAccess) EnsureRunning(ctx context.Context, botID string) error {
	if err := a.manager.EnsureNativeRunning(ctx, botID); err != nil {
		return err
	}
	return a.manager.WaitForWorkspaceReady(workspace.WithWorkspaceTarget(ctx, workspace.WorkspaceTargetNative), botID)
}

func (a *managerWorkspaceAccess) OnBridgeReset(fn func(botID string)) { a.manager.OnBridgeReset(fn) }

// ListBots enumerates existing native workspaces, including Bots whose last
// dependency was removed while an old process still held its payload.
func (a *managerWorkspaceAccess) ListBots(ctx context.Context) ([]string, error) {
	return a.manager.ListBots(ctx)
}
