package claudecode

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	agentfeedback "github.com/felinics/memoh/internal/agent/decision/feedback"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/apperror"
)

// dependencyID is the workspace dependency catalog id that provisions the
// Claude Code CLI (design §9.1). The pinned version comes from
// PinnedCLIVersion so the declaration and the wire contract cannot drift.
const dependencyID = "claude-code"

var _ external.DependencyRequirer = (*Driver)(nil)

// RequiredDependency implements external.DependencyRequirer.
func (*Driver) RequiredDependency() (string, string) { return dependencyID, PinnedCLIVersion }

// SetLauncherResolver installs the resolver that picks which CLI copy a turn
// executes. Without one the driver runs the toolkit launcher unconditionally.
// Call it during assembly, before the driver serves turns.
func (d *Driver) SetLauncherResolver(resolver external.LauncherResolver) {
	d.launchers = resolver
}

// resolveLauncher picks the CLI executable for botID. A missing dependency
// becomes the stable agent_dependency_missing feedback (design §9.4), which
// callers must return unwrapped so it reaches the user; any other resolver
// failure is a runtime-unavailable error like a failed bridge lookup.
func (d *Driver) resolveLauncher(ctx context.Context, botID string) (external.Launcher, error) {
	if d.launchers == nil {
		return external.Launcher{Path: defaultLauncherPath, Source: external.LauncherSourceToolkit}, nil
	}
	launcher, err := d.launchers.ResolveLauncher(ctx, botID, dependencyID, PinnedCLIVersion)
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

// dependencyMissingFeedback is the user-facing shape of "no CLI copy exists":
// the turn does not start, and the args let the client render the background
// installation the resolver already queued.
func dependencyMissingFeedback(missing *external.DependencyMissingError) *agentfeedback.Error {
	message := "Claude Code is not installed in this workspace yet. Install it from the bot's dependencies and send the message again."
	if strings.TrimSpace(missing.TaskID) != "" {
		message = "Claude Code is not installed in this workspace yet; installation has started in the background. Send the message again when it finishes."
	}
	return agentfeedback.New(
		agentfeedback.CodeAgentDependencyMissing,
		"dependency_missing",
		http.StatusConflict,
		"chat.externalAgent.dependencyMissing",
		message,
		map[string]string{
			"dep_id":           firstNonEmpty(missing.DependencyID, dependencyID),
			"required_version": firstNonEmpty(missing.RequiredVersion, PinnedCLIVersion),
			"install_task_id":  strings.TrimSpace(missing.TaskID),
		},
	)
}

// noticeVersionMismatch tells a thread once that the CLI it runs is not the
// pinned version. The turn still launches (WD-EXT-001); the notice is keyed by
// the (required, installed) pair so a later pin change or a different
// installed copy is announced again, and an aligned launcher clears the slot.
func (d *Driver) noticeVersionMismatch(input external.PromptInput, launcher external.Launcher) {
	key := ""
	if launcher.Mismatch {
		key = PinnedCLIVersion + "\x00" + strings.TrimSpace(launcher.Version)
	}
	threadID := strings.TrimSpace(input.ThreadID)

	d.mismatchMu.Lock()
	if d.mismatchNoticed == nil {
		d.mismatchNoticed = map[string]string{}
	}
	previous, noticed := d.mismatchNoticed[threadID]
	if key == "" {
		delete(d.mismatchNoticed, threadID)
	} else {
		d.mismatchNoticed[threadID] = key
	}
	d.mismatchMu.Unlock()

	if key == "" || (noticed && previous == key) || input.Sink == nil {
		return
	}
	input.Sink.EmitStreamEvent(versionMismatchNotice(launcher))
}

// versionMismatchNotice is the one-time stream notice for a launcher whose
// version differs from the pin. Code is the stable feedback code (the UI shows
// it as the notice name); Metadata carries the same args the missing feedback
// uses so both runtimes and the client speak one vocabulary.
func versionMismatchNotice(launcher external.Launcher) event.StreamEvent {
	installed := strings.TrimSpace(launcher.Version)
	shown := installed
	if shown == "" {
		shown = "an unknown version"
	}
	return event.StreamEvent{
		Type: event.RuntimeNotice,
		Code: agentfeedback.CodeAgentDependencyVersionMismatch,
		Delta: fmt.Sprintf("Claude Code %s is installed in this workspace but this Memoh build expects %s. The session runs on the installed version; update Claude Code to %s to align them.",
			shown, PinnedCLIVersion, PinnedCLIVersion),
		Metadata: map[string]any{
			"dep_id":            dependencyID,
			"required_version":  PinnedCLIVersion,
			"installed_version": installed,
		},
	}
}

// versionObserver returns the handshake callback that feeds the CLI's
// self-reported version back into the resolver's cache, or nil when the
// resolver keeps no cache. The handshake value only corrects the cache; the
// launcher decision was already made from discovery (WD-EXT-001).
func (d *Driver) versionObserver(botID string) func(context.Context, string) {
	observer, ok := d.launchers.(external.VersionObserver)
	if !ok {
		return nil
	}
	return func(ctx context.Context, version string) {
		observer.ObserveLauncherVersion(ctx, botID, dependencyID, version)
	}
}
