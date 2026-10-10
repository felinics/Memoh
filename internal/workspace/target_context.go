package workspace

import (
	"context"
	"errors"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

// WithWorkspaceTarget returns a child context whose workspace operations
// default to targetID. The override is request-scoped; it never mutates the
// Bot's persisted Primary target or Manager state.
func WithWorkspaceTarget(ctx context.Context, targetID string) context.Context {
	return bridge.WithWorkspaceTarget(ctx, targetID)
}

// WorkspaceTargetFromContext returns the request-scoped workspace target
// override, or an empty string when the Bot's persisted Primary should apply.
func WorkspaceTargetFromContext(ctx context.Context) string {
	return bridge.WorkspaceTargetFromContext(ctx)
}

// ErrCapabilityUnavailable means target identity is known, but its runtime or
// approval configuration cannot support workspace operations for this turn.
var ErrCapabilityUnavailable = errors.New("workspace capability unavailable")
