package workspacedeps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/felinics/memoh/internal/agent/background"
	"github.com/felinics/memoh/internal/textutil"
	"github.com/felinics/memoh/internal/workspace/bridge"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

// ActionRollback is the sixth action (design §4.3). It is never scripted by a
// manifest, so it is not a catalog.Action constant; the service performs it as
// a pure data operation and only reports it under this name.
const ActionRollback catalog.Action = "rollback"

// Preflight reasons a dependency requirement is not satisfied.
const (
	PreflightReasonMissing             = "missing"
	PreflightReasonVersionMismatch     = "version_mismatch"
	PreflightReasonPlatformUnsupported = "platform_unsupported"
	PreflightReasonUnknownDependency   = "unknown_dependency"
)

const (
	// defaultCacheTTL bounds how long a discovery snapshot is reused.
	defaultCacheTTL = 10 * time.Minute
	// lastErrorLimit caps the text stored in last_error.
	lastErrorLimit = 2048
	// rollbackTimeout bounds the symlink switch; it touches no network.
	rollbackTimeout = 2 * time.Minute
	// staleReapMessage is written to last_error by ReapStale.
	staleReapMessage = "operation did not finish within its timeout and was marked failed by the stale reaper"
	// rollbackScript is the whole body run by Rollback: switch `current` to
	// the previous version. state.json is rewritten by the Server afterwards.
	rollbackScript = `dep_switch "$MEMOH_DEP_HOME/versions/$MEMOH_DEP_VERSION"` + "\n"
)

// Options configures NewService. Workspace, Store, and Catalog are required.
type Options struct {
	Workspace WorkspaceAccess
	Store     Store
	Catalog   *catalog.Catalog
	// Cache defaults to NewCache(10 minutes).
	Cache  *Cache
	Logger *slog.Logger
	Now    func() time.Time
	// ScriptEnv returns extra environment entries for every script run, such
	// as NPM_MIRROR (design §5.4). It may be nil.
	ScriptEnv func(ctx context.Context) []string
	// Background receives the installs the launcher resolver starts for a
	// missing dependency (design §9.4). When nil the resolver reports the
	// dependency missing without starting anything and TaskID stays empty.
	Background *background.Manager
}

// Service reconciles the catalog, the installation records, and the
// workspace (design §3) and runs the six dependency actions.
type Service struct {
	workspace WorkspaceAccess
	store     Store
	catalog   *catalog.Catalog
	cache     *Cache
	logger    *slog.Logger
	now       func() time.Time
	scriptEnv func(ctx context.Context) []string

	// Package functions behind fields so tests can replace them.
	probe    func(ctx context.Context, client *bridge.Client) (Platform, error)
	discover func(ctx context.Context, client *bridge.Client, cat *catalog.Catalog, dataRoot string, depIDs []string, platform Platform) (map[string]Observed, error)
	run      func(ctx context.Context, client *bridge.Client, spec RunSpec, sink LogSink) (Result, error)

	locks operationLocks

	// background and the maps below belong to the launcher resolver
	// (resolver.go); resolverMu guards both maps.
	background *background.Manager
	resolverMu sync.Mutex
	// installs maps a (bot, target, dependency) to the background install the
	// resolver started for it, until that task finishes.
	installs map[InstallationKey]string
	// launched remembers the path ResolveLauncher last handed out per key so
	// a handshake-reported version is written to that copy, not the default
	// winner.
	launched map[InstallationKey]string
}

// NewService wires a Service and subscribes the cache to bridge resets so a
// restarted or rebuilt container is re-discovered (design §8.5). It panics
// when a required option is nil, which is a wiring error.
func NewService(opts Options) *Service {
	switch {
	case opts.Workspace == nil:
		panic("workspacedeps: Options.Workspace is nil")
	case opts.Store == nil:
		panic("workspacedeps: Options.Store is nil")
	case opts.Catalog == nil:
		panic("workspacedeps: Options.Catalog is nil")
	}
	s := &Service{
		workspace: opts.Workspace,
		store:     opts.Store,
		catalog:   opts.Catalog,
		cache:     opts.Cache,
		logger:    opts.Logger,
		now:       opts.Now,
		scriptEnv: opts.ScriptEnv,
		probe:     ProbePlatform,
		discover:  Discover,
		run:       Run,
		locks:     operationLocks{held: make(map[InstallationKey]struct{})},

		background: opts.Background,
		installs:   make(map[InstallationKey]string),
		launched:   make(map[InstallationKey]string),
	}
	if s.cache == nil {
		s.cache = NewCache(defaultCacheTTL)
	}
	if s.logger == nil {
		s.logger = slog.Default()
	}
	s.logger = s.logger.With(slog.String("component", "workspacedeps"))
	if s.now == nil {
		s.now = time.Now
	}
	s.workspace.OnBridgeReset(s.cache.Invalidate)
	return s
}

// Entry is one catalog dependency as seen for a (bot, target) pair after
// reconciliation (design §8.2).
type Entry struct {
	Dependency catalog.Dependency
	// Installation is the reconciled record, nil when the dependency is
	// neither recorded nor present.
	Installation *Installation
	Observed     Observed
	// Status is the reconciled status. The zero value means "not installed":
	// no record and nothing discovered.
	Status           Status
	InstalledVersion string
	// RequiredVersion is the catalog pin for agent dependencies and empty
	// otherwise (design §10.1).
	RequiredVersion string
	// LatestVersion is the pin for agent dependencies and the last upstream
	// check result for tool dependencies.
	LatestVersion string
	// NeedsAlignment is set for present agent dependencies whose version
	// differs from the pin.
	NeedsAlignment bool
	// UpdateAvailable is set for present tool dependencies whose last
	// upstream check reported a newer version.
	UpdateAvailable   bool
	PlatformSupported bool
	// Actions lists what the UI may offer right now.
	Actions []catalog.Action
}

// ListResult is the reconciled view of every catalog dependency for one
// (bot, target) pair.
type ListResult struct {
	// Platform is zero when the workspace is not running and was never
	// probed.
	Platform Platform
	// Workspace is the target's state; Entries carry no discovery data
	// unless it is WorkspaceRunning.
	Workspace WorkspaceState
	// DataRoot is the workspace data root every managed dependency lives
	// below (design §6). It is empty when the target cannot be resolved,
	// which only happens for offline remote targets.
	DataRoot string
	Entries  []Entry
}

// PreflightItem is the verdict for one required dependency (design §9.3).
type PreflightItem struct {
	DependencyID string
	// Name is the catalog display name, empty for an unknown dependency.
	Name      string
	Satisfied bool
	// Reason is empty when Satisfied, otherwise one of the PreflightReason
	// constants.
	Reason           string
	InstalledVersion string
	RequiredVersion  string
}

// PreflightResult reports whether a set of dependencies is ready to use.
// Items is empty when the workspace cannot be inspected; Workspace says why.
type PreflightResult struct {
	Workspace WorkspaceState
	Items     []PreflightItem
}

// OperationResult is the receipt of a completed action.
type OperationResult struct {
	DependencyID string
	Action       catalog.Action
	// Version and Entrypoints are empty after remove.
	Version      string
	Entrypoints  map[string]string
	Installation Installation
}

// List returns the reconciled dependency view, reusing the cached discovery
// snapshot when one is fresh. Reconciliation writes back to the store: it
// flips confirmed records between installed and missing, corrects observed
// facts, and adopts unrecorded copies (design §8.2).
func (s *Service) List(ctx context.Context, botID, targetID string) (ListResult, error) {
	return s.list(ctx, botID, targetID, false)
}

// Refresh is List with a forced re-discovery.
func (s *Service) Refresh(ctx context.Context, botID, targetID string) (ListResult, error) {
	return s.list(ctx, botID, targetID, true)
}

func (s *Service) list(ctx context.Context, botID, targetID string, force bool) (ListResult, error) {
	targetID = normalizeTargetID(targetID)
	state, err := s.workspace.State(ctx, botID, targetID)
	if err != nil {
		return ListResult{}, fmt.Errorf("workspacedeps: workspace state: %w", err)
	}
	records, err := s.store.ListForTarget(ctx, botID, targetID)
	if err != nil {
		return ListResult{}, fmt.Errorf("workspacedeps: list installations: %w", err)
	}
	byDep := indexRecords(records)
	result := ListResult{Workspace: state}
	// The data root is a constant for native targets and a resolved mount for
	// remote ones; an offline remote target simply has none to report.
	if dataRoot, err := s.workspace.DataRoot(ctx, botID, targetID); err == nil {
		result.DataRoot = dataRoot
	}
	deps := s.catalog.List()

	if state != WorkspaceRunning {
		// A stopped workspace keeps its last probed platform in the cache,
		// which is enough to grey out unsupported dependencies.
		if snap, ok := s.cache.Get(botID, targetID); ok {
			result.Platform = snap.Platform
		}
		for _, dep := range deps {
			result.Entries = append(result.Entries, offlineEntry(dep, byDep[dep.ID], result.Platform))
		}
		return result, nil
	}

	snap, err := s.snapshot(ctx, botID, targetID, force)
	if err != nil {
		return ListResult{}, err
	}
	result.Platform = snap.Platform
	for _, dep := range deps {
		key := InstallationKey{BotID: botID, WorkspaceTargetID: targetID, DependencyID: dep.ID}
		entry, err := s.reconcile(ctx, key, dep, snap, byDep[dep.ID])
		if err != nil {
			return ListResult{}, err
		}
		result.Entries = append(result.Entries, entry)
	}
	return result, nil
}

// snapshot returns the cached discovery for (bot, target) or performs one.
func (s *Service) snapshot(ctx context.Context, botID, targetID string, force bool) (Snapshot, error) {
	if !force {
		if snap, ok := s.cache.Get(botID, targetID); ok {
			return snap, nil
		}
	}
	client, dataRoot, err := s.target(ctx, botID, targetID)
	if err != nil {
		return Snapshot{}, err
	}
	platform, err := s.probe(ctx, client)
	if err != nil {
		return Snapshot{}, err
	}
	observed, err := s.discover(ctx, client, s.catalog, dataRoot, s.catalogIDs(), platform)
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{Platform: platform, Observed: observed}
	s.cache.Put(botID, targetID, snap)
	return snap, nil
}

// reconcile applies the three-state table of design §8.2 to one dependency
// and builds its Entry. Records in a transient state are displayed as they
// are and never written: an operation in flight must not be flipped by a
// concurrent List, and touching the row would postpone the stale reaper.
// Failed records keep their status so the failure stays visible until the
// next operation replaces it, but their observed facts are corrected.
func (s *Service) reconcile(ctx context.Context, key InstallationKey, dep catalog.Dependency, snap Snapshot, rec *Installation) (Entry, error) {
	obs := snap.Observed[dep.ID]
	entry := Entry{
		Dependency:        dep,
		Observed:          obs,
		PlatformSupported: dep.SupportsPlatform(snap.Platform.OS, snap.Platform.Arch, snap.Platform.Libc),
	}
	switch {
	case rec != nil && rec.Status.InProgress():
		// Read-only: the operation owns the row, and any write here would
		// refresh updated_at and postpone the stale reaper (WD-STATE-002).
	case rec == nil && obs.Present:
		adopted, err := s.store.Upsert(ctx, UpsertInstallation{
			InstallationKey:  key,
			Source:           installationSource(obs.Source),
			Status:           StatusInstalled,
			InstalledVersion: obs.Version,
			ManifestDigest:   observedDigest(obs),
		})
		if err != nil {
			return Entry{}, fmt.Errorf("workspacedeps: adopt %s: %w", dep.ID, err)
		}
		// Adoption records the intent columns; the fact columns that Upsert
		// does not carry (the agent pin as latest_version) follow.
		corrected, err := s.correctRecord(ctx, key, adopted, obs, dep)
		if err != nil {
			return Entry{}, err
		}
		rec = &corrected
	case rec != nil && obs.Present:
		corrected, err := s.correctRecord(ctx, key, *rec, obs, dep)
		if err != nil {
			return Entry{}, err
		}
		rec = &corrected
	case rec != nil && !obs.Present && rec.Status == StatusInstalled:
		missing, err := s.store.SetStatus(ctx, key, StatusMissing, "")
		if err != nil {
			return Entry{}, fmt.Errorf("workspacedeps: mark %s missing: %w", dep.ID, err)
		}
		rec = &missing
	}
	return buildEntry(entry, dep, rec, obs), nil
}

// correctRecord writes the discovered facts into a record that discovery
// confirmed (design §8.2, first row). Only changed columns are written.
func (s *Service) correctRecord(ctx context.Context, key InstallationKey, rec Installation, obs Observed, dep catalog.Dependency) (Installation, error) {
	if rec.Status == StatusMissing {
		restored, err := s.store.SetStatus(ctx, key, StatusInstalled, "")
		if err != nil {
			return Installation{}, fmt.Errorf("workspacedeps: mark %s installed: %w", dep.ID, err)
		}
		rec = restored
	}
	var upd ObservedUpdate
	changed := false
	if source := installationSource(obs.Source); rec.Source != source {
		upd.Source = &source
		changed = true
	}
	if obs.Version != "" && rec.InstalledVersion != obs.Version {
		version := obs.Version
		upd.InstalledVersion = &version
		changed = true
	}
	if dep.IsAgent() && rec.LatestVersion != dep.Version.Pin {
		pin := dep.Version.Pin
		upd.LatestVersion = &pin
		changed = true
	}
	if digest := observedDigest(obs); digest != "" && rec.ManifestDigest != digest {
		upd.ManifestDigest = &digest
		changed = true
	}
	if !changed {
		return rec, nil
	}
	corrected, err := s.store.UpdateObserved(ctx, key, upd)
	if err != nil {
		return Installation{}, fmt.Errorf("workspacedeps: correct %s: %w", dep.ID, err)
	}
	return corrected, nil
}

func buildEntry(entry Entry, dep catalog.Dependency, rec *Installation, obs Observed) Entry {
	entry.Installation = rec
	if rec != nil {
		entry.Status = rec.Status
		entry.InstalledVersion = rec.InstalledVersion
		entry.LatestVersion = rec.LatestVersion
	}
	if dep.IsAgent() {
		entry.RequiredVersion = dep.Version.Pin
		entry.LatestVersion = dep.Version.Pin
	}
	// The version shown is that of the copy the launcher resolver would run,
	// not necessarily the discovery winner: a managed 0.147.0 next to a
	// toolkit copy at the pin runs the toolkit copy, and the panel must not
	// ask for an alignment the runtime does not need (nor the other way
	// round). The record keeps the discovery winner's version, which is what
	// the Server installed.
	if obs.Present {
		if version := launcherVersion(obs, entry.RequiredVersion); version != "" {
			entry.InstalledVersion = version
		}
	}
	if dep.IsAgent() {
		entry.NeedsAlignment = obs.Present && entry.InstalledVersion != dep.Version.Pin
	} else if obs.Present && entry.LatestVersion != "" {
		entry.UpdateAvailable = entry.LatestVersion != entry.InstalledVersion
	}
	entry.Actions = availableActions(dep, rec, obs)
	return entry
}

// launcherVersion is the version of the copy the launcher resolver would
// execute for the dependency (selectLauncherCandidate, design §9.2), falling
// back to the discovery winner when no candidate qualifies. The panel
// (buildEntry) and Preflight both go through it so neither can disagree with
// the runtime about whether a version mismatch exists.
func launcherVersion(obs Observed, requiredVersion string) string {
	if candidate, ok := selectLauncherCandidate(obs.Candidates, requiredVersion); ok {
		return candidate.Version
	}
	return obs.Version
}

// offlineEntry builds an Entry from the record alone, for a workspace that
// cannot be inspected. Nothing can run, so no actions are offered.
func offlineEntry(dep catalog.Dependency, rec *Installation, platform Platform) Entry {
	entry := Entry{Dependency: dep, Installation: rec, PlatformSupported: true}
	if platform.OS != "" {
		entry.PlatformSupported = dep.SupportsPlatform(platform.OS, platform.Arch, platform.Libc)
	}
	if rec != nil {
		entry.Status = rec.Status
		entry.InstalledVersion = rec.InstalledVersion
		entry.LatestVersion = rec.LatestVersion
	}
	if dep.IsAgent() {
		entry.RequiredVersion = dep.Version.Pin
		entry.LatestVersion = dep.Version.Pin
	}
	return entry
}

// availableActions lists the actions the current state allows.
func availableActions(dep catalog.Dependency, rec *Installation, obs Observed) []catalog.Action {
	if dep.IsImageProvided() {
		return nil
	}
	if rec != nil && rec.Status.InProgress() {
		return nil
	}
	if !obs.Present {
		actions := []catalog.Action{catalog.ActionInstall}
		if rec != nil {
			// A missing or failed record can be dropped without a workspace
			// copy to delete; remove runs the script (a no-op then) and
			// deletes the intent.
			actions = append(actions, catalog.ActionRemove)
		}
		return actions
	}
	actions := []catalog.Action{catalog.ActionUpdate, catalog.ActionReinstall, catalog.ActionRemove}
	if obs.State != nil && strings.TrimSpace(obs.State.PreviousVersion) != "" {
		actions = append(actions, ActionRollback)
	}
	if !dep.IsAgent() && dep.Scripts.CheckUpdate != "" {
		actions = append(actions, catalog.ActionCheckUpdate)
	}
	return actions
}

// Preflight checks whether every dependency in depIDs is present at the
// required version (design §9.3). It never starts a workspace: when the
// target cannot be inspected Items is empty and Workspace says why
// (WD-EXT-004).
func (s *Service) Preflight(ctx context.Context, botID, targetID string, depIDs []string) (PreflightResult, error) {
	targetID = normalizeTargetID(targetID)
	state, err := s.workspace.State(ctx, botID, targetID)
	if err != nil {
		return PreflightResult{}, fmt.Errorf("workspacedeps: workspace state: %w", err)
	}
	result := PreflightResult{Workspace: state}
	if state != WorkspaceRunning {
		return result, nil
	}
	snap, err := s.snapshot(ctx, botID, targetID, false)
	if err != nil {
		return PreflightResult{}, err
	}
	for _, id := range depIDs {
		result.Items = append(result.Items, preflightItem(s.catalog, snap, id))
	}
	return result, nil
}

func preflightItem(cat *catalog.Catalog, snap Snapshot, depID string) PreflightItem {
	item := PreflightItem{DependencyID: depID}
	dep, ok := cat.Get(depID)
	if !ok {
		item.Reason = PreflightReasonUnknownDependency
		return item
	}
	item.Name = dep.Name
	obs := snap.Observed[depID]
	if dep.IsAgent() {
		item.RequiredVersion = dep.Version.Pin
	}
	// The verdict follows the copy the launcher resolver would run, the same
	// way the panel's entry does (launcherVersion), so the UI never reports a
	// mismatch the runtime would not see, or the other way round.
	version := launcherVersion(obs, item.RequiredVersion)
	switch {
	case !obs.Present && !dep.SupportsPlatform(snap.Platform.OS, snap.Platform.Arch, snap.Platform.Libc):
		item.Reason = PreflightReasonPlatformUnsupported
	case !obs.Present:
		item.Reason = PreflightReasonMissing
	case item.RequiredVersion != "" && version != item.RequiredVersion:
		item.InstalledVersion = version
		item.Reason = PreflightReasonVersionMismatch
	default:
		item.InstalledVersion = version
		item.Satisfied = true
	}
	return item
}

// Install runs the install script and records the result. A stopped native
// workspace is started first; a missing one or an offline remote target is
// refused (WD-EXT-004).
func (s *Service) Install(ctx context.Context, botID, targetID, depID string, sink LogSink) (OperationResult, error) {
	op, err := s.begin(ctx, botID, targetID, depID, true)
	if err != nil {
		return OperationResult{}, err
	}
	defer op.release()
	return s.provision(ctx, op, catalog.ActionInstall, StatusInstalling, sink)
}

// Update runs the update script, falling back to the install script when the
// manifest has none (design §4.3). For agent dependencies this aligns the
// copy with the Server pin.
func (s *Service) Update(ctx context.Context, botID, targetID, depID string, sink LogSink) (OperationResult, error) {
	op, err := s.begin(ctx, botID, targetID, depID, true)
	if err != nil {
		return OperationResult{}, err
	}
	defer op.release()
	return s.provision(ctx, op, catalog.ActionUpdate, StatusUpdating, sink)
}

// Reinstall runs the manifest's reinstall script or, when there is none,
// remove followed by install under a single lock (design §4.3). A failed
// remove stops the operation.
func (s *Service) Reinstall(ctx context.Context, botID, targetID, depID string, sink LogSink) (OperationResult, error) {
	op, err := s.begin(ctx, botID, targetID, depID, true)
	if err != nil {
		return OperationResult{}, err
	}
	defer op.release()
	if _, scripted := s.catalog.Script(op.dep.ID, catalog.ActionReinstall); scripted {
		return s.provision(ctx, op, catalog.ActionReinstall, StatusInstalling, sink)
	}
	removeScript, ok := s.catalog.Script(op.dep.ID, catalog.ActionRemove)
	if !ok {
		return OperationResult{}, fmt.Errorf("%w: %s has no remove script", ErrActionUnsupported, op.dep.ID)
	}
	installScript, ok := s.catalog.Script(op.dep.ID, catalog.ActionInstall)
	if !ok {
		return OperationResult{}, fmt.Errorf("%w: %s has no install script", ErrActionUnsupported, op.dep.ID)
	}
	previous := s.readStateBestEffort(ctx, op)
	if err := s.markInProgress(ctx, op.key, StatusInstalling); err != nil {
		return OperationResult{}, err
	}
	if _, err := s.runScript(ctx, op, catalog.ActionRemove, removeScript, "", stateVersion(previous), 0, sink); err != nil {
		return OperationResult{}, s.fail(ctx, op, err)
	}
	if err := op.deleteShims(ctx, shimNames(op.dep, previous)); err != nil {
		return OperationResult{}, s.fail(ctx, op, err)
	}
	result, err := s.runScript(ctx, op, catalog.ActionInstall, installScript, op.dep.Version.Pin, "", 0, sink)
	if err != nil {
		return OperationResult{}, s.fail(ctx, op, err)
	}
	return s.commit(ctx, op, catalog.ActionReinstall, result, nil)
}

// Remove runs the remove script, deletes the shims, and drops the record.
func (s *Service) Remove(ctx context.Context, botID, targetID, depID string, sink LogSink) (OperationResult, error) {
	op, err := s.begin(ctx, botID, targetID, depID, false)
	if err != nil {
		return OperationResult{}, err
	}
	defer op.release()
	script, ok := s.catalog.Script(op.dep.ID, catalog.ActionRemove)
	if !ok {
		return OperationResult{}, fmt.Errorf("%w: %s has no remove script", ErrActionUnsupported, op.dep.ID)
	}
	previous := s.readStateBestEffort(ctx, op)
	if err := s.markInProgress(ctx, op.key, StatusRemoving); err != nil {
		return OperationResult{}, err
	}
	if _, err := s.runScript(ctx, op, catalog.ActionRemove, script, "", stateVersion(previous), 0, sink); err != nil {
		return OperationResult{}, s.fail(ctx, op, err)
	}
	if err := op.deleteShims(ctx, shimNames(op.dep, previous)); err != nil {
		return OperationResult{}, s.fail(ctx, op, err)
	}
	if err := s.store.Delete(ctx, op.key); err != nil && !errors.Is(err, ErrInstallationNotFound) {
		return OperationResult{}, fmt.Errorf("workspacedeps: delete record for %s: %w", op.dep.ID, err)
	}
	s.cache.Invalidate(op.key.BotID)
	return OperationResult{DependencyID: op.dep.ID, Action: catalog.ActionRemove}, nil
}

// Rollback switches `current` back to the previous version recorded in
// state.json (design §4.3). It runs no catalog script and needs no network;
// the only workspace command is the symlink switch through the prelude.
func (s *Service) Rollback(ctx context.Context, botID, targetID, depID string) (OperationResult, error) {
	op, err := s.begin(ctx, botID, targetID, depID, false)
	if err != nil {
		return OperationResult{}, err
	}
	defer op.release()
	current, err := op.readState(ctx)
	if err != nil {
		return OperationResult{}, err
	}
	if current == nil || strings.TrimSpace(current.PreviousVersion) == "" {
		return OperationResult{}, ErrRollbackUnavailable
	}
	previous := strings.TrimSpace(current.PreviousVersion)
	if _, err := op.client.Stat(ctx, path.Join(VersionsDir(op.home), previous)); err != nil {
		if errors.Is(err, bridge.ErrNotFound) {
			return OperationResult{}, fmt.Errorf("%w: versions/%s is gone", ErrRollbackUnavailable, previous)
		}
		return OperationResult{}, fmt.Errorf("workspacedeps: stat previous version of %s: %w", op.dep.ID, err)
	}
	if err := s.markInProgress(ctx, op.key, StatusUpdating); err != nil {
		return OperationResult{}, err
	}
	if _, err := s.runScript(ctx, op, ActionRollback, rollbackScript, previous, current.Version, rollbackTimeout, nil); err != nil {
		return OperationResult{}, s.fail(ctx, op, err)
	}
	state := State{
		DependencyID:    op.dep.ID,
		Version:         previous,
		InstalledAt:     s.now().UTC(),
		ManifestDigest:  current.ManifestDigest,
		Entrypoints:     current.Entrypoints,
		PreviousVersion: current.Version,
	}
	if err := op.writeState(ctx, state); err != nil {
		return OperationResult{}, s.fail(ctx, op, err)
	}
	return s.record(ctx, op, ActionRollback, state)
}

// CheckUpdates is the manual refresh (design §10.3): it re-discovers the
// target, then runs check_update for every present tool dependency that
// follows a channel and writes the result to its record.
func (s *Service) CheckUpdates(ctx context.Context, botID, targetID string) (ListResult, error) {
	targetID = normalizeTargetID(targetID)
	result, err := s.list(ctx, botID, targetID, true)
	if err != nil || result.Workspace != WorkspaceRunning {
		return result, err
	}
	client, dataRoot, err := s.target(ctx, botID, targetID)
	if err != nil {
		return ListResult{}, err
	}
	checked := false
	for _, entry := range result.Entries {
		if !upstreamCheckable(entry.Dependency) || !entry.Observed.Present || entry.Installation == nil {
			continue
		}
		key := InstallationKey{BotID: botID, WorkspaceTargetID: targetID, DependencyID: entry.Dependency.ID}
		if !s.locks.tryLock(key) {
			continue
		}
		check, checkErr := s.checkUpdate(ctx, client, dataRoot, result.Platform, entry.Dependency, entry.InstalledVersion)
		s.locks.unlock(key)
		if err := s.recordCheck(ctx, key, check, checkErr); err != nil {
			return ListResult{}, err
		}
		checked = true
	}
	if !checked {
		return result, nil
	}
	return s.list(ctx, botID, targetID, false)
}

// ScriptPreview returns the exact stdin text the runner would feed to `sh -s`
// for the action, prelude included (WD-API-001). Reinstall without a script
// of its own previews both orchestrated steps.
func (s *Service) ScriptPreview(depID string, action catalog.Action) (string, error) {
	dep, err := s.dependency(depID)
	if err != nil {
		return "", err
	}
	if dep.IsImageProvided() {
		return "", fmt.Errorf("%w: %s ships with the workspace image", ErrActionUnsupported, dep.ID)
	}
	if action == ActionRollback {
		return WrapScript(rollbackScript), nil
	}
	if script, ok := s.catalog.Script(dep.ID, action); ok {
		return WrapScript(script), nil
	}
	switch action {
	case catalog.ActionUpdate:
		if script, ok := s.catalog.Script(dep.ID, catalog.ActionInstall); ok {
			return WrapScript(script), nil
		}
	case catalog.ActionReinstall:
		removeScript, okRemove := s.catalog.Script(dep.ID, catalog.ActionRemove)
		installScript, okInstall := s.catalog.Script(dep.ID, catalog.ActionInstall)
		if okRemove && okInstall {
			return "# ---- reinstall step 1/2: remove ----\n" + WrapScript(removeScript) +
				"\n# ---- reinstall step 2/2: install ----\n" + WrapScript(installScript), nil
		}
	}
	return "", fmt.Errorf("%w: %s has no %s script", ErrActionUnsupported, dep.ID, action)
}

// ReapStale marks in-progress records failed once they have outlived their
// script timeout plus the lock grace (WD-STATE-002). Records whose operation
// is still running in this process are skipped. It returns how many records
// were reaped.
func (s *Service) ReapStale(ctx context.Context) (int, error) {
	stale, err := s.store.ListStaleOperations(ctx, lockStaleGrace)
	if err != nil {
		return 0, fmt.Errorf("workspacedeps: list stale operations: %w", err)
	}
	now := s.now()
	reaped := 0
	var errs []error
	for _, rec := range stale {
		if !rec.Status.InProgress() || now.Sub(rec.UpdatedAt) < s.operationBudget(rec) {
			continue
		}
		key := InstallationKey{BotID: rec.BotID, WorkspaceTargetID: rec.WorkspaceTargetID, DependencyID: rec.DependencyID}
		if s.locks.locked(key) {
			continue
		}
		if _, err := s.store.SetStatus(ctx, key, StatusFailed, staleReapMessage); err != nil {
			errs = append(errs, fmt.Errorf("workspacedeps: reap %s for bot %s: %w", rec.DependencyID, rec.BotID, err))
			continue
		}
		s.logger.Warn("stale dependency operation marked failed",
			slog.String("bot_id", rec.BotID),
			slog.String("workspace_target_id", rec.WorkspaceTargetID),
			slog.String("dependency_id", rec.DependencyID),
			slog.String("status", string(rec.Status)),
		)
		reaped++
	}
	return reaped, errors.Join(errs...)
}

// operationBudget is how long an in-progress record may go without an update
// before it is stale: the script timeout of its action plus the same grace
// the prelude applies to workspace locks.
func (s *Service) operationBudget(rec Installation) time.Duration {
	dep, ok := s.catalog.Get(rec.DependencyID)
	if !ok {
		return time.Duration(catalog.DefaultInstallTimeout+catalog.DefaultRemoveTimeout)*time.Second + lockStaleGrace
	}
	var budget time.Duration
	switch rec.Status {
	case StatusInstalling:
		// Reinstall runs under installing and spends remove plus install.
		budget = dep.Timeouts.Duration(catalog.ActionReinstall)
	case StatusUpdating:
		budget = dep.Timeouts.Duration(catalog.ActionUpdate)
	case StatusRemoving:
		budget = dep.Timeouts.Duration(catalog.ActionRemove)
	default:
		budget = dep.Timeouts.Duration(catalog.ActionInstall)
	}
	return budget + lockStaleGrace
}

// operation is the prepared context of one mutating action.
type operation struct {
	key      InstallationKey
	dep      catalog.Dependency
	client   *bridge.Client
	dataRoot string
	home     string
	shimDir  string
	platform Platform
	release  func()
}

// begin validates the dependency, takes the in-memory lock, makes sure the
// workspace can run scripts, and resolves everything the action needs. The
// caller must release the returned operation.
func (s *Service) begin(ctx context.Context, botID, targetID, depID string, requirePlatform bool) (*operation, error) {
	targetID = normalizeTargetID(targetID)
	dep, err := s.dependency(depID)
	if err != nil {
		return nil, err
	}
	if dep.IsImageProvided() {
		return nil, fmt.Errorf("%w: %s ships with the workspace image", ErrActionUnsupported, dep.ID)
	}
	key := InstallationKey{BotID: botID, WorkspaceTargetID: targetID, DependencyID: dep.ID}
	if !s.locks.tryLock(key) {
		return nil, ErrBusy
	}
	op := &operation{key: key, dep: dep, release: func() { s.locks.unlock(key) }}
	if err := s.prepare(ctx, op, requirePlatform); err != nil {
		op.release()
		return nil, err
	}
	return op, nil
}

func (s *Service) prepare(ctx context.Context, op *operation, requirePlatform bool) error {
	botID, targetID := op.key.BotID, op.key.WorkspaceTargetID
	if err := s.ensureWorkspace(ctx, botID, targetID); err != nil {
		return err
	}
	client, dataRoot, err := s.target(ctx, botID, targetID)
	if err != nil {
		return err
	}
	platform, err := s.platformFor(ctx, botID, targetID, client)
	if err != nil {
		return err
	}
	if requirePlatform && !op.dep.SupportsPlatform(platform.OS, platform.Arch, platform.Libc) {
		return fmt.Errorf("%w: %s on %s/%s/%s", ErrPlatformUnsupported, op.dep.ID, platform.OS, platform.Arch, platform.Libc)
	}
	op.client = client
	op.dataRoot = dataRoot
	op.home = Home(dataRoot, op.dep.ID)
	op.shimDir = ShimDir(dataRoot)
	op.platform = platform
	return nil
}

// ensureWorkspace makes the target runnable for a user-requested operation:
// a stopped native container is started (WD-EXT-004), anything else that is
// not running is refused.
func (s *Service) ensureWorkspace(ctx context.Context, botID, targetID string) error {
	state, err := s.workspace.State(ctx, botID, targetID)
	if err != nil {
		return fmt.Errorf("workspacedeps: workspace state: %w", err)
	}
	switch state {
	case WorkspaceRunning:
		return nil
	case WorkspaceNotRunning:
		if err := s.workspace.EnsureRunning(ctx, botID, targetID); err != nil {
			return fmt.Errorf("%w: %w", ErrWorkspaceNotRunning, err)
		}
		return nil
	case WorkspaceMissing:
		return ErrWorkspaceMissing
	case WorkspaceRemoteOffline:
		return ErrRemoteOffline
	default:
		return fmt.Errorf("workspacedeps: unknown workspace state %q", state)
	}
}

// target resolves the bridge client and data root of (bot, target).
func (s *Service) target(ctx context.Context, botID, targetID string) (*bridge.Client, string, error) {
	client, err := s.workspace.Client(ctx, botID, targetID)
	if err != nil {
		return nil, "", fmt.Errorf("workspacedeps: workspace client: %w", err)
	}
	dataRoot, err := s.workspace.DataRoot(ctx, botID, targetID)
	if err != nil {
		return nil, "", fmt.Errorf("workspacedeps: workspace data root: %w", err)
	}
	return client, dataRoot, nil
}

// platformFor reuses the cached probe when there is one.
func (s *Service) platformFor(ctx context.Context, botID, targetID string, client *bridge.Client) (Platform, error) {
	if snap, ok := s.cache.Get(botID, targetID); ok {
		return snap.Platform, nil
	}
	return s.probe(ctx, client)
}

// provision is the shared body of install, update, and scripted reinstall.
func (s *Service) provision(ctx context.Context, op *operation, action catalog.Action, status Status, sink LogSink) (OperationResult, error) {
	script, ok := s.catalog.Script(op.dep.ID, action)
	if !ok && action == catalog.ActionUpdate {
		script, ok = s.catalog.Script(op.dep.ID, catalog.ActionInstall)
	}
	if !ok {
		return OperationResult{}, fmt.Errorf("%w: %s has no %s script", ErrActionUnsupported, op.dep.ID, action)
	}
	previous := s.readStateBestEffort(ctx, op)
	if err := s.markInProgress(ctx, op.key, status); err != nil {
		return OperationResult{}, err
	}
	result, err := s.runScript(ctx, op, action, script, op.dep.Version.Pin, stateVersion(previous), 0, sink)
	if err != nil {
		return OperationResult{}, s.fail(ctx, op, err)
	}
	return s.commit(ctx, op, action, result, previous)
}

// commit turns a successful install-like run into workspace state and a
// record: state.json, shims, then the installed row (design §6).
func (s *Service) commit(ctx context.Context, op *operation, action catalog.Action, result Result, previous *State) (OperationResult, error) {
	version := strings.TrimSpace(result.Version)
	if version == "" {
		version = op.dep.Version.Pin
	}
	if version == "" {
		return OperationResult{}, s.fail(ctx, op, errors.New("workspacedeps: script reported no version"))
	}
	if len(result.Entrypoints) == 0 {
		return OperationResult{}, s.fail(ctx, op, errors.New("workspacedeps: script reported no entrypoints"))
	}
	state := State{
		DependencyID:   op.dep.ID,
		Version:        version,
		InstalledAt:    s.now().UTC(),
		ManifestDigest: op.dep.ManifestDigest,
		Entrypoints:    result.Entrypoints,
	}
	if previous != nil {
		switch strings.TrimSpace(previous.Version) {
		case "", version:
			// Reinstalling the same version keeps the older fallback.
			state.PreviousVersion = previous.PreviousVersion
		default:
			state.PreviousVersion = previous.Version
		}
	}
	if err := op.writeState(ctx, state); err != nil {
		return OperationResult{}, s.fail(ctx, op, err)
	}
	if err := WriteShims(ctx, op.client, op.shimDir, state.Entrypoints, op.dep.IsAgent()); err != nil {
		return OperationResult{}, s.fail(ctx, op, err)
	}
	return s.record(ctx, op, action, state)
}

// record writes the installed row for a state.json the Server just wrote and
// invalidates the bot's discovery cache.
func (s *Service) record(ctx context.Context, op *operation, action catalog.Action, state State) (OperationResult, error) {
	rec, err := s.store.Upsert(ctx, UpsertInstallation{
		InstallationKey:  op.key,
		Source:           InstallationSourceManaged,
		Status:           StatusInstalled,
		InstalledVersion: state.Version,
		ManifestDigest:   state.ManifestDigest,
	})
	if err != nil {
		return OperationResult{}, fmt.Errorf("workspacedeps: record %s %s: %w", action, op.dep.ID, err)
	}
	s.cache.Invalidate(op.key.BotID)
	return OperationResult{
		DependencyID: op.dep.ID,
		Action:       action,
		Version:      state.Version,
		Entrypoints:  cloneStringMap(state.Entrypoints),
		Installation: rec,
	}, nil
}

// runScript executes one catalog script for the operation. ErrLocked from the
// prelude means another Server instance holds the workspace lock and is
// reported as ErrBusy. A zero timeout uses the catalog timeout for action.
func (s *Service) runScript(ctx context.Context, op *operation, action catalog.Action, script, version, currentVersion string, timeout time.Duration, sink LogSink) (Result, error) {
	if timeout <= 0 {
		timeout = op.dep.Timeouts.Duration(action)
	}
	spec := RunSpec{
		DepID:          op.dep.ID,
		Action:         action,
		Script:         script,
		Home:           op.home,
		ShimDir:        op.shimDir,
		Version:        version,
		CurrentVersion: currentVersion,
		Platform:       op.platform,
		Timeout:        timeout,
	}
	if s.scriptEnv != nil {
		spec.ExtraEnv = s.scriptEnv(ctx)
	}
	result, err := s.run(ctx, op.client, spec, sink)
	if errors.Is(err, ErrLocked) {
		return result, fmt.Errorf("%w: %w", ErrBusy, err)
	}
	return result, err
}

// markInProgress moves the record into a transient status, creating it when
// the dependency was never recorded.
func (s *Service) markInProgress(ctx context.Context, key InstallationKey, status Status) error {
	_, err := s.store.SetStatus(ctx, key, status, "")
	if errors.Is(err, ErrInstallationNotFound) {
		_, err = s.store.Upsert(ctx, UpsertInstallation{InstallationKey: key, Source: InstallationSourceManaged, Status: status})
	}
	if err != nil {
		return fmt.Errorf("workspacedeps: mark %s %s: %w", key.DependencyID, status, err)
	}
	return nil
}

// fail records a failed operation and returns the cause. A busy error leaves
// the record alone: the instance holding the workspace lock owns its state.
func (s *Service) fail(ctx context.Context, op *operation, cause error) error {
	if errors.Is(cause, ErrBusy) {
		return cause
	}
	// A cancelled request must still leave a failed record behind.
	storeCtx := context.WithoutCancel(ctx)
	if _, err := s.store.SetStatus(storeCtx, op.key, StatusFailed, truncateError(cause)); err != nil {
		s.logger.Warn("record failed dependency operation",
			slog.String("bot_id", op.key.BotID),
			slog.String("dependency_id", op.dep.ID),
			slog.Any("error", err),
		)
	}
	s.cache.Invalidate(op.key.BotID)
	return cause
}

// readState decodes the dependency's state.json; nil when there is none.
func (op *operation) readState(ctx context.Context) (*State, error) {
	reader, err := op.client.ReadRaw(ctx, StatePath(op.home))
	if err != nil {
		if errors.Is(err, bridge.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("workspacedeps: read state of %s: %w", op.dep.ID, err)
	}
	defer func() { _ = reader.Close() }()
	raw, err := io.ReadAll(reader)
	if err != nil {
		if errors.Is(err, bridge.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("workspacedeps: read state of %s: %w", op.dep.ID, err)
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("workspacedeps: decode state of %s: %w", op.dep.ID, err)
	}
	return &state, nil
}

// readStateBestEffort is readState for callers that only want the previous
// version if it is readable; problems are logged, not returned.
func (s *Service) readStateBestEffort(ctx context.Context, op *operation) *State {
	state, err := op.readState(ctx)
	if err != nil {
		s.logger.Warn("ignoring unreadable dependency state",
			slog.String("bot_id", op.key.BotID),
			slog.String("dependency_id", op.dep.ID),
			slog.Any("error", err),
		)
		return nil
	}
	return state
}

// writeState writes state.json as a single line. Discovery locates the
// primary entrypoint with sed and depends on that shape.
func (op *operation) writeState(ctx context.Context, state State) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("workspacedeps: encode state of %s: %w", op.dep.ID, err)
	}
	if err := op.client.Mkdir(ctx, op.home); err != nil {
		return fmt.Errorf("workspacedeps: create %s: %w", op.home, err)
	}
	if err := op.client.WriteFile(ctx, StatePath(op.home), data); err != nil {
		return fmt.Errorf("workspacedeps: write state of %s: %w", op.dep.ID, err)
	}
	return nil
}

// deleteShims removes the named shims; ones that do not exist are fine.
func (op *operation) deleteShims(ctx context.Context, names []string) error {
	for _, name := range names {
		if !isPlainFileName(name) {
			continue
		}
		target := path.Join(op.shimDir, name)
		if err := op.client.DeleteFile(ctx, target, false); err != nil && !errors.Is(err, bridge.ErrNotFound) {
			return fmt.Errorf("workspacedeps: delete shim %s: %w", target, err)
		}
	}
	return nil
}

// updateCheck is the check_update result payload (design §5.4).
type updateCheck struct {
	Installed       string `json:"installed"`
	Latest          string `json:"latest"`
	UpdateAvailable bool   `json:"update_available"`
}

// upstreamCheckable reports whether a dependency takes part in upstream
// update checks (WD-UPD-001): a managed, unpinned tool with a check_update
// script.
func upstreamCheckable(dep catalog.Dependency) bool {
	return !dep.IsAgent() && !dep.IsImageProvided() && dep.Version.Pin == "" && dep.Scripts.CheckUpdate != ""
}

// checkUpdate runs the check_update script and decodes its result. The exit
// status only says whether the check ran (WD-EXEC-003).
func (s *Service) checkUpdate(ctx context.Context, client *bridge.Client, dataRoot string, platform Platform, dep catalog.Dependency, currentVersion string) (updateCheck, error) {
	script, ok := s.catalog.Script(dep.ID, catalog.ActionCheckUpdate)
	if !ok {
		return updateCheck{}, fmt.Errorf("%w: %s has no check_update script", ErrActionUnsupported, dep.ID)
	}
	spec := RunSpec{
		DepID:          dep.ID,
		Action:         catalog.ActionCheckUpdate,
		Script:         script,
		Home:           Home(dataRoot, dep.ID),
		ShimDir:        ShimDir(dataRoot),
		CurrentVersion: currentVersion,
		Platform:       platform,
		Timeout:        dep.Timeouts.Duration(catalog.ActionCheckUpdate),
	}
	if s.scriptEnv != nil {
		spec.ExtraEnv = s.scriptEnv(ctx)
	}
	result, err := s.run(ctx, client, spec, nil)
	if err != nil {
		if errors.Is(err, ErrLocked) {
			return updateCheck{}, fmt.Errorf("%w: %w", ErrBusy, err)
		}
		return updateCheck{}, err
	}
	if len(result.Raw) == 0 {
		return updateCheck{}, fmt.Errorf("workspacedeps: check_update for %s wrote no result", dep.ID)
	}
	var check updateCheck
	if err := json.Unmarshal(result.Raw, &check); err != nil {
		return updateCheck{}, fmt.Errorf("workspacedeps: decode check_update result for %s: %w", dep.ID, err)
	}
	check.Latest = strings.TrimSpace(check.Latest)
	if check.Latest == "" {
		return updateCheck{}, fmt.Errorf("workspacedeps: check_update for %s reported no latest version", dep.ID)
	}
	return check, nil
}

// recordCheck writes a check result to one record. Failures only touch
// last_error and last_checked_at; the status stays as it was (WD-UPD-004).
func (s *Service) recordCheck(ctx context.Context, key InstallationKey, check updateCheck, checkErr error) error {
	now := s.now().UTC()
	upd := ObservedUpdate{LastCheckedAt: &now}
	if checkErr != nil {
		msg := truncateError(checkErr)
		upd.LastError = &msg
	} else {
		latest := check.Latest
		cleared := ""
		upd.LatestVersion = &latest
		upd.LastError = &cleared
	}
	if _, err := s.store.UpdateObserved(ctx, key, upd); err != nil {
		if errors.Is(err, ErrInstallationNotFound) {
			return nil
		}
		return fmt.Errorf("workspacedeps: record update check for %s: %w", key.DependencyID, err)
	}
	return nil
}

// Dependency returns the catalog entry with the given id. Handlers use it to
// validate a request and read the pinned version before an operation starts.
func (s *Service) Dependency(depID string) (catalog.Dependency, bool) {
	return s.catalog.Get(strings.TrimSpace(depID))
}

func (s *Service) dependency(depID string) (catalog.Dependency, error) {
	dep, ok := s.catalog.Get(strings.TrimSpace(depID))
	if !ok {
		return catalog.Dependency{}, fmt.Errorf("%w: %q", ErrDependencyNotFound, depID)
	}
	return dep, nil
}

func (s *Service) catalogIDs() []string {
	deps := s.catalog.List()
	ids := make([]string, 0, len(deps))
	for _, dep := range deps {
		ids = append(ids, dep.ID)
	}
	return ids
}

// operationLocks is the in-memory mutual exclusion per (bot, target,
// dependency) (design §8.4). Callers never wait: a held key means busy.
type operationLocks struct {
	mu   sync.Mutex
	held map[InstallationKey]struct{}
}

func (l *operationLocks) tryLock(key InstallationKey) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, held := l.held[key]; held {
		return false
	}
	l.held[key] = struct{}{}
	return true
}

func (l *operationLocks) unlock(key InstallationKey) {
	l.mu.Lock()
	delete(l.held, key)
	l.mu.Unlock()
}

func (l *operationLocks) locked(key InstallationKey) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, held := l.held[key]
	return held
}

func indexRecords(records []Installation) map[string]*Installation {
	byDep := make(map[string]*Installation, len(records))
	for i := range records {
		byDep[records[i].DependencyID] = &records[i]
	}
	return byDep
}

// installationSource maps a discovery source to the record's source column
// (design §8.2): toolkit copies come from the image, everything else counts
// as managed.
func installationSource(source Source) string {
	if source == SourceToolkit {
		return InstallationSourceImage
	}
	return InstallationSourceManaged
}

func observedDigest(obs Observed) string {
	if obs.Source != SourceManaged || obs.State == nil {
		return ""
	}
	return strings.TrimSpace(obs.State.ManifestDigest)
}

func stateVersion(state *State) string {
	if state == nil {
		return ""
	}
	return strings.TrimSpace(state.Version)
}

// shimNames lists every shim a dependency may have written: the entrypoints
// recorded in state.json plus the manifest's provides, deduplicated.
func shimNames(dep catalog.Dependency, state *State) []string {
	seen := make(map[string]bool, len(dep.Provides))
	names := make([]string, 0, len(dep.Provides))
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		names = append(names, name)
	}
	for _, name := range dep.Provides {
		add(name)
	}
	if state != nil {
		for name := range state.Entrypoints {
			add(name)
		}
	}
	sort.Strings(names)
	return names
}

func truncateError(err error) string {
	return textutil.TruncateRunesWithSuffix(strings.TrimSpace(err.Error()), lastErrorLimit, "...")
}
