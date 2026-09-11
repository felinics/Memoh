package apps

import (
	"context"
	"errors"
	"slices"

	"github.com/felinics/memoh/internal/supermarket"
	"github.com/felinics/memoh/internal/workspacedeps"
)

// pruneReferences releases references absent from the published release. All
// required reads finish before cleanup starts: a failed query is not evidence
// that a shared resource is unused. Retained references let later requests
// resume cleanup after a failure or Server restart.
//
// Dependencies no other App references are removed from the workspace before
// their reference is dropped. Connector references are only unlinked: the
// bot-level connection stays authorized until the user disconnects it or
// uninstalls the last App that uses it.
//
// This snapshot does not fence concurrent reference writers across Apps or
// Servers. Resource-level deletion claims are a separate lifecycle change.
func (s *Service) pruneReferences(ctx context.Context, inst Installation, release supermarket.AppDescriptor, sink EventSink, result *OperationResult) error {
	deps, err := s.store.ListDependencyRefs(ctx, inst.ID)
	if err != nil {
		return fail("list dependency references for cleanup", err)
	}
	conns, err := s.store.ListConnectorRefs(ctx, inst.ID)
	if err != nil {
		return fail("list connector references for cleanup", err)
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
			return fail("inspect shared dependencies before cleanup", err)
		}
	}
	states, err := s.cleanupDependencyStates(ctx, inst, deps, targetRefs)
	if err != nil {
		return err
	}
	record := func(step StepResult, cause error) error {
		if cause != nil {
			step.Status, step.Error = StepFailed, publicMessage(cause)
		}
		result.Steps = append(result.Steps, step)
		sink.Send(Event{Type: EventStepDone, Kind: step.Kind, ID: step.ID, Status: step.Status, Message: step.Error})
		return cause
	}
	for _, ref := range deps {
		step := StepResult{Kind: KindDependency, ID: ref.DependencyID, Status: StepKept}
		entry, known := states[ref.DependencyID]
		switch {
		case referencedByOthers(targetRefs, ref.DependencyID, inst.ID):
			step.Error = RemovalReasonShared
		case !known || !entry.Observed.Present:
			// Not in the catalog or not in the workspace: nothing to remove.
			step.Error = RemovalReasonAbsent
		case entry.Observed.Source != workspacedeps.SourceManaged:
			step.Error = RemovalReasonImage
		default:
			sink.Send(Event{Type: EventStep, Kind: KindDependency, ID: ref.DependencyID})
			if _, err := s.dependencies.Remove(ctx, inst.BotID, inst.WorkspaceTargetID, ref.DependencyID, logSink(sink, KindDependency, ref.DependencyID)); err != nil {
				return record(step, fail("remove dependency "+ref.DependencyID, err))
			}
			step.Status = StepRemoved
		}
		if err := s.store.RemoveDependencyRef(ctx, inst.ID, ref.DependencyID); err != nil {
			return record(step, fail("drop dependency reference "+ref.DependencyID, err))
		}
		_ = record(step, nil)
	}
	for _, ref := range conns {
		step := StepResult{Kind: KindConnector, ID: ref.ConnectorType, Status: StepUnlinked}
		if err := s.store.RemoveConnectorRef(ctx, inst.ID, ref.ConnectorType); err != nil {
			return record(step, fail("drop connector reference "+ref.ConnectorType, err))
		}
		_ = record(step, nil)
	}
	return nil
}

// cleanupDependencyStates returns live workspace facts for the references
// pruneReferences may have to remove from the workspace; shared references
// need none. A stopped native workspace is started the way an installation
// starts it. A missing workspace, an offline remote target, failed discovery
// or an operation still in progress retain the references for a later retry
// instead of deciding on incomplete information.
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
		return nil, fail("inspect dependencies before cleanup", err)
	}
	if view.Workspace == workspacedeps.WorkspaceNotRunning {
		if err := s.dependencies.EnsureRunning(ctx, inst.BotID, inst.WorkspaceTargetID); err != nil {
			return nil, fail("start workspace for dependency cleanup", err)
		}
		if view, err = s.dependencies.Refresh(ctx, inst.BotID, inst.WorkspaceTargetID); err != nil {
			return nil, fail("inspect dependencies after starting the workspace", err)
		}
	}
	if view.Workspace != workspacedeps.WorkspaceRunning {
		return nil, fail("dependency cleanup needs a running workspace (state "+string(view.Workspace)+"); references are retained", nil)
	}
	if view.DiscoveryError != "" {
		return nil, fail("dependency cleanup needs successful workspace discovery; references are retained", errors.New(view.DiscoveryError))
	}
	states := indexEntries(view)
	for _, ref := range unshared {
		// A dependency the catalog no longer lists is absent from states and
		// has nothing to remove; pruneReferences drops its reference.
		if entry, known := states[ref.DependencyID]; known && entry.Status.InProgress() {
			return nil, fail("dependency "+ref.DependencyID+" has an operation in progress; its cleanup reference is retained", nil)
		}
	}
	return states, nil
}
