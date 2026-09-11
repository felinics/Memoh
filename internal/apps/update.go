package apps

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	skillset "github.com/felinics/memoh/internal/skills"
	"github.com/felinics/memoh/internal/supermarket"
	"github.com/felinics/memoh/internal/workspacedeps"
)

// UpdateRequest selects what to update for one App on a workspace target.
type UpdateRequest struct {
	RegistryID        string
	AppID             string
	WorkspaceTargetID string
	// Release moves the installation to the registry's current release.
	Release bool
	// Dependencies are updated to their latest version. Each must be one the
	// App references or, for a discovered canonical App, its own id.
	Dependencies []string
}

// UpdateSelection runs the chosen updates of one App as one stream:
// the dependencies first, each to its latest version, then the release.
// A dependency that fails does not stop the others; the release step only
// runs when it was selected.
func (s *Service) UpdateSelection(ctx context.Context, botID string, req UpdateRequest, sink EventSink) (OperationResult, error) {
	sink = nonNilSink(sink)
	registryID := strings.TrimSpace(req.RegistryID)
	appID := strings.TrimSpace(req.AppID)
	if !skillset.IsValidRegistryID(registryID) || !skillset.IsValidRegistryComponent(appID) {
		return OperationResult{}, ErrInvalidRequest
	}
	depIDs := uniqueDependencyIDs(req.Dependencies)
	if !req.Release && len(depIDs) == 0 {
		return OperationResult{}, fmt.Errorf("%w: nothing selected to update", ErrInvalidRequest)
	}
	if len(depIDs) > 0 && registryID != DependencyRegistryID {
		return OperationResult{}, fmt.Errorf("%w: dependencies are only supported in the %s registry", ErrInvalidRequest, DependencyRegistryID)
	}
	if len(depIDs) > 0 && s.dependencies == nil {
		return OperationResult{}, ErrDependenciesUnavailable
	}
	targetID, err := s.skills.ResolveTargetID(ctx, botID, req.WorkspaceTargetID)
	if err != nil {
		return OperationResult{}, err
	}
	unlock, err := lockInstallation(ctx, botID, targetID, registryID, appID)
	if err != nil {
		return OperationResult{}, err
	}
	defer unlock()
	inst, err := s.store.Get(ctx, botID, targetID, registryID, appID)
	installed := err == nil
	if err != nil && !errors.Is(err, ErrNotInstalled) {
		return OperationResult{}, fmt.Errorf("apps: read installation: %w", err)
	}
	if req.Release && !installed {
		return OperationResult{}, ErrNotInstalled
	}
	// Only the App's own dependencies may be updated through it. A
	// discovered App is the canonical one of a single dependency.
	allowed := map[string]bool{appID: !installed}
	if installed {
		release, err := s.releaseFor(ctx, inst)
		if err != nil {
			return OperationResult{}, err
		}
		for _, id := range release.Dependencies {
			allowed[id] = true
		}
	}
	for _, id := range depIDs {
		if !allowed[id] {
			return OperationResult{}, fmt.Errorf("%w: %s does not reference dependency %s", ErrInvalidRequest, appID, id)
		}
	}

	result := OperationResult{Installation: inst}
	sink.Send(Event{Type: EventStarted, Kind: KindApp, ID: appID, Version: inst.Version})
	var firstErr error
	failed := 0
	for _, depID := range depIDs {
		sink.Send(Event{Type: EventStep, Kind: KindDependency, ID: depID})
		step := StepResult{Kind: KindDependency, ID: depID}
		res, err := s.dependencies.Update(ctx, botID, targetID, depID, "", logSink(sink, KindDependency, depID))
		if err != nil {
			step.Status, step.Error = StepFailed, err.Error()
			failed++
			if firstErr == nil {
				firstErr = err
			}
		} else {
			step.Status, step.Version = StepUpdated, res.Version
		}
		result.Steps = append(result.Steps, step)
		sink.Send(Event{Type: EventStepDone, Kind: step.Kind, ID: step.ID, Status: step.Status, Version: step.Version, Message: step.Error})
	}
	if req.Release {
		releaseResult, err := s.updateRelease(ctx, botID, inst, sink, true)
		result.Steps = append(result.Steps, releaseResult.Steps...)
		if releaseResult.Installation.ID != "" {
			result.Installation = releaseResult.Installation
		}
		return result, err
	}
	if failed == len(depIDs) {
		return result, fmt.Errorf("apps: update dependencies of %s: %w", appID, firstErr)
	}
	// The handler reports an App without an installation as discovered.
	status := "discovered"
	if installed {
		status = string(inst.Status)
	}
	sink.Send(Event{Type: EventDone, Kind: KindApp, ID: appID, Status: status, Version: inst.Version})
	return result, nil
}

func uniqueDependencyIDs(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	unique := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	return unique
}

// Update moves an installation to the registry's current release. Skills
// are replaced atomically, new references are linked or installed, and
// references the new release dropped are released the way Remove would.
// Dependency definitions keep their own update cycle; an App update never
// reinstalls a dependency that is already present.
func (s *Service) Update(ctx context.Context, botID, installationID string, sink EventSink) (OperationResult, error) {
	sink = nonNilSink(sink)
	inst, err := s.store.GetByID(ctx, botID, installationID)
	if err != nil {
		return OperationResult{}, err
	}
	unlock, err := lockInstallation(ctx, botID, inst.WorkspaceTargetID, inst.RegistryID, inst.AppID)
	if err != nil {
		return OperationResult{}, err
	}
	defer unlock()
	inst, err = s.store.GetByID(ctx, botID, installationID)
	if err != nil {
		return OperationResult{}, err
	}
	return s.updateRelease(ctx, botID, inst, sink, false)
}

// updateRelease is the body of Update once the installation is locked.
// announced is set when the caller already sent the started event.
func (s *Service) updateRelease(ctx context.Context, botID string, inst Installation, sink EventSink, announced bool) (OperationResult, error) {
	current, err := s.registry.FetchCurrentApp(ctx, inst.RegistryID, inst.AppID)
	if err != nil {
		return OperationResult{}, err
	}
	if current.Revision == inst.Revision {
		if _, err := s.store.SetCheck(ctx, botID, inst.ID, "", "", s.now().UTC()); err != nil {
			return OperationResult{}, fmt.Errorf("apps: record update check: %w", err)
		}
		sink.Send(Event{Type: EventDone, Kind: KindApp, ID: inst.AppID, Status: string(inst.Status), Version: inst.Version})
		return OperationResult{Installation: inst}, nil
	}
	release, err := s.registry.FetchRelease(ctx, inst.RegistryID, inst.AppID, current.Revision)
	if err != nil {
		return OperationResult{}, err
	}
	if err := validateReferences(release); err != nil {
		return OperationResult{}, err
	}
	previousDeps, err := s.store.ListDependencyRefs(ctx, inst.ID)
	if err != nil {
		return OperationResult{}, fmt.Errorf("apps: list dependency references: %w", err)
	}
	previousConns, err := s.store.ListConnectorRefs(ctx, inst.ID)
	if err != nil {
		return OperationResult{}, fmt.Errorf("apps: list connector references: %w", err)
	}
	result, err := s.materialize(ctx, botID, inst.WorkspaceTargetID, release, inst.Reason, StatusUpdating, sink, announced)
	if err != nil {
		return result, err
	}
	s.pruneReferences(ctx, result.Installation, release, previousDeps, previousConns, sink, &result)
	return result, nil
}

// pruneReferences releases references the new release no longer declares.
func (s *Service) pruneReferences(ctx context.Context, inst Installation, release supermarket.AppDescriptor, previousDeps []DependencyRef, previousConns []ConnectorRef, sink EventSink, result *OperationResult) {
	keepDeps := make(map[string]bool, len(release.Dependencies))
	for _, depID := range release.Dependencies {
		keepDeps[depID] = true
	}
	keepConns := make(map[string]bool, len(release.Connectors))
	for _, ref := range release.Connectors {
		keepConns[ref.Type] = true
	}
	var targetRefs []TargetDependencyRef
	var states map[string]workspacedeps.Entry
	for _, ref := range previousDeps {
		if keepDeps[ref.DependencyID] {
			continue
		}
		if targetRefs == nil {
			refs, err := s.store.ListTargetDependencyRefs(ctx, inst.BotID, inst.WorkspaceTargetID)
			if err != nil {
				s.logger.Warn("list dependency references", slog.Any("error", err))
				refs = nil
			}
			targetRefs = refs
			states, _ = s.dependencyStates(ctx, inst.BotID, inst.WorkspaceTargetID)
		}
		step := StepResult{Kind: KindDependency, ID: ref.DependencyID, Status: StepKept}
		entry, known := states[ref.DependencyID]
		if s.dependencies != nil && !referencedByOthers(targetRefs, ref.DependencyID, inst.ID) && known && entry.Observed.Present && entry.Observed.Source == workspacedeps.SourceManaged {
			sink.Send(Event{Type: EventStep, Kind: KindDependency, ID: ref.DependencyID})
			if _, err := s.dependencies.Remove(ctx, inst.BotID, inst.WorkspaceTargetID, ref.DependencyID, logSink(sink, KindDependency, ref.DependencyID)); err != nil {
				step.Status, step.Error = StepFailed, err.Error()
			} else {
				step.Status = StepRemoved
			}
		}
		if err := s.store.RemoveDependencyRef(ctx, inst.ID, ref.DependencyID); err != nil {
			s.logger.Warn("drop dependency reference", slog.String("dependency_id", ref.DependencyID), slog.Any("error", err))
		}
		result.Steps = append(result.Steps, step)
		sink.Send(Event{Type: EventStepDone, Kind: step.Kind, ID: step.ID, Status: step.Status, Message: step.Error})
	}
	var botRefs []BotConnectorRef
	for _, ref := range previousConns {
		if keepConns[ref.ConnectorType] {
			continue
		}
		if botRefs == nil {
			refs, err := s.store.ListBotConnectorRefs(ctx, inst.BotID)
			if err != nil {
				s.logger.Warn("list connector references", slog.Any("error", err))
			}
			botRefs = refs
		}
		step := StepResult{Kind: KindConnector, ID: ref.ConnectorType, Status: StepKept}
		if ref.ConnectionID != "" && s.connectors != nil && !connectionReferencedByOthers(botRefs, ref.ConnectionID, inst.ID) {
			sink.Send(Event{Type: EventStep, Kind: KindConnector, ID: ref.ConnectorType})
			if err := s.connectors.Delete(ctx, inst.BotID, ref.ConnectionID); err != nil && !isNotFound(err) {
				step.Status, step.Error = StepFailed, err.Error()
			} else {
				step.Status = StepDisconnected
			}
		}
		if err := s.store.RemoveConnectorRef(ctx, inst.ID, ref.ConnectorType); err != nil {
			s.logger.Warn("drop connector reference", slog.String("connector_type", ref.ConnectorType), slog.Any("error", err))
		}
		result.Steps = append(result.Steps, step)
		sink.Send(Event{Type: EventStepDone, Kind: step.Kind, ID: step.ID, Status: step.Status, Message: step.Error})
	}
}
