package claudecode

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/errs"
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

// resolveLauncher picks the CLI executable for botID. A missing dependency
// becomes external.DependencyMissingError, which callers must not wrap in
// an external.Failure so the application can translate it; any other
// resolver failure is a runtime-unavailable error like a failed bridge lookup.
func (d *Driver) resolveLauncher(ctx context.Context, botID string) (external.Launcher, error) {
	if d.launchers == nil {
		return external.Launcher{Path: defaultLauncherPath, Source: external.LauncherSourceToolkit}, nil
	}
	launcher, err := d.launchers.ResolveLauncher(ctx, botID, dependencyID)
	if err != nil {
		var missing *external.DependencyMissingError
		if errors.As(err, &missing) {
			return external.Launcher{}, dependencyMissing(missing)
		}
		return external.Launcher{}, external.Unavailable(fmt.Errorf("resolve claude launcher: %w", err))
	}
	if strings.TrimSpace(launcher.Path) == "" {
		return external.Launcher{}, external.Unavailable(errs.New("resolve claude launcher: resolver returned an empty path"))
	}
	return launcher, nil
}

// dependencyMissing blocks a turn until the dependency is available. It
// names this driver's dependency when the resolver did not, and counts a
// started installation task as an operation in progress.
func dependencyMissing(missing *external.DependencyMissingError) *external.DependencyMissingError {
	return &external.DependencyMissingError{
		DependencyID:        firstNonEmpty(missing.DependencyID, dependencyID),
		TaskID:              strings.TrimSpace(missing.TaskID),
		OperationInProgress: missing.OperationInProgress || strings.TrimSpace(missing.TaskID) != "",
	}
}

// versionObserver returns the handshake callback that feeds the CLI's
// self-reported version back into the resolver's cache, or nil when the
// resolver keeps no cache. The handshake value only corrects the cache; the
// launcher decision was already made from discovery.
func (d *Driver) versionObserver(botID string) func(context.Context, string) {
	observer, ok := d.launchers.(external.VersionObserver)
	if !ok {
		return nil
	}
	return func(ctx context.Context, version string) {
		observer.ObserveLauncherVersion(ctx, botID, dependencyID, version)
	}
}
