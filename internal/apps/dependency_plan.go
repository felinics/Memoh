package apps

import (
	"context"

	"github.com/felinics/memoh/internal/workspacedeps"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

func (s *Service) acquireDependencies(ctx context.Context, botID string) (context.Context, func(), error) {
	if s.dependencies == nil {
		return ctx, func() {}, nil
	}
	return s.dependencies.AcquireGraph(ctx, botID)
}

func dependencyRoots(ids []string) []workspacedeps.PlanRoot {
	roots := make([]workspacedeps.PlanRoot, 0, len(ids))
	for _, id := range ids {
		roots = append(roots, workspacedeps.PlanRoot{DependencyID: id, Action: catalog.ActionInstall, Ensure: true})
	}
	return roots
}

func (s *Service) ensureDependencyPlan(ctx context.Context, botID string, roots []workspacedeps.PlanRoot) (context.Context, error) {
	if len(roots) == 0 {
		return ctx, nil
	}
	if s.dependencies == nil {
		return nil, ErrDependenciesUnavailable
	}
	id := workspacedeps.PlanID(ctx)
	if id == "" {
		if workspacedeps.RequiresConfirmedPlan(ctx) {
			return nil, workspacedeps.ErrPlanChanged
		}
		plan, err := s.dependencies.PreparePlan(ctx, botID, roots)
		if err != nil {
			return nil, err
		}
		id = plan.ID
	}
	return s.dependencies.ConfirmPlan(ctx, botID, id, roots)
}

func planEvents(ctx context.Context, sink EventSink) context.Context {
	return workspacedeps.WithPlanEvents(ctx, func(event workspacedeps.PlanEvent) {
		kind := EventStep
		if event.Status != "running" && event.Status != "log" {
			kind = EventStepDone
		}
		if event.Status == "reused" {
			event.Status = StepLinked
		}
		if event.Status == "blocked" {
			event.Status = StepFailed
		}
		if event.Status == "log" {
			kind = EventLog
		}
		sink.Send(Event{Type: kind, Stream: event.Stream, Data: event.Data, Kind: KindDependency, ID: event.DependencyID, Status: event.Status, Version: event.Version, Action: event.Action, RequiredBy: event.RequiredBy, Failure: event.Failure})
	})
}

type selectedUpdatesKey struct{}

func rootsForRelease(ctx context.Context, ids []string) []workspacedeps.PlanRoot {
	roots := dependencyRoots(ids)
	selected, _ := ctx.Value(selectedUpdatesKey{}).([]string)
	for i := range roots {
		for _, id := range selected {
			if id == roots[i].DependencyID {
				roots[i].Action = catalog.ActionUpdate
				roots[i].Ensure = false
			}
		}
	}
	for _, id := range selected {
		found := false
		for _, root := range roots {
			if root.DependencyID == id {
				found = true
				break
			}
		}
		if !found {
			roots = append(roots, workspacedeps.PlanRoot{DependencyID: id, Action: catalog.ActionUpdate})
		}
	}
	return roots
}
