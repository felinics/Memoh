package external

import "context"

// DependencyReadiness restores only previously authorized installations. It is
// called during turn execution, separately from read-only launcher discovery,
// model listing and login status checks.
type DependencyReadiness interface {
	EnsureDependenciesReady(ctx context.Context, botID string, depIDs []string) error
}

// PrepareDependency preserves read-only resolvers while allowing a configured
// recovery coordinator to prepare an already-authorized target for a turn.
func PrepareDependency(ctx context.Context, resolver LauncherResolver, botID, depID string) error {
	if coordinator, ok := resolver.(DependencyReadiness); ok {
		return coordinator.EnsureDependenciesReady(ctx, botID, []string{depID})
	}
	return nil
}
