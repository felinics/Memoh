package workspacedeps

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// TargetNative is the workspace target id of the server-owned container.
const TargetNative = workspace.WorkspaceTargetNative

// WorkspaceState says whether a workspace target can run dependency scripts
// right now (design WD-EXT-004). List and Preflight report it instead of
// failing so the UI can offer "start workspace" or "create workspace".
type WorkspaceState string

// Workspace states.
const (
	// WorkspaceRunning means scripts can run now.
	WorkspaceRunning WorkspaceState = "running"
	// WorkspaceNotRunning means the native container exists but is stopped.
	// Mutating operations start it; read-only ones do not.
	WorkspaceNotRunning WorkspaceState = "not_running"
	// WorkspaceMissing means the native container has not been created.
	WorkspaceMissing WorkspaceState = "missing"
	// WorkspaceRemoteOffline means the remote target is not connected,
	// revoked, or otherwise unusable.
	WorkspaceRemoteOffline WorkspaceState = "remote_offline"
)

// WorkspaceAccess is the slice of workspace.Manager the service needs. It
// exists so the service can be tested without a container runtime and so the
// dependency on the manager stays explicit.
type WorkspaceAccess interface {
	// Client returns the bridge client for (bot, target); target is
	// TargetNative or a remote target id.
	Client(ctx context.Context, botID, targetID string) (*bridge.Client, error)
	// DataRoot returns the workspace data root for the target: the data mount
	// for native containers, the default working directory for remote
	// targets. Every managed dependency lives below it (design §6).
	DataRoot(ctx context.Context, botID, targetID string) (string, error)
	// State reports whether the target can run scripts now.
	State(ctx context.Context, botID, targetID string) (WorkspaceState, error)
	// EnsureRunning starts a stopped native workspace and waits for its
	// bridge. Remote targets are never started; an offline one returns
	// ErrRemoteOffline.
	EnsureRunning(ctx context.Context, botID, targetID string) error
	// OnBridgeReset registers a callback that runs whenever the bot's bridge
	// connection is evicted, i.e. after a container restart or rebuild.
	OnBridgeReset(fn func(botID string))
	// CurrentTargetID returns the workspace target a runtime launched for the
	// bot right now would use: the request-scoped override when the context
	// carries one, otherwise the bot's primary target. It never connects to a
	// runtime.
	CurrentTargetID(ctx context.Context, botID string) (string, error)
}

// normalizeTargetID maps the empty target to the native workspace.
func normalizeTargetID(targetID string) string {
	targetID = strings.TrimSpace(targetID)
	if targetID == "" {
		return TargetNative
	}
	return targetID
}

func isNativeTarget(targetID string) bool {
	return normalizeTargetID(targetID) == TargetNative
}

// managerWorkspaceAccess adapts *workspace.Manager to WorkspaceAccess.
type managerWorkspaceAccess struct {
	manager *workspace.Manager
}

// NewManagerWorkspaceAccess returns the production WorkspaceAccess backed by
// the workspace manager.
func NewManagerWorkspaceAccess(m *workspace.Manager) WorkspaceAccess {
	if m == nil {
		panic("workspacedeps: workspace manager is nil")
	}
	return &managerWorkspaceAccess{manager: m}
}

func (a *managerWorkspaceAccess) Client(ctx context.Context, botID, targetID string) (*bridge.Client, error) {
	if isNativeTarget(targetID) {
		return a.manager.NativeMCPClient(ctx, botID)
	}
	target, err := a.manager.ResolveWorkspaceTarget(ctx, botID, targetID)
	if err != nil {
		return nil, mapRemoteError(err)
	}
	return target.Client, nil
}

func (a *managerWorkspaceAccess) DataRoot(ctx context.Context, botID, targetID string) (string, error) {
	if isNativeTarget(targetID) {
		return config.DefaultDataMount, nil
	}
	target, err := a.manager.ResolveWorkspaceTarget(ctx, botID, targetID)
	if err != nil {
		return "", mapRemoteError(err)
	}
	root := strings.TrimSpace(target.Info.DefaultWorkDir)
	if root == "" {
		return "", fmt.Errorf("workspacedeps: remote workspace target %s reports no working directory", targetID)
	}
	return root, nil
}

// State inspects the native container record and live task, or resolves the
// remote mount. It never starts anything.
func (a *managerWorkspaceAccess) State(ctx context.Context, botID, targetID string) (WorkspaceState, error) {
	if isNativeTarget(targetID) {
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
	_, err := a.manager.ResolveWorkspaceTarget(ctx, botID, targetID)
	switch {
	case err == nil:
		return WorkspaceRunning, nil
	case isRemoteUnavailable(err):
		return WorkspaceRemoteOffline, nil
	default:
		return "", fmt.Errorf("workspacedeps: resolve workspace target %s: %w", targetID, err)
	}
}

func (a *managerWorkspaceAccess) EnsureRunning(ctx context.Context, botID, targetID string) error {
	if isNativeTarget(targetID) {
		if err := a.manager.EnsureNativeRunning(ctx, botID); err != nil {
			return err
		}
		return a.manager.WaitForWorkspaceReady(ctx, botID)
	}
	state, err := a.State(ctx, botID, targetID)
	if err != nil {
		return err
	}
	if state != WorkspaceRunning {
		return ErrRemoteOffline
	}
	return nil
}

func (a *managerWorkspaceAccess) OnBridgeReset(fn func(botID string)) {
	a.manager.OnBridgeReset(fn)
}

func (a *managerWorkspaceAccess) CurrentTargetID(ctx context.Context, botID string) (string, error) {
	targetID, err := a.manager.CurrentWorkspaceTargetID(ctx, botID)
	if err != nil {
		return "", fmt.Errorf("workspacedeps: current workspace target: %w", mapRemoteError(err))
	}
	return normalizeTargetID(targetID), nil
}

// isRemoteUnavailable groups every manager error that means "the remote
// target cannot run anything right now"; the service reports them all as
// ErrRemoteOffline / WorkspaceRemoteOffline.
func isRemoteUnavailable(err error) bool {
	return errors.Is(err, workspace.ErrRemoteRuntimeOffline) ||
		errors.Is(err, workspace.ErrRemoteRuntimeRevoked) ||
		errors.Is(err, workspace.ErrRemoteRuntimeOwnerMismatch) ||
		errors.Is(err, workspace.ErrRemoteRuntimeClientUpdateNeeded)
}

func mapRemoteError(err error) error {
	if isRemoteUnavailable(err) {
		return fmt.Errorf("%w: %w", ErrRemoteOffline, err)
	}
	return err
}
