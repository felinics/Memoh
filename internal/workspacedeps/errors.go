package workspacedeps

import "errors"

// Sentinel errors returned by Service. Handlers map them to HTTP status codes
// and stable apperror codes; callers test them with errors.Is because most are
// wrapped with context.
var (
	// ErrDependencyNotFound means the dependency id is not in the catalog.
	ErrDependencyNotFound = errors.New("workspace dependency not found in catalog")
	// ErrPlatformUnsupported means the catalog manifest does not list the
	// probed platform of the target (WD-CAT-002).
	ErrPlatformUnsupported = errors.New("workspace dependency does not support the target platform")
	// ErrBusy means another operation on the same (bot, target, dependency)
	// is in progress, either in this process or, through the workspace lock,
	// in another Server instance (design §8.4). Operations never queue.
	ErrBusy = errors.New("workspace dependency operation already in progress")
	// ErrWorkspaceNotRunning means the native workspace exists but is stopped
	// and could not be started for the operation.
	ErrWorkspaceNotRunning = errors.New("workspace is not running")
	// ErrWorkspaceMissing means the native workspace container has not been
	// created; the caller must create it before installing anything
	// (WD-EXT-004).
	ErrWorkspaceMissing = errors.New("workspace has not been created")
	// ErrRemoteOffline means the remote workspace target cannot be reached.
	// Remote targets are never woken up by the Server (WD-PLAT-001).
	ErrRemoteOffline = errors.New("remote workspace is offline")
	// ErrRollbackUnavailable means state.json records no previous version or
	// its versions/<previous> directory is gone.
	ErrRollbackUnavailable = errors.New("no previous version available to roll back to")
	// ErrActionUnsupported means the dependency does not support the action:
	// image-provided dependencies have no scripts (WD-CAT-001), and a
	// manifest may leave optional actions unconfigured.
	ErrActionUnsupported = errors.New("action is not supported for this dependency")
)
