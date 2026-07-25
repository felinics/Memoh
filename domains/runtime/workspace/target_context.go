package workspace

import (
	"context"
	"strings"
)

type workspaceTargetContextKey struct{}

// WithWorkspaceTarget returns a child context whose workspace operations
// default to targetID. The override is request-scoped; it never mutates the
// Bot's persisted Primary target or Manager state.
func WithWorkspaceTarget(ctx context.Context, targetID string) context.Context {
	return context.WithValue(ctx, workspaceTargetContextKey{}, strings.TrimSpace(targetID))
}

// WorkspaceTargetFromContext returns the request-scoped workspace target
// override, or an empty string when the Bot's persisted Primary should apply.
func WorkspaceTargetFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	targetID, _ := ctx.Value(workspaceTargetContextKey{}).(string)
	return strings.TrimSpace(targetID)
}
