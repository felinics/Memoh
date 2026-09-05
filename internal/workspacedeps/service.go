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
	// interruptedMessage is written to last_error when List finds an
	// in-progress record whose operation is provably gone (WD-STATE-002).
	interruptedMessage = "operation interrupted"
	// cancelledMessagePrefix marks a last_error caused by the request going
	// away (closed dialog, dropped connection, shutdown) rather than by the
	// script.
	cancelledMessagePrefix = "operation cancelled: "
	// finalizeTimeout bounds the writes that record an operation's outcome
	// once its script has finished or failed. They run on a context detached
	// from the request so a closed dialog, a dropped connection, or the
	// shutdown window still leaves a terminal record behind instead of an
	// installing/updating/removing row nobody owns.
	finalizeTimeout = 10 * time.Second
	// staleOperationAge is how long an in-progress record must have gone
	// untouched before List may treat it as interrupted. An operation that
	// is running has taken its workspace lock well within that time.
	staleOperationAge = 60 * time.Second
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
	// Snapshot ages are compared with record timestamps, so the cache and
	// the service must read the same clock.
	s.cache.now = s.now
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
	Status Status
	// InstalledVersion is the version of the copy in effect: the one the
	// launcher resolver runs and the one the shim directory puts first on
	// PATH (managed, then toolkit, then PATH).
	InstalledVersion string
	// ImageVersion is the version of the copy the workspace image ships in
	// its toolkit, empty when the image has none. It is the baseline a
	// managed overlay sits on and what remove returns to.
	ImageVersion string
	// Overlay is set when the copy in effect is a managed one installed over
	// an image copy: removing it uncovers the ImageVersion copy.
	Overlay bool
	// LatestVersion is the last check_update result recorded for the
	// dependency, empty until a check ran.
	LatestVersion string
	// UpdateAvailable is set for present dependencies whose last upstream
	// check reported a version other than the one in effect.
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
	// DiscoveryError is set when the workspace is running but could not be
	// inspected (the discovery exec was killed or timed out, the bridge did
	// not answer). Entries then reflect the records alone, carry no
	// discovery facts, and offer no actions.
	DiscoveryError string
}

// PreflightItem is the verdict for one required dependency (design §9.3).
type PreflightItem struct {
	DependencyID string
	// Name is the catalog display name, empty for an unknown dependency.
	Name      string
	Satisfied bool
	// Reason is empty when Satisfied, otherwise one of the PreflightReason
	// constants.
	Reason string
	// InstalledVersion is the version of the copy in effect, when present.
	InstalledVersion string
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
		if ctx.Err() != nil {
			return ListResult{}, err
		}
		// The workspace runs but could not be inspected: the discovery exec
		// was killed or timed out, or the bridge did not answer. The records
		// still say what the user asked for, so report them with the problem
		// instead of failing the whole list. A stopped workspace never gets
		// here; it keeps its own semantics above.
		s.logger.Warn("workspace dependency discovery failed; listing records only",
			slog.String("bot_id", botID),
			slog.String("workspace_target_id", targetID),
			slog.Any("error", err),
		)
		result.DiscoveryError = truncateMessage(err.Error())
		if snap, ok := s.cache.Get(botID, targetID); ok {
			result.Platform = snap.Platform
		}
		for _, dep := range deps {
			result.Entries = append(result.Entries, offlineEntry(dep, byDep[dep.ID], result.Platform))
		}
		return result, nil
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
	snap := Snapshot{Platform: platform, Observed: observed, At: s.now()}
	s.cache.Put(botID, targetID, snap)
	return snap, nil
}

// reconcile applies the three-state table of design §8.2 to one dependency
// and builds its Entry. Records in a transient state are displayed as they
// are and never written: an operation in flight must not be flipped by a
// concurrent List, and touching the row would postpone the stale reaper. The
// one exception is a record whose operation is provably gone (interrupted);
// it is marked failed on the spot so the UI offers a retry instead of waiting
// for the reaper. Failed records keep their status so the failure stays
// visible until the next operation replaces it, but their observed facts are
// corrected.
func (s *Service) reconcile(ctx context.Context, key InstallationKey, dep catalog.Dependency, snap Snapshot, rec *Installation) (Entry, error) {
	obs := snap.Observed[dep.ID]
	entry := Entry{
		Dependency:        dep,
		Observed:          obs,
		PlatformSupported: dep.SupportsPlatform(snap.Platform.OS, snap.Platform.Arch, snap.Platform.Libc),
	}
	switch {
	case rec != nil && rec.Status.InProgress() && s.interrupted(key, *rec, snap):
		failed, err := s.store.SetStatus(ctx, key, StatusFailed, interruptedMessage)
		if err != nil {
			return Entry{}, fmt.Errorf("workspacedeps: mark interrupted %s failed: %w", dep.ID, err)
		}
		s.logger.Warn("interrupted dependency operation marked failed",
			slog.String("bot_id", key.BotID),
			slog.String("workspace_target_id", key.WorkspaceTargetID),
			slog.String("dependency_id", key.DependencyID),
			slog.String("status", string(rec.Status)),
		)
		rec = &failed
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
		rec = &adopted
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

// interrupted reports whether an in-progress record belongs to an operation
// that is provably gone, so List may mark it failed at once (WD-STATE-002
// applied at read time). Three facts must agree, because in a multi-instance
// deployment another Server may be running the operation: this process holds
// no lock on the key, the record has gone untouched for staleOperationAge,
// and the workspace shows no lock directory for the dependency in a
// discovery taken after the run had that long to create one. A record that
// has outlived its whole budget (script timeout plus lock grace) is gone by
// the prelude's own rule whatever the lock directory says; the reaper would
// reach the same verdict on its next pass.
func (s *Service) interrupted(key InstallationKey, rec Installation, snap Snapshot) bool {
	if !rec.Status.InProgress() || s.locks.locked(key) {
		return false
	}
	age := s.now().Sub(rec.UpdatedAt)
	if age <= staleOperationAge {
		return false
	}
	if age > s.operationBudget(rec) {
		return true
	}
	if snap.Observed[rec.DependencyID].LockHeld {
		return false
	}
	// A cached snapshot may predate the operation, or have been taken before
	// its script reached the prelude; only one taken well after the record
	// went in progress can vouch for the lock's absence.
	return snap.At.Sub(rec.UpdatedAt) > staleOperationAge
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
	if obs.Present {
		// The version shown is that of the copy in effect, which is what the
		// launcher resolver runs and what the shim directory puts first on
		// PATH (managed → toolkit → PATH). The record keeps the discovery
		// winner's version, which is the same copy in practice.
		effective, image := effectiveCandidate(obs), imageCandidate(obs)
		if effective.Version != "" {
			entry.InstalledVersion = effective.Version
		}
		entry.ImageVersion = image.Version
		entry.Overlay = effective.Source == SourceManaged && image.Path != ""
		entry.UpdateAvailable = entry.LatestVersion != "" && entry.LatestVersion != entry.InstalledVersion
	}
	entry.Actions = availableActions(dep, rec, obs)
	return entry
}

// effectiveCandidate is the discovered copy in effect for a present
// dependency: the one selectLauncherCandidate picks (managed → toolkit →
// PATH), falling back to the discovery winner. The panel (buildEntry),
// Preflight, and the launcher resolver all go through the same order so none
// of them can disagree about which copy runs.
func effectiveCandidate(obs Observed) Candidate {
	if candidate, ok := selectLauncherCandidate(obs.Candidates); ok {
		return candidate
	}
	return Candidate{Source: obs.Source, Path: obs.Command, Version: obs.Version}
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

// imageCandidate is the toolkit copy discovery found, i.e. what the
// workspace image ships. The zero Candidate means the image has none.
func imageCandidate(obs Observed) Candidate {
	for _, candidate := range obs.Candidates {
		if candidate.Source == SourceToolkit && candidate.Path != "" {
			return candidate
		}
	}
	return Candidate{}
}

// hasManagedCopy reports whether discovery found a Server-installed copy
// (one with a usable state.json), whatever else is present next to it.
func hasManagedCopy(obs Observed) bool {
	for _, candidate := range obs.Candidates {
		if candidate.Source == SourceManaged && candidate.Path != "" {
			return true
		}
	}
	return false
}

// offlineEntry builds an Entry from the record alone, for a workspace that
// cannot be inspected: one that is not running, or one whose discovery
// failed. Without facts no action is offered.
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
	return entry
}

// ActionSupported reports whether the catalog gives the dependency a way to
// perform action: its own script, or the documented fallback (update runs
// install; reinstall runs remove then install). Rollback needs no script.
// Dependencies without an install script only ship with the image; nothing
// can be installed over them or removed from them.
func ActionSupported(dep catalog.Dependency, action catalog.Action) bool {
	switch action {
	case catalog.ActionInstall:
		return dep.Scripts.Install != ""
	case catalog.ActionUpdate:
		return dep.Scripts.Update != "" || dep.Scripts.Install != ""
	case catalog.ActionRemove:
		return dep.Scripts.Remove != ""
	case catalog.ActionReinstall:
		return dep.Scripts.Reinstall != "" || (dep.Scripts.Remove != "" && dep.Scripts.Install != "")
	case catalog.ActionCheckUpdate:
		return dep.Scripts.CheckUpdate != ""
	case catalog.ActionVersion:
		return dep.Scripts.Version != ""
	case ActionRollback:
		// Rollback needs no script, but only an installed overlay keeps a
		// previous version to switch back to.
		return dep.Scripts.Install != ""
	default:
		return false
	}
}

// UserActions is the order in which the actions a user may request are
// reported: the five scripted actions of design §4.3 plus rollback.
var UserActions = []catalog.Action{
	catalog.ActionInstall,
	catalog.ActionUpdate,
	catalog.ActionReinstall,
	catalog.ActionRemove,
	ActionRollback,
	catalog.ActionCheckUpdate,
}

// SupportedActions lists, in UserActions order, the actions the catalog lets
// the dependency perform regardless of any workspace state.
func SupportedActions(dep catalog.Dependency) []catalog.Action {
	actions := make([]catalog.Action, 0, len(UserActions))
	for _, action := range UserActions {
		if ActionSupported(dep, action) {
			actions = append(actions, action)
		}
	}
	return actions
}

// availableActions lists the actions the current state allows. A managed
// copy is what install produces and what update, reinstall, remove, and
// rollback operate on; an image copy underneath it is only ever a baseline.
// Update checks follow the script and the pin, not the category: an agent
// CLI is checked upstream like any other tool.
func availableActions(dep catalog.Dependency, rec *Installation, obs Observed) []catalog.Action {
	if rec != nil && rec.Status.InProgress() {
		return nil
	}
	var actions []catalog.Action
	switch {
	case !ActionSupported(dep, catalog.ActionInstall):
		// Image only: there is no overlay to manage.
	case !hasManagedCopy(obs):
		actions = append(actions, catalog.ActionInstall)
		if rec != nil && !obs.Present {
			// A missing or failed record can be dropped without a workspace
			// copy to delete; remove runs the script (a no-op then) and
			// deletes the intent.
			actions = append(actions, catalog.ActionRemove)
		}
	default:
		actions = append(actions, catalog.ActionUpdate, catalog.ActionReinstall, catalog.ActionRemove)
		if obs.State != nil && strings.TrimSpace(obs.State.PreviousVersion) != "" {
			actions = append(actions, ActionRollback)
		}
	}
	if obs.Present && upstreamCheckable(dep) {
		actions = append(actions, catalog.ActionCheckUpdate)
	}
	return actions
}

// Preflight checks whether every dependency in depIDs is present (design
// §9.3). It never starts a workspace: when the target cannot be inspected
// Items is empty and Workspace says why (WD-EXT-004).
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
	switch {
	case !obs.Present && !dep.SupportsPlatform(snap.Platform.OS, snap.Platform.Arch, snap.Platform.Libc):
		item.Reason = PreflightReasonPlatformUnsupported
	case !obs.Present:
		item.Reason = PreflightReasonMissing
	default:
		// The version reported is that of the copy the launcher resolver
		// runs, the same one the panel shows (effectiveCandidate).
		item.InstalledVersion = effectiveCandidate(obs).Version
		item.Satisfied = true
	}
	return item
}

// Install runs the install script and records the result. version is the
// version to install; empty means the manifest pin when there is one and
// otherwise whatever the script considers latest. The version recorded is
// the one the script reports, not the one requested. A stopped native
// workspace is started first; a missing one or an offline remote target is
// refused (WD-EXT-004). For a dependency the image already ships the result
// is a managed overlay that shadows the image copy.
func (s *Service) Install(ctx context.Context, botID, targetID, depID, version string, sink LogSink) (OperationResult, error) {
	op, err := s.begin(ctx, botID, targetID, depID, version, true)
	if err != nil {
		return OperationResult{}, err
	}
	defer op.release()
	return s.provision(ctx, op, catalog.ActionInstall, StatusInstalling, sink)
}

// Update runs the update script, falling back to the install script when the
// manifest has none (design §4.3). version follows the Install rules.
func (s *Service) Update(ctx context.Context, botID, targetID, depID, version string, sink LogSink) (OperationResult, error) {
	op, err := s.begin(ctx, botID, targetID, depID, version, true)
	if err != nil {
		return OperationResult{}, err
	}
	defer op.release()
	return s.provision(ctx, op, catalog.ActionUpdate, StatusUpdating, sink)
}

// Reinstall runs the manifest's reinstall script or, when there is none,
// remove followed by install under a single lock (design §4.3). A failed
// remove stops the operation. version follows the Install rules.
func (s *Service) Reinstall(ctx context.Context, botID, targetID, depID, version string, sink LogSink) (OperationResult, error) {
	op, err := s.begin(ctx, botID, targetID, depID, version, true)
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
	if err := s.markInProgress(ctx, op, StatusInstalling); err != nil {
		return OperationResult{}, err
	}
	if _, err := s.runScript(ctx, op, catalog.ActionRemove, removeScript, "", stateVersion(previous), 0, sink); err != nil {
		return OperationResult{}, s.fail(ctx, op, err)
	}
	if err := op.deleteShims(ctx, shimNames(op.dep, previous)); err != nil {
		return OperationResult{}, s.fail(ctx, op, err)
	}
	result, err := s.runScript(ctx, op, catalog.ActionInstall, installScript, op.version, "", 0, sink)
	if err != nil {
		return OperationResult{}, s.fail(ctx, op, err)
	}
	return s.commit(ctx, op, catalog.ActionReinstall, result, nil)
}

// Remove runs the remove script, deletes the shims, and drops the record.
// For a dependency the image ships this removes the managed overlay only:
// the next discovery finds the image copy again and adopts it as installed
// from the image.
func (s *Service) Remove(ctx context.Context, botID, targetID, depID string, sink LogSink) (OperationResult, error) {
	op, err := s.begin(ctx, botID, targetID, depID, "", false)
	if err != nil {
		return OperationResult{}, err
	}
	defer op.release()
	script, ok := s.catalog.Script(op.dep.ID, catalog.ActionRemove)
	if !ok {
		return OperationResult{}, fmt.Errorf("%w: %s has no remove script", ErrActionUnsupported, op.dep.ID)
	}
	previous := s.readStateBestEffort(ctx, op)
	if err := s.markInProgress(ctx, op, StatusRemoving); err != nil {
		return OperationResult{}, err
	}
	if _, err := s.runScript(ctx, op, catalog.ActionRemove, script, "", stateVersion(previous), 0, sink); err != nil {
		return OperationResult{}, s.fail(ctx, op, err)
	}
	// The script has run; finishing up must not depend on the request still
	// being there.
	finalCtx, cancel := finalizeContext(ctx)
	defer cancel()
	if err := op.deleteShims(finalCtx, shimNames(op.dep, previous)); err != nil {
		return OperationResult{}, s.fail(ctx, op, err)
	}
	if err := s.store.Delete(finalCtx, op.key); err != nil && !errors.Is(err, ErrInstallationNotFound) {
		return OperationResult{}, s.fail(ctx, op, fmt.Errorf("workspacedeps: delete record for %s: %w", op.dep.ID, err))
	}
	s.cache.Invalidate(op.key.BotID)
	return OperationResult{DependencyID: op.dep.ID, Action: catalog.ActionRemove}, nil
}

// Rollback switches `current` back to the previous version recorded in
// state.json (design §4.3). It runs no catalog script and needs no network;
// the only workspace command is the symlink switch through the prelude.
func (s *Service) Rollback(ctx context.Context, botID, targetID, depID string) (OperationResult, error) {
	op, err := s.begin(ctx, botID, targetID, depID, "", false)
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
	if err := s.markInProgress(ctx, op, StatusUpdating); err != nil {
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
	finalCtx, cancel := finalizeContext(ctx)
	defer cancel()
	if err := op.writeState(finalCtx, state); err != nil {
		return OperationResult{}, s.fail(ctx, op, err)
	}
	return s.record(ctx, op, ActionRollback, state)
}

// CheckUpdates is the manual refresh (design §10.3): it re-discovers the
// target, then runs check_update for every present, unpinned dependency with
// a check_update script and writes the result to its record.
func (s *Service) CheckUpdates(ctx context.Context, botID, targetID string) (ListResult, error) {
	targetID = normalizeTargetID(targetID)
	result, err := s.list(ctx, botID, targetID, true)
	if err != nil || result.Workspace != WorkspaceRunning || result.DiscoveryError != "" {
		// Without discovery facts there is nothing to check against.
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
	key InstallationKey
	dep catalog.Dependency
	// version is the MEMOH_DEP_VERSION install-like scripts receive: the
	// requested version, else the manifest pin, else empty for latest.
	version  string
	client   *bridge.Client
	dataRoot string
	home     string
	shimDir  string
	platform Platform
	release  func()
	// marked is set once markInProgress wrote the record; prior is what the
	// record said before that, nil when it did not exist. Both let a busy
	// verdict from the prelude undo the write (see restore).
	marked bool
	prior  *Installation
}

// begin validates the dependency, takes the in-memory lock, makes sure the
// workspace can run scripts, and resolves everything the action needs. The
// caller must release the returned operation.
func (s *Service) begin(ctx context.Context, botID, targetID, depID, version string, requirePlatform bool) (*operation, error) {
	targetID = normalizeTargetID(targetID)
	dep, err := s.dependency(depID)
	if err != nil {
		return nil, err
	}
	key := InstallationKey{BotID: botID, WorkspaceTargetID: targetID, DependencyID: dep.ID}
	if !s.locks.tryLock(key) {
		return nil, ErrBusy
	}
	op := &operation{key: key, dep: dep, version: targetVersion(dep, version), release: func() { s.locks.unlock(key) }}
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
	if err := s.markInProgress(ctx, op, status); err != nil {
		return OperationResult{}, err
	}
	result, err := s.runScript(ctx, op, action, script, op.version, stateVersion(previous), 0, sink)
	if err != nil {
		return OperationResult{}, s.fail(ctx, op, err)
	}
	return s.commit(ctx, op, action, result, previous)
}

// commit turns a successful install-like run into workspace state and a
// record: state.json, shims, then the installed row (design §6). The version
// recorded is the one the script reported through dep_result; the requested
// version only stands in when the script reported none, which "latest"
// never can. Once the script has succeeded the outcome is persisted on the
// finalization context: a request that went away meanwhile must not leave
// the workspace half committed and the record in progress.
func (s *Service) commit(ctx context.Context, op *operation, action catalog.Action, result Result, previous *State) (OperationResult, error) {
	version := strings.TrimSpace(result.Version)
	if version == "" {
		version = op.version
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
	finalCtx, cancel := finalizeContext(ctx)
	defer cancel()
	if err := op.writeState(finalCtx, state); err != nil {
		return OperationResult{}, s.fail(ctx, op, err)
	}
	if err := WriteShims(finalCtx, op.client, op.shimDir, state.Entrypoints, op.dep.IsAgent()); err != nil {
		return OperationResult{}, s.fail(ctx, op, err)
	}
	return s.record(ctx, op, action, state)
}

// record writes the installed row for a state.json the Server just wrote and
// invalidates the bot's discovery cache. The write runs on the finalization
// context; should it still fail, the record is marked failed rather than
// left in progress.
func (s *Service) record(ctx context.Context, op *operation, action catalog.Action, state State) (OperationResult, error) {
	storeCtx, cancel := finalizeContext(ctx)
	defer cancel()
	rec, err := s.store.Upsert(storeCtx, UpsertInstallation{
		InstallationKey:  op.key,
		Source:           InstallationSourceManaged,
		Status:           StatusInstalled,
		InstalledVersion: state.Version,
		ManifestDigest:   state.ManifestDigest,
	})
	if err != nil {
		return OperationResult{}, s.fail(ctx, op, fmt.Errorf("workspacedeps: record %s %s: %w", action, op.dep.ID, err))
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
// the dependency was never recorded. It remembers what the record said
// before so a busy verdict from the prelude can put it back (restore).
func (s *Service) markInProgress(ctx context.Context, op *operation, status Status) error {
	key := op.key
	prior, err := s.store.Get(ctx, key)
	switch {
	case errors.Is(err, ErrInstallationNotFound):
		_, err = s.store.Upsert(ctx, UpsertInstallation{InstallationKey: key, Source: InstallationSourceManaged, Status: status})
		if err == nil {
			op.marked, op.prior = true, nil
		}
	case err == nil:
		_, err = s.store.SetStatus(ctx, key, status, "")
		if err == nil {
			op.marked, op.prior = true, &prior
		}
	}
	if err != nil {
		return fmt.Errorf("workspacedeps: mark %s %s: %w", key.DependencyID, status, err)
	}
	return nil
}

// finalizeContext derives the context for the writes that record an
// operation's outcome: detached from the request's cancellation, so a closed
// dialog or a shutdown window cannot strand the record in progress, and
// bounded so a stuck store or bridge cannot hold the operation forever.
func finalizeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), finalizeTimeout)
}

// fail records a failed operation and returns the cause. When the cause is
// that the request went away, last_error says so. A busy verdict means the
// instance holding the workspace lock owns the record's state: the row is put
// back to what it said before this operation touched it, never marked failed.
func (s *Service) fail(ctx context.Context, op *operation, cause error) error {
	if errors.Is(cause, ErrBusy) {
		s.restore(ctx, op)
		return cause
	}
	message := cause.Error()
	if ctx.Err() != nil || errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		message = cancelledMessagePrefix + message
	}
	storeCtx, cancel := finalizeContext(ctx)
	defer cancel()
	if _, err := s.store.SetStatus(storeCtx, op.key, StatusFailed, truncateMessage(message)); err != nil {
		s.logger.Warn("record failed dependency operation",
			slog.String("bot_id", op.key.BotID),
			slog.String("dependency_id", op.dep.ID),
			slog.Any("error", err),
		)
	}
	s.cache.Invalidate(op.key.BotID)
	return cause
}

// restore undoes markInProgress after the prelude reported the dependency's
// workspace lock as held by another Server instance (design §8.4). That
// instance owns the record, so the row goes back to what it said before this
// operation wrote to it, or away again when this operation created it.
// Leaving our own in-progress status behind would show an operation nobody
// runs and nobody could finish.
func (s *Service) restore(ctx context.Context, op *operation) {
	if !op.marked {
		return
	}
	storeCtx, cancel := finalizeContext(ctx)
	defer cancel()
	var err error
	if op.prior == nil {
		err = s.store.Delete(storeCtx, op.key)
		if errors.Is(err, ErrInstallationNotFound) {
			err = nil
		}
	} else {
		_, err = s.store.SetStatus(storeCtx, op.key, op.prior.Status, op.prior.LastError)
	}
	if err != nil {
		s.logger.Warn("restore dependency record after busy verdict",
			slog.String("bot_id", op.key.BotID),
			slog.String("dependency_id", op.dep.ID),
			slog.Any("error", err),
		)
	}
	op.marked = false
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
// update checks (WD-UPD-001): it has a check_update script and no pin. The
// category does not matter; an agent CLI follows upstream like any tool.
func upstreamCheckable(dep catalog.Dependency) bool {
	return dep.Version.Pin == "" && dep.Scripts.CheckUpdate != ""
}

// targetVersion is the MEMOH_DEP_VERSION an install-like action passes: the
// version the caller asked for, else the manifest pin, else empty so the
// script installs whatever it considers latest.
func targetVersion(dep catalog.Dependency, requested string) string {
	if requested = strings.TrimSpace(requested); requested != "" {
		return requested
	}
	return dep.Version.Pin
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
// A busy verdict is not a check result at all — another operation holds the
// dependency — and leaves the record untouched.
func (s *Service) recordCheck(ctx context.Context, key InstallationKey, check updateCheck, checkErr error) error {
	if errors.Is(checkErr, ErrBusy) {
		return nil
	}
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
	storeCtx, cancel := finalizeContext(ctx)
	defer cancel()
	if _, err := s.store.UpdateObserved(storeCtx, key, upd); err != nil {
		if errors.Is(err, ErrInstallationNotFound) {
			return nil
		}
		return fmt.Errorf("workspacedeps: record update check for %s: %w", key.DependencyID, err)
	}
	return nil
}

// Dependency returns the catalog entry with the given id. Handlers use it to
// validate a request before an operation starts.
func (s *Service) Dependency(depID string) (catalog.Dependency, bool) {
	return s.catalog.Get(strings.TrimSpace(depID))
}

// Catalog returns every catalog dependency in catalog order. It reads no
// workspace: this is the bot-independent view the Supermarket shows before a
// bot is chosen.
func (s *Service) Catalog() []catalog.Dependency {
	return s.catalog.List()
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
	return truncateMessage(err.Error())
}

// truncateMessage caps a message at lastErrorLimit runes for storage and API
// responses.
func truncateMessage(message string) string {
	return textutil.TruncateRunesWithSuffix(strings.TrimSpace(message), lastErrorLimit, "...")
}
