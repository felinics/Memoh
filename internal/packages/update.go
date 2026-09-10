package packages

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/felinics/memoh/internal/supermarket"
	"github.com/felinics/memoh/internal/workspacedeps"
)

// Update moves an installation to the registry's current release. Skills
// are replaced atomically, new references are linked or installed, and
// references the new release dropped are released the way Remove would.
// Dependency definitions keep their own update cycle; a Package update never
// reinstalls a dependency that is already present.
func (s *Service) Update(ctx context.Context, botID, installationID string, sink EventSink) (OperationResult, error) {
	sink = nonNilSink(sink)
	inst, err := s.store.GetByID(ctx, botID, installationID)
	if err != nil {
		return OperationResult{}, err
	}
	unlock, err := lockInstallation(ctx, botID, inst.WorkspaceTargetID, inst.RegistryID, inst.PackageID)
	if err != nil {
		return OperationResult{}, err
	}
	defer unlock()
	inst, err = s.store.GetByID(ctx, botID, installationID)
	if err != nil {
		return OperationResult{}, err
	}
	current, err := s.registry.FetchCurrentPackage(ctx, inst.RegistryID, inst.PackageID)
	if err != nil {
		return OperationResult{}, err
	}
	if current.Revision == inst.Revision {
		if _, err := s.store.SetCheck(ctx, botID, inst.ID, "", "", s.now().UTC()); err != nil {
			return OperationResult{}, fmt.Errorf("packages: record update check: %w", err)
		}
		sink.Send(Event{Type: EventDone, Kind: KindPackage, ID: inst.PackageID, Status: string(inst.Status), Version: inst.Version})
		return OperationResult{Installation: inst}, nil
	}
	release, err := s.registry.FetchRelease(ctx, inst.RegistryID, inst.PackageID, current.Revision)
	if err != nil {
		return OperationResult{}, err
	}
	if err := validateReferences(release); err != nil {
		return OperationResult{}, err
	}
	previousDeps, err := s.store.ListDependencyRefs(ctx, inst.ID)
	if err != nil {
		return OperationResult{}, fmt.Errorf("packages: list dependency references: %w", err)
	}
	previousConns, err := s.store.ListConnectorRefs(ctx, inst.ID)
	if err != nil {
		return OperationResult{}, fmt.Errorf("packages: list connector references: %w", err)
	}
	result, err := s.materialize(ctx, botID, inst.WorkspaceTargetID, release, inst.Reason, StatusUpdating, sink)
	if err != nil {
		return result, err
	}
	s.pruneReferences(ctx, result.Installation, release, previousDeps, previousConns, sink, &result)
	return result, nil
}

// pruneReferences releases references the new release no longer declares.
func (s *Service) pruneReferences(ctx context.Context, inst Installation, release supermarket.SkillPackageDescriptor, previousDeps []DependencyRef, previousConns []ConnectorRef, sink EventSink, result *OperationResult) {
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
