package claudecode

import (
	"context"

	"github.com/felinics/memoh/internal/agent/runtime/external/agentproc"
)

// dependencyID is the workspace dependency catalog id that provisions the
// Claude Code CLI. No version is declared: the dependency
// manager installs what the user asks for, and turnRunner warns when the
// handshake reports a CLI that drifted from PinnedCLIVersion.
const dependencyID = "claude-code"

// versionObserver returns the handshake callback that feeds the version the
// CLI reports back to the launcher resolver.
func (d *Driver) versionObserver(botID string) func(context.Context, string) {
	return func(ctx context.Context, version string) {
		agentproc.ObserveVersion(ctx, d.launchers, botID, dependencyID, version)
	}
}
