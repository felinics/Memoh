package apps

import (
	"context"
	"fmt"

	"github.com/felinics/memoh/internal/workspacedeps"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

func (*fakeDeps) AcquireGraph(ctx context.Context, _ string) (context.Context, func(), error) {
	return ctx, func() {}, nil
}

func (f *fakeDeps) PreparePlan(_ context.Context, bot string, roots []workspacedeps.PlanRoot) (workspacedeps.Plan, error) {
	p := workspacedeps.Plan{ID: fmt.Sprintf("plan-%d", len(f.plans)), BotID: bot, Roots: roots}
	if f.plans == nil {
		f.plans = map[string]workspacedeps.Plan{}
	}
	f.plans[p.ID] = p
	return p, nil
}

func (*fakeDeps) ConfirmPlan(ctx context.Context, _, id string, _ []workspacedeps.PlanRoot) (context.Context, error) {
	return workspacedeps.WithPlanID(ctx, id), nil
}

func (f *fakeDeps) ExecutePlan(ctx context.Context, bot, id string, sink workspacedeps.LogSink) (workspacedeps.PlanResult, error) {
	result := workspacedeps.PlanResult{}
	for _, root := range f.plans[id].Roots {
		node := workspacedeps.PlanNodeResult{DependencyID: root.DependencyID, Action: string(root.Action), Status: "installed"}
		entry := f.present[root.DependencyID]
		if root.Ensure && dependencyPresent(entry) {
			node.Status = "reused"
			node.Operation.Version = entry.InstalledVersion
		} else {
			var err error
			if root.Action == catalog.ActionUpdate {
				node.Operation, err = f.Update(ctx, bot, root.DependencyID, root.Version, sink)
			} else {
				node.Operation, err = f.Install(ctx, bot, root.DependencyID, root.Version, workspacedeps.LogFunc(func(stream, line string) {
					workspacedeps.SendPlanEvent(ctx, workspacedeps.PlanEvent{DependencyID: root.DependencyID, Status: "log", Stream: stream, Data: line})
				}))
			}
			if err != nil {
				node.Status = "failed"
				node.Failure = &workspacedeps.PlanFailure{DependencyID: root.DependencyID}
			}
		}
		result.Nodes = append(result.Nodes, node)
	}
	return result, nil
}

func (*fakeDeps) RemovalBlockers(context.Context, string, string, string, bool) ([]string, error) {
	return nil, nil
}
