package claudecode

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	agentfeedback "github.com/felinics/memoh/internal/agent/decision/feedback"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/apperror"
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
// becomes the stable agent_dependency_missing feedback, which
// callers must return unwrapped so it reaches the user; any other resolver
// failure is a runtime-unavailable error like a failed bridge lookup.
func (d *Driver) resolveLauncher(ctx context.Context, botID string) (external.Launcher, error) {
	if d.launchers == nil {
		return external.Launcher{Path: defaultLauncherPath, Source: external.LauncherSourceToolkit}, nil
	}
	launcher, err := d.launchers.ResolveLauncher(ctx, botID, dependencyID)
	if err != nil {
		var missing *external.DependencyMissingError
		if errors.As(err, &missing) {
			return external.Launcher{}, dependencyMissingFeedback(missing)
		}
		return external.Launcher{}, apperror.Wrap(apperror.CodeExternalRuntimeUnavailable,
			fmt.Errorf("resolve claude launcher: %w", err), map[string]string{"runtime": RuntimeType})
	}
	if strings.TrimSpace(launcher.Path) == "" {
		return external.Launcher{}, apperror.Wrap(apperror.CodeExternalRuntimeUnavailable,
			errors.New("resolve claude launcher: resolver returned an empty path"), map[string]string{"runtime": RuntimeType})
	}
	return launcher, nil
}

// dependencyMissingFeedback blocks a turn until the dependency is available.
func dependencyMissingFeedback(missing *external.DependencyMissingError) *agentfeedback.Error {
	message := "Claude Code is not installed in this workspace. Ask a bot administrator to install it from the bot's dependencies, then send the message again."
	operationInProgress := missing.OperationInProgress || strings.TrimSpace(missing.TaskID) != ""
	if operationInProgress {
		message = "Claude Code is not installed in this workspace yet; a dependency operation is already in progress. Send the message again when it finishes."
	}
	return agentfeedback.New(
		agentfeedback.CodeAgentDependencyMissing,
		"dependency_missing",
		http.StatusConflict,
		"chat.externalAgent.dependencyMissing",
		message,
		map[string]string{
			"dep_id":                firstNonEmpty(missing.DependencyID, dependencyID),
			"install_task_id":       strings.TrimSpace(missing.TaskID),
			"operation_in_progress": strconv.FormatBool(operationInProgress),
		},
	)
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
