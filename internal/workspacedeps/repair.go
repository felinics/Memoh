package workspacedeps

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/workspace/bridge"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

// RepairAuthorization describes the exact reinstallation to confirm before a
// legacy or adopted copy can become a durable recovery target.
type RepairAuthorization struct {
	DependencyID       string
	Version            string
	SourceURL          string
	RegistryID         string
	DefinitionRevision string
	ManifestDigest     string
}

func (s *Service) PrepareRepairAuthorization(ctx context.Context, botID, depID, version, revision string) (RepairAuthorization, error) {
	if _, ok := s.store.(DesiredStore); !ok {
		return RepairAuthorization{}, ErrRepairAuthorizationRequired
	}
	if version == "" {
		rec, err := s.store.Get(ctx, InstallationKey{BotID: botID, DependencyID: depID})
		if err != nil {
			return RepairAuthorization{}, err
		}
		version = rec.InstalledVersion
	}
	if !exactRepairVersion(version) {
		return RepairAuthorization{}, ErrInvalidVersion
	}
	ctx = WithDefinitionRevision(ctx, revision)
	cat, err := s.operationCatalog(ctx, depID)
	if err != nil {
		return RepairAuthorization{}, err
	}
	dep, ok := cat.Get(depID)
	if !ok || dep.Retired {
		return RepairAuthorization{}, ErrDependencyNotFound
	}
	if !catalog.ValidRevision(dep.Revision) {
		return RepairAuthorization{}, ErrDefinitionUnavailable
	}
	if dep.Version.Pin != "" && dep.Version.Pin != version {
		return RepairAuthorization{}, ErrInvalidVersion
	}
	return RepairAuthorization{DependencyID: depID, Version: version, SourceURL: dep.SourceURL, RegistryID: dep.RegistryID, DefinitionRevision: dep.Revision, ManifestDigest: dep.ManifestDigest}, nil
}

// AuthorizeRepair intentionally performs the confirmed reinstallation. It does
// not convert an untrusted workspace state file into execution authority.
func (s *Service) AuthorizeRepair(ctx context.Context, botID, depID, version, revision, actor string) (DesiredInstallation, error) {
	if !exactRepairVersion(version) || !catalog.ValidRevision(revision) {
		return DesiredInstallation{}, ErrInvalidVersion
	}
	ctx = WithRepairActor(WithDefinitionRevision(ctx, revision), actor)
	if _, err := s.Reinstall(ctx, botID, depID, version, nil); err != nil {
		return DesiredInstallation{}, err
	}
	store, ok := s.store.(DesiredStore)
	if !ok {
		return DesiredInstallation{}, ErrRepairAuthorizationRequired
	}
	return store.GetDesired(ctx, InstallationKey{BotID: botID, DependencyID: depID})
}

func exactRepairVersion(version string) bool {
	return version != "" && strings.TrimSpace(version) == version && ValidRequestedVersion(version) && ExactVersion(version)
}

// RetryRepair durably queues the current target. It never changes its version,
// recipe or authorization. Execution belongs to readiness or the repair worker.
func (s *Service) RetryRepair(ctx context.Context, botID, depID string) (DesiredInstallation, error) {
	store, ok := s.store.(DesiredStore)
	if !ok {
		return DesiredInstallation{}, ErrRepairAuthorizationRequired
	}
	target, err := store.GetDesired(ctx, InstallationKey{BotID: botID, DependencyID: depID})
	if errors.Is(err, ErrInstallationNotFound) {
		return DesiredInstallation{}, ErrRepairAuthorizationRequired
	}
	if err != nil {
		return DesiredInstallation{}, err
	}
	if target.RepairStatus == RepairQueued || target.RepairStatus == RepairInstalling {
		return target, nil
	}
	id, err := newRepairID()
	if err != nil {
		return DesiredInstallation{}, err
	}
	return store.QueueRepair(ctx, target, id, s.now().UTC(), true)
}

// EnsureDependenciesReady is an explicit execution-preparation boundary. It
// restores only confirmed targets, in prerequisite order. Read-only discovery
// never calls it. A caller must not silently fall back when this returns an error.
func (s *Service) EnsureDependenciesReady(ctx context.Context, botID string, depIDs []string) error {
	store, ok := s.store.(DesiredStore)
	if !ok {
		return nil
	}
	targets, err := store.ListDesired(ctx, botID)
	if err != nil {
		return err
	}
	if err := s.invalidateRevokedLaunchers(ctx, botID, targets); err != nil {
		return err
	}
	if len(targets) == 0 {
		return nil
	}
	state, err := s.workspace.State(ctx, botID)
	if err != nil {
		return err
	}
	if state != WorkspaceRunning {
		return ErrWorkspaceNotRunning
	}
	byID := make(map[string]DesiredInstallation, len(targets))
	for _, target := range targets {
		byID[target.DependencyID] = target
	}
	if len(depIDs) == 0 {
		for _, target := range targets {
			depIDs = append(depIDs, target.DependencyID)
		}
	}
	visiting, done := map[string]bool{}, map[string]bool{}
	var ensure func(string) error
	ensure = func(id string) error {
		if done[id] {
			return nil
		}
		if visiting[id] {
			return fmt.Errorf("%w: cyclic dependency target", ErrDefinitionInvalid)
		}
		target, managed := byID[id]
		if !managed {
			return nil
		}
		visiting[id] = true
		defer delete(visiting, id)
		cat, dep, err := s.frozenRepairCatalog(ctx, target, byID)
		if err != nil {
			return s.recordRepairPreparationFailure(ctx, target, err)
		}
		for _, required := range dep.Requires {
			if _, authorized := byID[required]; authorized {
				if err := ensure(required); err != nil {
					return err
				}
			} else if err := s.requireExistingPrerequisite(ctx, botID, cat, required); err != nil {
				return s.recordRepairPreparationFailure(ctx, target, err)
			}
		}
		if err := s.ensureDesiredTarget(ctx, target, cat, dep); err != nil {
			return err
		}
		visiting[id] = false
		done[id] = true
		return nil
	}
	var failures []error
	for _, id := range depIDs {
		if err := ensure(id); err != nil {
			failure := &external.DependencyMissingError{DependencyID: id}
			if target, targetErr := store.GetDesired(ctx, InstallationKey{BotID: botID, DependencyID: id}); targetErr == nil {
				failure.OperationInProgress = target.RepairStatus == RepairQueued || target.RepairStatus == RepairInstalling
				failure.RepairStatus = string(target.RepairStatus)
				failure.DesiredVersion = target.Version
				if failure.OperationInProgress {
					failure.RepairOperationID = target.RepairOperationID
				}
			}
			failures = append(failures, errors.Join(err, failure))
		}
	}
	return errors.Join(failures...)
}

func (s *Service) frozenRepairCatalog(ctx context.Context, target DesiredInstallation, targets map[string]DesiredInstallation) (*catalog.Catalog, catalog.Dependency, error) {
	if s.provider == nil || !exactRepairVersion(target.Version) {
		return nil, catalog.Dependency{}, ErrDefinitionUnavailable
	}
	result, err := s.provider.Cached(ctx)
	if err != nil {
		return nil, catalog.Dependency{}, err
	}
	// Assemble the reachable frozen graph before validating it. Merging a single
	// old definition into today's snapshot can invent cycles, lose prerequisites
	// after a registry change, or accidentally apply a new prerequisite policy.
	definitions := []catalog.Definition{}
	visiting, loaded := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("%w: cyclic dependency target", ErrDefinitionInvalid)
		}
		if loaded[id] {
			return nil
		}
		visiting[id] = true
		defer delete(visiting, id)
		approved, authorized := targets[id]
		current, present := result.Catalog.Get(id)
		var key DefinitionKey
		if authorized {
			if !exactRepairVersion(approved.Version) {
				return ErrDefinitionInvalid
			}
			if present && current.SourceURL == approved.SourceURL && current.Retired {
				return ErrDependencyNotFound
			}
			key = DefinitionKey{SourceURL: approved.SourceURL, DependencyID: id, Revision: approved.DefinitionRevision}
		} else {
			// No authorization is inferred here. This definition is used only to
			// discover an already-existing prerequisite later in the readiness pass.
			if !present || current.Retired {
				return ErrRepairAuthorizationRequired
			}
			key = DefinitionKey{SourceURL: current.SourceURL, DependencyID: id, Revision: current.Revision}
		}
		def, err := s.provider.StoredDefinition(ctx, key)
		if err != nil {
			return err
		}
		dep := def.Dependency()
		if dep.ID != id || dep.SourceURL != key.SourceURL || dep.Revision != key.Revision || dep.Retired {
			return ErrDefinitionInvalid
		}
		if authorized && (dep.RegistryID != approved.RegistryID || dep.ManifestDigest != approved.ManifestDigest || dep.Version.Pin != "" && dep.Version.Pin != approved.Version) {
			return ErrDefinitionInvalid
		}
		for _, required := range dep.Requires {
			if err := visit(required); err != nil {
				return err
			}
		}
		definitions = append(definitions, def)
		loaded[id] = true
		return nil
	}
	if err := visit(target.DependencyID); err != nil {
		return nil, catalog.Dependency{}, err
	}
	cat, err := catalog.New(definitions)
	if err != nil {
		return nil, catalog.Dependency{}, errors.Join(ErrDefinitionInvalid, err)
	}
	return cat, cat.MustGet(target.DependencyID), nil
}

func (s *Service) requireExistingPrerequisite(ctx context.Context, botID string, cat *catalog.Catalog, depID string) error {
	dep, ok := cat.Get(depID)
	if !ok {
		return ErrRepairAuthorizationRequired
	}
	client, dataRoot, err := s.target(ctx, botID)
	if err != nil {
		return err
	}
	platform, err := s.platformFor(ctx, botID, client)
	if err != nil {
		return err
	}
	observed, err := s.discover(ctx, client, cat, dataRoot, []string{depID}, platform)
	if err != nil {
		return err
	}
	obs := observed[depID]
	for _, candidate := range obs.Candidates {
		if candidate.Path != "" && (dep.Version.Pin == "" || candidate.Version == dep.Version.Pin) {
			return nil
		}
	}
	return ErrRepairAuthorizationRequired
}

func (s *Service) ensureDesiredTarget(ctx context.Context, target DesiredInstallation, cat *catalog.Catalog, dep catalog.Dependency) error {
	store := s.store.(DesiredStore)
	current, err := store.GetDesired(ctx, target.InstallationKey)
	if err != nil {
		return err
	}
	if current.Revision != target.Revision {
		return ErrDesiredTargetChanged
	}
	target = current
	if target.RepairStatus == RepairInstalling {
		// An observation timeout is not process death. The existing receipt
		// reaper arbitrates completion before a future repair may execute.
		if _, err := s.ReapStale(ctx); err != nil {
			return err
		}
		current, err = store.GetDesired(ctx, target.InstallationKey)
		if err != nil {
			return err
		}
		if current.Revision != target.Revision {
			return ErrDesiredTargetChanged
		}
		if current.RepairStatus == RepairReady {
			s.invalidateSupersededLauncher(current)
			return nil
		}
		rec, recErr := s.store.Get(ctx, target.InstallationKey)
		if recErr == nil && rec.Status.InProgress() {
			return ErrRepairPending
		}
		if recErr != nil && !errors.Is(recErr, ErrInstallationNotFound) {
			return recErr
		}
		return s.recordRepairFailure(ctx, target, ErrOperationUncertain)
	}
	if target.RepairStatus == RepairManualRequired {
		return ErrRepairManualRequired
	}
	if target.RepairStatus == RepairBackoff && target.RepairNextAttemptAt != nil && s.now().Before(*target.RepairNextAttemptAt) {
		return ErrRepairPending
	}
	payloadReady, linksReady, err := s.inspectDesiredTarget(ctx, target, cat, dep)
	if err != nil {
		return s.recordRepairPreparationFailure(ctx, target, err)
	}
	if payloadReady && linksReady {
		s.invalidateSupersededLauncher(target)
		if target.RepairStatus == RepairQueued || target.RepairStatus == RepairBackoff {
			_, err = store.SetRepairResult(ctx, target, RepairReady, target.RepairAttempts, nil, "")
		}
		return err
	}
	if target.RepairStatus != RepairQueued {
		id, err := newRepairID()
		if err != nil {
			return err
		}
		target, err = store.QueueRepair(ctx, target, id, s.now().UTC(), false)
		if err != nil {
			return err
		}
	}
	// Both reads and the store claim fence the queued revision. Neither a
	// toolkit fallback nor an altered state.json changes this chosen target.
	runCtx := context.WithValue(ctx, frozenRepairCatalogKey{}, cat)
	runCtx = context.WithValue(runCtx, repairTargetKey{}, target)
	if payloadReady {
		err = s.restoreDesiredEntrypoints(runCtx, target, cat)
	} else {
		_, err = s.Reinstall(runCtx, target.BotID, target.DependencyID, target.Version, nil)
	}
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrBusy) || errors.Is(err, ErrDesiredTargetChanged) {
		return ErrRepairPending
	}
	// Live/uncertain workspace execution retains the queued operation identity
	// for normal receipt reconciliation; never launch a second script.
	if errors.Is(err, ErrOperationUncertain) {
		return err
	}
	return s.recordRepairFailure(ctx, target, err)
}

// A different Server may have committed an update. Local cache invalidation at
// commit cannot invalidate its peers, so execution readiness fences their cached
// launcher against the durable installation identity before starting new work.
func (s *Service) invalidateSupersededLauncher(target DesiredInstallation) {
	if snapshot, ok := s.cache.Get(target.BotID); ok {
		state := snapshot.Observed[target.DependencyID].State
		if state == nil || state.InstallationID != target.InstallationID || state.PayloadPath != target.PayloadPath || state.Version != target.Version {
			s.cache.Invalidate(target.BotID)
		}
	}
}

func (s *Service) inspectDesiredTarget(ctx context.Context, target DesiredInstallation, cat *catalog.Catalog, dep catalog.Dependency) (bool, bool, error) {
	client, dataRoot, err := s.target(ctx, target.BotID)
	if err != nil {
		return false, false, err
	}
	platform, err := s.probe(ctx, client)
	if err != nil {
		return false, false, err
	}
	if platform.OS != target.Platform.OS || platform.Arch != target.Platform.Arch || platform.Libc != target.Platform.Libc || !dep.SupportsPlatform(platform.OS, platform.Arch, platform.Libc) {
		return false, false, ErrPlatformUnsupported
	}
	if target.PayloadPath == "" || !path.IsAbs(target.PayloadPath) || path.Clean(target.PayloadPath) != target.PayloadPath {
		return false, false, ErrRepairManualRequired
	}
	home := Home(dataRoot, target.DependencyID)
	var script strings.Builder
	fmt.Fprintf(&script, "[ -d %s ] || exit 41\n", shellQuote(target.PayloadPath))
	payloadEntrypoints, err := desiredPayloadEntrypoints(target, home, dep.Provides)
	if err != nil {
		return false, false, err
	}
	for _, command := range dep.Provides {
		fmt.Fprintf(&script, "[ -x %s ] || exit 42\n", shellQuote(payloadEntrypoints[command]))
	}
	fmt.Fprintf(&script, "[ \"$(cd %s 2>/dev/null && pwd -P)\" = %s ] || exit 44\n", shellQuote(path.Join(home, "current")), shellQuote(target.PayloadPath))
	for _, command := range dep.Provides {
		fmt.Fprintf(&script, "[ -x %s ] || exit 44\n", shellQuote(path.Join(ShimDir(dataRoot), command)))
	}
	result, err := client.ExecWithOptions(ctx, script.String(), dataRoot, 15, nil, bridge.ExecOptions{})
	if err != nil {
		return false, false, err
	}
	switch result.ExitCode {
	case 41:
		return false, false, nil
	case 42:
		return false, false, nil
	case 0, 44:
		version, err := s.probeInstallationVersion(ctx, client, cat, dep, home, dataRoot, payloadEntrypoints[dep.Provides[0]], platform)
		if err != nil {
			if dep.StorageLayout == "isolated" {
				// The next candidate is verified before publication. If the same
				// artifact cannot run in this environment, repair backs off and
				// reaches its bounded manual-attention state.
				return false, false, nil
			}
			return false, false, errors.Join(ErrRepairManualRequired, err)
		}
		if version != target.Version {
			return false, false, ErrRepairManualRequired
		}
		return true, result.ExitCode == 0, nil
	default:
		return false, false, ErrRepairManualRequired
	}
}

func newRepairID() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(nonce[:]), nil
}

func repairFailureCode(err error) (string, bool) {
	switch {
	case errors.Is(err, ErrRepairAuthorizationRequired):
		return "workspace_dependency.repair_authorization_required", true
	case errors.Is(err, ErrDefinitionUnavailable), errors.Is(err, ErrDefinitionInvalid), errors.Is(err, ErrDependencyNotFound):
		return "workspace_dependency.definition_unavailable", true
	case errors.Is(err, ErrPlatformUnsupported):
		return "workspace_dependency.platform_unsupported", true
	case errors.Is(err, ErrRepairManualRequired):
		return "workspace_dependency.repair_manual_required", true
	default:
		return "workspace_dependency.repair_failed", false
	}
}

func repairRetryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 6 {
		attempts = 6
	}
	return time.Minute * time.Duration(1<<uint(attempts-1))
}

func (s *Service) recordRepairPreparationFailure(ctx context.Context, target DesiredInstallation, cause error) error {
	if target.RepairStatus == RepairInstalling {
		return cause
	}
	// Preserve the observation's status in the CAS. Another Server may have
	// claimed this queued operation while catalog or filesystem probing failed;
	// that observation cannot terminate the new owner's running operation.
	return s.setRepairFailure(ctx, target, cause)
}

func (s *Service) recordRepairFailure(ctx context.Context, target DesiredInstallation, cause error) error {
	store := s.store.(DesiredStore)
	current, err := store.GetDesired(ctx, target.InstallationKey)
	if err != nil {
		return errors.Join(cause, err)
	}
	if current.Revision != target.Revision || current.RepairOperationID != target.RepairOperationID {
		return ErrDesiredTargetChanged
	}
	return s.setRepairFailure(ctx, current, cause)
}

func (s *Service) setRepairFailure(ctx context.Context, current DesiredInstallation, cause error) error {
	store := s.store.(DesiredStore)
	code, permanent := repairFailureCode(cause)
	status := RepairBackoff
	attempts := current.RepairAttempts
	if current.RepairStatus != RepairInstalling {
		attempts++
	}
	if attempts == 0 {
		attempts = 1
	}
	var next *time.Time
	if permanent || attempts >= 5 {
		status = RepairManualRequired
	} else {
		n := s.now().UTC().Add(repairRetryDelay(attempts))
		next = &n
	}
	_, err := store.SetRepairResult(ctx, current, status, attempts, next, code)
	if err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

// CheckRepairs scans only this store's Team scope and does not start stopped
// workspaces. Pagination survives large installations without unbounded reads.
func (s *Service) CheckRepairs(ctx context.Context) error {
	store, ok := s.store.(DesiredStore)
	if !ok {
		return nil
	}
	var after InstallationKey
	var errs []error
	if lister, ok := s.workspace.(interface {
		ListBots(context.Context) ([]string, error)
	}); ok {
		bots, err := lister.ListBots(ctx)
		if err != nil {
			errs = append(errs, err)
		}
		for _, botID := range bots {
			state, err := s.workspace.State(ctx, botID)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if state == WorkspaceRunning {
				if err := s.ReapPayloads(ctx, botID); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	visited := map[string]bool{}
	for {
		targets, err := store.ListDesiredPage(ctx, after, 128)
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		if len(targets) == 0 {
			break
		}
		for _, target := range targets {
			after = target.InstallationKey
			if visited[target.BotID] {
				continue
			}
			visited[target.BotID] = true
			state, err := s.workspace.State(ctx, target.BotID)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if state != WorkspaceRunning {
				continue
			}
			if err := s.ReapPayloads(ctx, target.BotID); err != nil {
				errs = append(errs, err)
			}
			if err := s.EnsureDependenciesReady(ctx, target.BotID, nil); err != nil && !errors.Is(err, ErrRepairPending) && !errors.Is(err, ErrRepairManualRequired) {
				errs = append(errs, err)
			}
		}
		if len(targets) < 128 {
			break
		}
	}
	return errors.Join(errs...)
}

func StartRepairWorker(ctx context.Context, svc *Service, interval time.Duration, logger *slog.Logger) func() {
	if interval <= 0 {
		interval = time.Minute
	}
	if logger == nil {
		logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if err := svc.CheckRepairs(ctx); err != nil && ctx.Err() == nil {
				logger.Warn("dependency repair pass", slog.Any("error", err))
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-svc.repairWake:
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { cancel(); <-done }) }
}

// NotifyWorkspaceReady coalesces ready events into one bounded worker wakeup.
// It never runs a script inside the workspace startup call stack.
func (s *Service) NotifyWorkspaceReady(ctx context.Context, _ string) {
	if ctx.Err() != nil {
		return
	}
	select {
	case <-s.shutdownCtx.Done():
	case s.repairWake <- struct{}{}:
	default:
	}
}

// Desired entrypoints were committed by a management operation. Restrict link
// repair to the same dependency's current tree rather than inferring a /bin
// layout or executing arbitrary paths from a mutable workspace state file.
func desiredPayloadEntrypoints(target DesiredInstallation, home string, provides []string) (map[string]string, error) {
	if len(provides) == 0 {
		return nil, ErrDefinitionInvalid
	}
	out := make(map[string]string, len(provides))
	currentPrefix := path.Join(home, "current") + "/"
	for _, command := range provides {
		entry := target.Entrypoints[command]
		if command == "" || path.Base(command) != command || path.Clean(entry) != entry {
			return nil, ErrRepairManualRequired
		}
		prefix := currentPrefix
		if strings.HasPrefix(entry, target.PayloadPath+"/") {
			prefix = target.PayloadPath + "/"
		}
		if !strings.HasPrefix(entry, prefix) {
			return nil, ErrRepairManualRequired
		}
		relative := strings.TrimPrefix(entry, prefix)
		if relative == "" {
			return nil, ErrRepairManualRequired
		}
		out[command] = path.Join(target.PayloadPath, relative)
	}
	return out, nil
}

// A peer may remove the final target while this Server still holds the old
// discovery snapshot. Readiness must invalidate that snapshot even though
// there is no longer a desired row to run through ensureDesiredTarget.
func (s *Service) invalidateRevokedLaunchers(ctx context.Context, botID string, targets []DesiredInstallation) error {
	snapshot, ok := s.cache.Get(botID)
	if !ok {
		return nil
	}
	authorized := make(map[string]bool, len(targets))
	for _, target := range targets {
		authorized[target.DependencyID] = true
	}
	for id, observed := range snapshot.Observed {
		state := observed.State
		if authorized[id] || state == nil || state.InstallationID == "" {
			continue
		}
		record, err := s.store.Get(ctx, InstallationKey{BotID: botID, DependencyID: id})
		if errors.Is(err, ErrInstallationNotFound) {
			s.cache.Invalidate(botID)
			return nil
		}
		if err != nil {
			return err
		}
		if record.Source != InstallationSourceManaged || record.LastOperationID != state.InstallationID || record.InstalledVersion != state.Version {
			s.cache.Invalidate(botID)
			return nil
		}
	}
	return nil
}
