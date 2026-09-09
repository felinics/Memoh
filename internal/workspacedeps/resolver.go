package workspacedeps

import (
	"context"
	"log/slog"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

// Drivers discover installed launchers without authorizing script execution.
var (
	_ external.LauncherResolver = (*Service)(nil)
	_ external.VersionObserver  = (*Service)(nil)
)

// BuiltinLauncherCommands is the command binding of the built-in direct runtimes.
// It contains no recipes and remains available when the remote catalog is empty.
func BuiltinLauncherCommands() map[string]string {
	return map[string]string{"codex": "codex", "claude-code": "claude"}
}

// ResolveLauncher is read-only. Missing dependencies require an administrator
// to preview and confirm an install through the dependency management endpoint.
// A conversation, including a guest turn, never authorizes downloading scripts.
func (s *Service) ResolveLauncher(ctx context.Context, botID, depID string) (external.Launcher, error) {
	depID = strings.TrimSpace(depID)
	prepared, _, catalogErr := s.prepareCatalog(ctx, false, true)
	if catalogErr == nil {
		ctx = prepared
	}
	dep, err := s.dependency(ctx, depID)
	if catalogErr != nil || err != nil {
		command, builtin := BuiltinLauncherCommands()[depID]
		if !builtin {
			if catalogErr != nil {
				return external.Launcher{}, catalogErr
			}
			return external.Launcher{}, err
		}
		// The local launcher contract only describes executable discovery. It
		// cannot install anything and does not need a network/cache bootstrap.
		fallback, fallbackErr := catalog.Discovery(map[string]string{depID: command})
		if fallbackErr != nil {
			return external.Launcher{}, fallbackErr
		}
		ctx = context.WithValue(ctx, catalogContextKey{}, CatalogResult{Catalog: fallback})
		dep = fallback.MustGet(depID)
	}
	targetID, err := s.workspace.CurrentTargetID(ctx, botID)
	if err != nil {
		return external.Launcher{}, err
	}
	targetID = normalizeTargetID(targetID)
	snap, err := s.snapshot(ctx, botID, targetID, false)
	if err != nil {
		return external.Launcher{}, err
	}
	key := InstallationKey{BotID: botID, WorkspaceTargetID: targetID, DependencyID: dep.ID}
	candidate, ok := selectLauncherCandidate(snap.Observed[dep.ID].Candidates)
	if !ok {
		s.forgetLaunched(key)
		s.resolverMu.Lock()
		taskID := s.installs[key]
		s.resolverMu.Unlock()
		return external.Launcher{}, &external.DependencyMissingError{
			DependencyID: dep.ID, TaskID: taskID,
			OperationInProgress: taskID != "" || s.locks.locked(key) || snap.Observed[dep.ID].LockHeld,
		}
	}
	s.rememberLaunched(key, candidate.Path)
	return external.Launcher{Path: candidate.Path, Version: candidate.Version, Source: launcherSource(candidate.Source)}, nil
}

// ObserveLauncherVersion feeds the version a runtime reported during its
// handshake back into the discovery cache. The version is written to the copy
// ResolveLauncher last handed out for the bot's current target, falling back
// to the default winning copy when none was recorded. Errors are logged only;
// the correction is best effort.
func (s *Service) ObserveLauncherVersion(ctx context.Context, botID, depID, version string) {
	depID = strings.TrimSpace(depID)
	version = strings.TrimSpace(version)
	if depID == "" || version == "" {
		return
	}
	targetID, err := s.workspace.CurrentTargetID(ctx, botID)
	if err != nil {
		s.logger.Warn("observe launcher version: resolve workspace target",
			slog.String("bot_id", botID),
			slog.String("dependency_id", depID),
			slog.Any("error", err),
		)
		return
	}
	targetID = normalizeTargetID(targetID)
	key := InstallationKey{BotID: botID, WorkspaceTargetID: targetID, DependencyID: depID}
	s.resolverMu.Lock()
	path := s.launched[key]
	s.resolverMu.Unlock()
	if path != "" {
		s.cache.ObserveVersionAt(botID, targetID, depID, path, version)
		return
	}
	s.cache.ObserveVersion(botID, targetID, depID, version)
}

func (s *Service) rememberLaunched(key InstallationKey, path string) {
	s.resolverMu.Lock()
	s.launched[key] = path
	s.resolverMu.Unlock()
}

func (s *Service) forgetLaunched(key InstallationKey) {
	s.resolverMu.Lock()
	delete(s.launched, key)
	s.resolverMu.Unlock()
}

// selectLauncherCandidate applies the launcher preference order to discovered copies:
// the managed copy, then the toolkit copy, then a PATH copy. Within a source
// the discovery order is kept. This is the same precedence the shim
// directory gives the managed copy on PATH, so the launcher and a terminal
// agree on which copy runs.
func selectLauncherCandidate(candidates []Candidate) (Candidate, bool) {
	for _, source := range []Source{SourceManaged, SourceToolkit, SourcePath} {
		for _, candidate := range candidates {
			if candidate.Source == source && candidate.Path != "" {
				return candidate, true
			}
		}
	}
	return Candidate{}, false
}

func launcherSource(source Source) external.LauncherSource {
	switch source {
	case SourceManaged:
		return external.LauncherSourceManaged
	case SourceToolkit:
		return external.LauncherSourceToolkit
	default:
		return external.LauncherSourcePath
	}
}
