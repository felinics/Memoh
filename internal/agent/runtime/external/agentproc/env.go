package agentproc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/workspace/vpath"
)

// ContainerPath is the PATH an agent CLI and every command it spawns see. The
// managed dependency shims come first, so a managed overlay of an image
// runtime (node, python, uv) wins over the toolkit copy.
const ContainerPath = vpath.DataMount + "/.memoh/deps/bin:/opt/memoh/toolkit/bin:/usr/local/bin:/usr/bin:/bin"

// BaseEnv is the environment every agent CLI starts from: the container PATH
// and the bot's data volume as HOME. Drivers append their own variables.
func BaseEnv(extra ...string) []string {
	return append([]string{"PATH=" + ContainerPath, "HOME=" + vpath.DataMount}, extra...)
}

// ShellQuote single-quotes value for a POSIX shell. Launcher paths live under
// per-bot data directories whose names Memoh does not control.
func ShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// ResolveLauncher finds the CLI copy a runtime runs for a bot, never
// installing one. Without a resolver it is the toolkit copy at fallbackPath.
// A missing dependency comes back as an *external.DependencyMissingError, which
// callers must pass through so the application can tell the user what to do;
// any other failure is the runtime being unavailable.
func ResolveLauncher(ctx context.Context, resolver external.LauncherResolver, botID, depID, fallbackPath string) (external.Launcher, error) {
	if resolver == nil {
		return external.Launcher{Path: fallbackPath, Source: external.LauncherSourceToolkit}, nil
	}
	launcher, err := resolver.ResolveLauncher(ctx, botID, depID)
	if err != nil {
		var missing *external.DependencyMissingError
		if errors.As(err, &missing) {
			taskID := strings.TrimSpace(missing.TaskID)
			id := strings.TrimSpace(missing.DependencyID)
			if id == "" {
				id = depID
			}
			return external.Launcher{}, &external.DependencyMissingError{
				DependencyID:        id,
				TaskID:              taskID,
				OperationInProgress: missing.OperationInProgress || taskID != "",
			}
		}
		return external.Launcher{}, external.Unavailable(fmt.Errorf("resolve %s launcher: %w", depID, err))
	}
	if strings.TrimSpace(launcher.Path) == "" {
		return external.Launcher{}, external.Unavailable(fmt.Errorf("resolve %s launcher: no path", depID))
	}
	return launcher, nil
}

// ObserveVersion feeds the version a CLI reported in its handshake back to a
// resolver that caches versions, so discovery needs no second probe.
func ObserveVersion(ctx context.Context, resolver external.LauncherResolver, botID, depID, version string) {
	observer, ok := resolver.(external.VersionObserver)
	if version = strings.TrimSpace(version); ok && version != "" {
		observer.ObserveLauncherVersion(ctx, botID, depID, version)
	}
}
