package workspacedeps

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/external"
)

// Service is the launcher resolver behind the External Agent runtimes
// (design §9.2, §9.4). Drivers reach it only through these ports.
var (
	_ external.LauncherResolver = (*Service)(nil)
	_ external.VersionObserver  = (*Service)(nil)
)

// ResolveLauncher picks the copy of depID's primary command a driver should
// execute in the bot's current workspace target. Candidates are ranked by
// design §9.2: managed at the required version, toolkit at the required
// version, any managed copy, any toolkit copy, any PATH copy. A copy whose
// version differs from the required one is still returned with Mismatch set
// (WD-EXT-001); an unknown version is not a mismatch. When no copy exists the
// install is handed to the background manager and the error is a
// *external.DependencyMissingError carrying that task id (WD-EXT-005).
//
// An empty requiredVersion falls back to the catalog pin. Dependencies that
// ship with the workspace image are not resolved here and report
// ErrDependencyNotFound, as does an id outside the catalog.
func (s *Service) ResolveLauncher(ctx context.Context, botID, depID, requiredVersion string) (external.Launcher, error) {
	dep, err := s.dependency(depID)
	if err != nil {
		return external.Launcher{}, err
	}
	if dep.IsImageProvided() {
		return external.Launcher{}, fmt.Errorf("%w: %s ships with the workspace image and has no launcher to resolve", ErrDependencyNotFound, dep.ID)
	}
	requiredVersion = strings.TrimSpace(requiredVersion)
	if requiredVersion == "" {
		requiredVersion = dep.Version.Pin
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

	candidate, ok := selectLauncherCandidate(snap.Observed[dep.ID].Candidates, requiredVersion)
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
		return external.Launcher{}, &external.DependencyMissingError{
			DependencyID:    dep.ID,
			RequiredVersion: requiredVersion,
			TaskID:          taskID,
		}
	}
	s.rememberLaunched(key, candidate.Path)
	return external.Launcher{
		Path:     candidate.Path,
		Version:  candidate.Version,
		Source:   launcherSource(candidate.Source),
		Mismatch: candidate.Version != "" && requiredVersion != "" && candidate.Version != requiredVersion,
	}, nil
}

// EnsureInstalledAsync starts a background install of depID for (bot, target)
// and returns the task id. The same (bot, target, dependency) never gets two
// concurrent tasks: while one is running its id is returned again. When
// another operation already holds the dependency's lock (a UI-driven install,
// for instance) nothing is started and the id is empty; the same holds when
// the service has no background manager.
func (s *Service) EnsureInstalledAsync(ctx context.Context, botID, targetID, depID string) (string, error) {
	targetID = normalizeTargetID(targetID)
	dep, err := s.dependency(depID)
	if err != nil {
		return "", err
	}
	if dep.IsImageProvided() {
		return "", fmt.Errorf("%w: %s ships with the workspace image", ErrActionUnsupported, dep.ID)
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
		if _, err := s.Install(runCtx, botID, targetID, dep.ID, sink); err != nil {
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
// managed at the required version, toolkit at the required version, any
// managed copy, any toolkit copy, then any PATH copy. Within a tier the
// discovery order is kept. An empty requiredVersion skips the version tiers.
func selectLauncherCandidate(candidates []Candidate, requiredVersion string) (Candidate, bool) {
	if len(candidates) == 0 {
		return Candidate{}, false
	}
	type tier struct {
		source  Source
		pinned  bool
		version string
	}
	tiers := []tier{
		{source: SourceManaged, pinned: true, version: requiredVersion},
		{source: SourceToolkit, pinned: true, version: requiredVersion},
		{source: SourceManaged},
		{source: SourceToolkit},
		{source: SourcePath},
	}
	for _, t := range tiers {
		if t.pinned && t.version == "" {
			continue
		}
		for _, candidate := range candidates {
			if candidate.Source != t.source || candidate.Path == "" {
				continue
			}
			if t.pinned && candidate.Version != t.version {
				continue
			}
			return candidate, true
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
