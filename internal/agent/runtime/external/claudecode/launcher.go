package claudecode

import (
	"context"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/agentproc"
)

// dependencyID is the workspace dependency catalog id that provisions the
// Claude Code CLI. No version is declared: the dependency
// manager installs what the user asks for, and turnRunner warns when the
// handshake reports a CLI that drifted from PinnedCLIVersion.
const dependencyID = "claude-code"

var _ external.DependencyRequirer = (*Driver)(nil)

// RequiredDependency implements external.DependencyRequirer.
func (*Driver) RequiredDependency() string { return dependencyID }

// SetLauncherResolver installs the resolver that picks which CLI copy a turn
// executes. Without one the driver runs the toolkit launcher unconditionally.
// Call it during assembly, before the driver serves turns.
func (d *Driver) SetLauncherResolver(resolver external.LauncherResolver) {
	d.launchers = resolver
}

// versionObserver returns the handshake callback that feeds the version the
// CLI reports back to the launcher resolver.
func (d *Driver) versionObserver(botID string) func(context.Context, string) {
	return func(ctx context.Context, version string) {
		agentproc.ObserveVersion(ctx, d.launchers, botID, dependencyID, version)
	}
}
