package bridge

import (
	"context"
	"strings"
)

type workspaceTargetContextKey struct{}

func WithWorkspaceTarget(ctx context.Context, targetID string) context.Context {
	return context.WithValue(ctx, workspaceTargetContextKey{}, strings.TrimSpace(targetID))
}

func WorkspaceTargetFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	targetID, _ := ctx.Value(workspaceTargetContextKey{}).(string)
	return strings.TrimSpace(targetID)
}
