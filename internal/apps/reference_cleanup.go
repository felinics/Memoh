package apps

import (
	"context"
	"fmt"
	"slices"

	"github.com/felinics/memoh/internal/connectors"
	"github.com/felinics/memoh/internal/supermarket"
	"github.com/felinics/memoh/internal/workspacedeps"
)

// pruneReferences releases references absent from the published release. All
// required reads finish before cleanup starts: a failed query is not evidence
// that a shared resource is unused. Retained references let later requests
// resume cleanup after a failure or Server restart.
//
// This snapshot does not fence concurrent reference writers across Apps or
// Servers. Resource-level deletion claims are a separate lifecycle change.
func (s *Service) pruneReferences(ctx context.Context, inst Installation, release supermarket.AppDescriptor, sink EventSink, result *OperationResult) error {
	deps, err := s.store.ListDependencyRefs(ctx, inst.ID)
	if err != nil {
		return fmt.Errorf("apps: list dependency references for cleanup: %w", err)
	}
	conns, err := s.store.ListConnectorRefs(ctx, inst.ID)
	if err != nil {
		return fmt.Errorf("apps: list connector references for cleanup: %w", err)
	}
	deps = slices.DeleteFunc(deps, func(ref DependencyRef) bool {
		return slices.Contains(release.Dependencies, ref.DependencyID)
	})
	conns = slices.DeleteFunc(conns, func(ref ConnectorRef) bool {
		return slices.ContainsFunc(release.Connectors, func(keep supermarket.AppConnectorReference) bool {
			return keep.Type == ref.ConnectorType
		})
	})
	var targetRefs []TargetDependencyRef
	if len(deps) > 0 {
		targetRefs, err = s.store.ListTargetDependencyRefs(ctx, inst.BotID, inst.WorkspaceTargetID)
		if err != nil {
			return fmt.Errorf("apps: inspect shared dependencies before cleanup: %w", err)
		}
	}
	var botRefs []BotConnectorRef
	if len(conns) > 0 {
		botRefs, err = s.store.ListBotConnectorRefs(ctx, inst.BotID)
		if err != nil {
			return fmt.Errorf("apps: inspect shared connectors before cleanup: %w", err)
		}
		for _, ref := range conns {
			if ref.ConnectionID != "" && !connectionReferencedByOthers(botRefs, ref.ConnectionID, inst.ID) && s.connectors == nil {
				return connectors.ErrNotConfigured
			}
		}
	}
	states, err := s.cleanupDependencyStates(ctx, inst, deps, targetRefs)
	if err != nil {
		return err
	}
	record := func(step StepResult, cause error) error {
		if cause != nil {
			step.Status, step.Error = StepFailed, cause.Error()
		}
		result.Steps = append(result.Steps, step)
		sink.Send(Event{Type: EventStepDone, Kind: step.Kind, ID: step.ID, Status: step.Status, Message: step.Error})
		return cause
	}
	for _, ref := range deps {
		step := StepResult{Kind: KindDependency, ID: ref.DependencyID, Status: StepKept}
		entry := states[ref.DependencyID]
		if !referencedByOthers(targetRefs, ref.DependencyID, inst.ID) && entry.Observed.Present && entry.Observed.Source == workspacedeps.SourceManaged {
			sink.Send(Event{Type: EventStep, Kind: KindDependency, ID: ref.DependencyID})
			if _, err := s.dependencies.Remove(ctx, inst.BotID, inst.WorkspaceTargetID, ref.DependencyID, logSink(sink, KindDependency, ref.DependencyID)); err != nil {
				return record(step, fmt.Errorf("apps: remove dependency %s: %w", ref.DependencyID, err))
			}
			step.Status = StepRemoved
		}
		if err := s.store.RemoveDependencyRef(ctx, inst.ID, ref.DependencyID); err != nil {
			return record(step, fmt.Errorf("apps: drop dependency reference %s: %w", ref.DependencyID, err))
		}
		_ = record(step, nil)
	}
	for _, ref := range conns {
		step := StepResult{Kind: KindConnector, ID: ref.ConnectorType, Status: StepKept}
		if ref.ConnectionID != "" && !connectionReferencedByOthers(botRefs, ref.ConnectionID, inst.ID) {
			sink.Send(Event{Type: EventStep, Kind: KindConnector, ID: ref.ConnectorType})
			if err := s.connectors.Delete(ctx, inst.BotID, ref.ConnectionID); err != nil && !isNotFound(err) {
				return record(step, fmt.Errorf("apps: disconnect %s: %w", ref.ConnectorType, err))
			}
			step.Status = StepDisconnected
		}
		if err := s.store.RemoveConnectorRef(ctx, inst.ID, ref.ConnectorType); err != nil {
			return record(step, fmt.Errorf("apps: drop connector reference %s: %w", ref.ConnectorType, err))
		}
		_ = record(step, nil)
	}
	return nil
}

// Only unshared dependencies need a workspace probe. A stopped workspace,
// failed discovery or unknown dependency must not erase recovery information.
func (s *Service) cleanupDependencyStates(ctx context.Context, inst Installation, deps []DependencyRef, refs []TargetDependencyRef) (map[string]workspacedeps.Entry, error) {
	unshared := slices.DeleteFunc(slices.Clone(deps), func(ref DependencyRef) bool {
		return referencedByOthers(refs, ref.DependencyID, inst.ID)
	})
	if len(unshared) == 0 {
		return nil, nil
	}
	if s.dependencies == nil {
		return nil, ErrDependenciesUnavailable
	}
	view, err := s.dependencies.List(ctx, inst.BotID, inst.WorkspaceTargetID)
	if err != nil {
		return nil, fmt.Errorf("apps: inspect dependencies before cleanup: %w", err)
	}
	if view.Workspace != workspacedeps.WorkspaceRunning || view.DiscoveryError != "" {
		return nil, fmt.Errorf("apps: dependency cleanup requires successful workspace discovery (state %s): %s", view.Workspace, view.DiscoveryError)
	}
	states := indexEntries(view)
	for _, ref := range unshared {
		entry, known := states[ref.DependencyID]
		if !known || entry.Status.InProgress() {
			return nil, fmt.Errorf("apps: dependency %s is unknown or busy; retain its cleanup reference", ref.DependencyID)
		}
	}
	return states, nil
}
