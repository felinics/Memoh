package workspacedeps

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

// Service is the launcher resolver behind the External Agent runtimes
// (design §9.2, §9.4). Drivers reach it only through these ports.
var (
	_ external.LauncherResolver = (*Service)(nil)
	_ external.VersionObserver  = (*Service)(nil)
)

// ResolveLauncher picks the copy of depID's primary command a driver should
// execute in the bot's current workspace target. Candidates are ranked by
// source (design §9.2): the managed copy, then the image toolkit copy, then
// any PATH copy; versions play no part. When no copy exists the install is
// handed to the background manager and the error is a
// *external.DependencyMissingError carrying that task id (WD-EXT-005). An id
// outside the catalog reports ErrDependencyNotFound.
func (s *Service) ResolveLauncher(ctx context.Context, botID, depID string) (external.Launcher, error) {
	dep, err := s.dependency(depID)
	if err != nil {
		return external.Launcher{}, err
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
		taskID, spawnErr := s.EnsureInstalledAsync(ctx, botID, targetID, dep.ID)
		if spawnErr != nil {
			s.logger.Warn("start background dependency install",
				slog.String("bot_id", botID),
				slog.String("workspace_target_id", targetID),
				slog.String("dependency_id", dep.ID),
				slog.Any("error", spawnErr),
			)
		}
		return external.Launcher{}, &external.DependencyMissingError{DependencyID: dep.ID, TaskID: taskID}
	}
	s.rememberLaunched(key, candidate.Path)
	return external.Launcher{
		Path:    candidate.Path,
		Version: candidate.Version,
		Source:  launcherSource(candidate.Source),
	}, nil
}

// EnsureInstalledAsync starts a background install of depID for (bot, target)
// and returns the task id. The install takes the manifest pin when there is
// one and otherwise the latest version. The same (bot, target, dependency)
// never gets two concurrent tasks: while one is running its id is returned
// again. When another operation already holds the dependency's lock (a
// UI-driven install, for instance) nothing is started and the id is empty;
// the same holds when the service has no background manager.
func (s *Service) EnsureInstalledAsync(ctx context.Context, botID, targetID, depID string) (string, error) {
	targetID = normalizeTargetID(targetID)
	dep, err := s.dependency(depID)
	if err != nil {
		return "", err
	}
	if !ActionSupported(dep, catalog.ActionInstall) {
		return "", fmt.Errorf("%w: %s has no install script", ErrActionUnsupported, dep.ID)
	}
	if s.background == nil {
		return "", nil
	}
	key := InstallationKey{BotID: botID, WorkspaceTargetID: targetID, DependencyID: dep.ID}

	// The map is held across SpawnManaged so the task's own cleanup, which
	// takes the same mutex, cannot run before the id has been recorded.
	s.resolverMu.Lock()
	defer s.resolverMu.Unlock()
	if taskID, running := s.installs[key]; running {
		return taskID, nil
	}
	if s.locks.locked(key) {
		return "", nil
	}
	description := "Install " + dep.Name
	if dep.Version.Pin != "" {
		description += " " + dep.Version.Pin
	}
	var taskID string
	taskID = s.background.SpawnManaged(ctx, botID, "", description, func(runCtx context.Context, log func(stream, chunk string)) error {
		defer func() {
			s.resolverMu.Lock()
			if s.installs[key] == taskID {
				delete(s.installs, key)
			}
			s.resolverMu.Unlock()
		}()
		sink := LogFunc(func(stream, line string) { log(stream, line+"\n") })
		if _, err := s.Install(runCtx, botID, targetID, dep.ID, "", sink); err != nil {
			return err
		}
		// Install already drops the snapshot on success; repeating it here
		// keeps the resolver correct should that ever change.
		s.cache.Invalidate(botID)
		return nil
	})
	s.installs[key] = taskID
	return taskID, nil
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

// selectLauncherCandidate applies the design §9.2 order to discovered copies:
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
