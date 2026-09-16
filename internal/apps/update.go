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

// UpdateRequest selects what to update for one App in a bot's isolated workspace.
type UpdateRequest struct {
	RegistryID string
	AppID      string

	// Release moves the installation to the confirmed ReleaseRevision.
	Release                 bool
	ReleaseRevision         string
	DependencyConfirmations []DependencyConfirmation
	// Dependencies are updated to their confirmed version. Each must be one the
	// App references or, for a discovered canonical App, its own id.
	Dependencies []string
}

// UpdateSelection runs the chosen updates of one App as one stream:
// the dependencies first, each to its confirmed version, then the release.
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

	unlock, err := lockInstallation(ctx, botID, registryID, appID)
	if err != nil {
		return OperationResult{}, err
	}
	defer unlock()
	inst, err := s.store.Get(ctx, botID, registryID, appID)
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

	var targetRelease supermarket.AppDescriptor
	var required []string
	if req.Release {
		if !supermarket.IsCanonicalSHA256(req.ReleaseRevision) {
			return OperationResult{}, ErrInvalidRequest
		}
		targetRelease, err = s.registry.FetchRelease(ctx, registryID, appID, req.ReleaseRevision)
		if err != nil {
			return OperationResult{}, err
		}
		if err := validateReferences(targetRelease); err != nil {
			return OperationResult{}, err
		}
		if targetRelease.Revision != inst.Revision || (inst.Status != StatusInstalled && inst.Status != StatusPartial) {
			required = targetRelease.Dependencies
		}
	}
	states := map[string]workspacedeps.Entry{}
	if len(required)+len(depIDs) > 0 {
		states, err = s.dependencyStates(ctx, botID)
		if err != nil {
			return OperationResult{}, err
		}
	}
	confirmed, err := s.validateDependencyConfirmations(ctx, botID, required, depIDs, states, req.DependencyConfirmations)
	if err != nil {
		return OperationResult{}, err
	}
	result := OperationResult{Installation: inst}
	sink.Send(Event{Type: EventStarted, Kind: KindApp, ID: appID, Version: inst.Version})
	var firstErr error
	failed := 0
	for _, depID := range depIDs {
		sink.Send(Event{Type: EventStep, Kind: KindDependency, ID: depID})
		step := StepResult{Kind: KindDependency, ID: depID}
		target := confirmed[depID]
		res, err := s.dependencies.Update(workspacedeps.WithDefinitionRevision(ctx, target.DefinitionRevision), botID, depID, target.Version, logSink(sink, KindDependency, depID))
		if err != nil {
			s.logger.Warn("App dependency update failed", slog.String("dependency_id", depID), slog.Any("error", err))
			step.Status, step.Error = StepFailed, publicCause(err)
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
		releaseResult, err := s.applyRelease(ctx, botID, inst, targetRelease, sink, true, installConfirmations(req.DependencyConfirmations))
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

// applyRelease publishes the confirmed App release after the installation lock
// and all selected dependency confirmations have been checked.
func (s *Service) applyRelease(ctx context.Context, botID string, inst Installation, release supermarket.AppDescriptor, sink EventSink, announced bool, confirmations []DependencyConfirmation) (OperationResult, error) {
	if release.Revision == inst.Revision && (inst.Status == StatusInstalled || inst.Status == StatusPartial) {
		release, err := s.releaseFor(ctx, inst)
		if err != nil {
			return OperationResult{}, err
		}
		result := OperationResult{Installation: inst}
		// A partial installation waits for authorization, not for a release,
		// so it is not republished either. Retained references are durable
		// cleanup work, including rows left by an older Server; matching
		// revisions alone do not prove completion.
		if err := s.pruneReferences(ctx, inst, release, sink, &result); err != nil {
			return result, s.failInstallation(ctx, inst, err)
		}
		if _, err := s.store.SetCheck(ctx, botID, inst.ID, "", "", s.now().UTC()); err != nil {
			return result, fmt.Errorf("apps: record update check: %w", err)
		}
		sink.Send(Event{Type: EventDone, Kind: KindApp, ID: inst.AppID, Status: string(inst.Status), Version: inst.Version})
		return result, nil
	}
	if err := validateReferences(release); err != nil {
		return OperationResult{}, err
	}
	return s.materialize(ctx, botID, release, inst.Reason, StatusUpdating, sink, announced, confirmations)
}

func installConfirmations(confirmations []DependencyConfirmation) []DependencyConfirmation {
	result := make([]DependencyConfirmation, 0, len(confirmations))
	for _, target := range confirmations {
		if target.Action == "install" {
			result = append(result, target)
		}
	}
	return result
}
