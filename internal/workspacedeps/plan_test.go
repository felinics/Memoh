package workspacedeps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/felinics/memoh/internal/workspace/bridge"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

// These tests run real scripts through the bridge, receipt runner, state writer,
// shim publisher and discovery. A parent must be able to invoke its prerequisite.
func requiresCatalog(t *testing.T, edges map[string][]string, failing string) *catalog.Catalog {
	t.Helper()
	files := fstest.MapFS{}
	for id, requires := range edges {
		manifest := strings.ReplaceAll(e2eFooYAML, "foo", id) + "requires: [" + strings.Join(requires, ", ") + "]\n"
		install := strings.ReplaceAll(e2eFooInstall, "foo", id)
		for _, required := range requires {
			install = required + " --version >/dev/null\n" + install
		}
		if id == failing {
			install = "exit 42\n"
		}
		files[id+"/dependency.yaml"] = &fstest.MapFile{Data: []byte(manifest)}
		files[id+"/install.sh"] = &fstest.MapFile{Data: []byte(install)}
		files[id+"/remove.sh"] = &fstest.MapFile{Data: []byte(e2eFooRemove)}
	}
	cat, err := catalog.LoadFS(files)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func realPlanFixture(t *testing.T, edges map[string][]string, failing string) *serviceFixture {
	t.Helper()
	f := newServiceFixture(t)
	cat := requiresCatalog(t, edges, failing)
	f.cat, f.svc.catalog = cat, cat
	f.svc.discover = Discover
	f.platform = Platform{OS: "darwin", Arch: "arm64", TmpDir: t.TempDir()}
	f.svc.run = func(ctx context.Context, client *bridge.Client, spec RunSpec, sink LogSink) (Result, error) {
		f.mu.Lock()
		f.runs = append(f.runs, spec)
		f.mu.Unlock()
		return Run(ctx, client, spec, sink)
	}
	return f
}

func runIDs(f *serviceFixture) []string {
	var ids []string
	for _, s := range f.runSpecs() {
		ids = append(ids, s.DepID)
	}
	return ids
}

func TestPlanChainAndDiamondUseCommittedPrerequisiteCommands(t *testing.T) {
	for _, edges := range []map[string][]string{{"req-a": {"req-b"}, "req-b": {"req-c"}, "req-c": {}}, {"req-a": {"req-b", "req-c"}, "req-b": {"req-d"}, "req-c": {"req-d"}, "req-d": {}}} {
		f := realPlanFixture(t, edges, "")
		roots := []PlanRoot{{DependencyID: "req-a", Action: catalog.ActionInstall, Ensure: true}}
		plan, err := f.svc.PreparePlan(f.ctx(), testBot, roots)
		if err != nil {
			t.Fatal(err)
		}
		result, err := f.svc.ExecutePlan(f.ctx(), testBot, plan.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		order, _ := planOrder(f.cat, roots)
		if !slices.Equal(runIDs(f), order) {
			t.Fatalf("order %v != %v", runIDs(f), order)
		}
		for _, node := range result.Nodes {
			if node.Status != "installed" {
				t.Fatalf("%+v", node)
			}
		}
		graph, _ := f.store.ReadGraph(f.ctx(), testBot)
		for id, requires := range edges {
			if !slices.Equal(graph[id].Requires, requires) || !graph[id].Known {
				t.Fatalf("uncommitted edges for %s: %+v", id, graph)
			}
		}
		if _, err := f.svc.ExecutePlan(f.ctx(), testBot, plan.ID, nil); err != nil {
			t.Fatal(err)
		}
		if len(f.runSpecs()) != len(edges) {
			t.Fatal("retry reinstalled completed nodes")
		}
		child := edges["req-a"][0]
		if _, err := f.svc.Remove(f.ctx(), testBot, child, nil); !errors.Is(err, ErrDependencyReferenced) {
			t.Fatalf("removed shared prerequisite: %v", err)
		}
		if _, err := f.svc.Remove(f.ctx(), testBot, "req-a", nil); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(f.shimPath(child)); err != nil {
			t.Fatal("parent removal deleted prerequisite", err)
		}
	}
}

func TestPlanPreflightsEveryBranchBeforeScripts(t *testing.T) {
	f := newServiceFixture(t)
	_, err := f.svc.PreparePlan(f.ctx(), testBot, []PlanRoot{{DependencyID: "agent-x", Action: catalog.ActionInstall}, {DependencyID: "mac-only", Action: catalog.ActionInstall}})
	if !errors.Is(err, ErrPlatformUnsupported) || len(f.runSpecs()) != 0 {
		t.Fatalf("preflight %v, runs %v", err, runIDs(f))
	}
	f.cat = requiresCatalog(t, map[string][]string{"parent": {"child"}, "child": {}}, "")
	f.svc.catalog = f.cat
	plan, err := f.svc.PreparePlan(f.ctx(), testBot, []PlanRoot{{DependencyID: "parent", Action: catalog.ActionInstall}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Nodes) != 2 {
		t.Fatal(plan)
	}
}

func TestPlanFailedPrerequisiteBlocksOnlyItsDependentsAndProtectsSuccess(t *testing.T) {
	f := realPlanFixture(t, map[string][]string{"req-a": {"req-b"}, "req-b": {"req-c"}, "req-c": {}, "req-independent": {}}, "req-b")
	plan, err := f.svc.PreparePlan(f.ctx(), testBot, []PlanRoot{{DependencyID: "req-a", Action: catalog.ActionInstall}, {DependencyID: "req-independent", Action: catalog.ActionInstall}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.svc.ExecutePlan(f.ctx(), testBot, plan.ID, nil)
	var failed *PlanFailure
	if !errors.As(err, &failed) || failed.DependencyID != "req-b" || !slices.Equal(failed.Path, []string{"req-a", "req-b"}) {
		t.Fatalf("failure %+v %v", failed, err)
	}
	if !slices.Equal(runIDs(f), []string{"req-c", "req-b", "req-independent"}) {
		t.Fatal(runIDs(f))
	}
	if result.Nodes[2].Status != "blocked" {
		t.Fatal(result)
	}
	if _, ok := f.store.get(f.key("req-a")); ok {
		t.Fatal("failed root recorded installed")
	}
	if _, err := f.svc.Remove(f.ctx(), testBot, "req-c", nil); !errors.Is(err, ErrDependencyReferenced) {
		t.Fatal("pending parent lost protection", err)
	}
}

func TestPlanReusesFactsAndRejectsExpandedConfirmation(t *testing.T) {
	f := realPlanFixture(t, map[string][]string{"req-parent": {"req-child"}, "req-child": {}}, "")
	if _, err := f.svc.Install(f.ctx(), testBot, "req-child", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	plan, err := f.svc.PreparePlan(f.ctx(), testBot, []PlanRoot{{DependencyID: "req-parent", Action: catalog.ActionInstall}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Nodes[0].Action != "reuse" {
		t.Fatal(plan)
	}
	if err := os.RemoveAll(Home(f.dataRoot, "req-child")); err != nil {
		t.Fatal(err)
	}
	// The database still says installed, but the confirmed reuse is now missing.
	if _, err := f.svc.ExecutePlan(f.ctx(), testBot, plan.ID, nil); !errors.Is(err, ErrPlanChanged) {
		t.Fatal(err)
	}
	if len(f.runSpecs()) != 1 {
		t.Fatal("ran unconfirmed scripts")
	}
	again, err := f.svc.PreparePlan(f.ctx(), testBot, plan.Roots)
	if err != nil {
		t.Fatal(err)
	}
	if again.Nodes[0].Action != "install" {
		t.Fatal("trusted stale database fact")
	}
}

func TestPlanUpdateFailureRetainsOldRequiresAndSuccessSwitchesEdges(t *testing.T) {
	f := realPlanFixture(t, map[string][]string{"req-parent": {"req-old"}, "req-old": {}, "req-new": {}}, "")
	if _, err := f.svc.Install(f.ctx(), testBot, "req-parent", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	next := map[string][]string{"req-parent": {"req-new"}, "req-old": {}, "req-new": {}}
	f.svc.catalog = requiresCatalog(t, next, "req-parent")
	if _, err := f.svc.Update(f.ctx(), testBot, "req-parent", "2.0.0", nil); err == nil {
		t.Fatal("expected failed v2")
	}
	graph, _ := f.store.ReadGraph(f.ctx(), testBot)
	if !slices.Contains(graph["req-parent"].Requires, "req-old") || !slices.Contains(graph["req-parent"].Pending, "req-new") {
		t.Fatal(graph)
	}
	f.svc.catalog = requiresCatalog(t, next, "")
	if _, err := f.svc.Update(f.ctx(), testBot, "req-parent", "2.0.0", nil); err != nil {
		t.Fatal(err)
	}
	graph, _ = f.store.ReadGraph(f.ctx(), testBot)
	if !slices.Equal(graph["req-parent"].Requires, []string{"req-new"}) || len(graph["req-parent"].Pending) > 0 {
		t.Fatal(graph)
	}
	if state := f.readState(t, "req-parent"); state.Version != "2.0.0" || state.PreviousVersion != "1.0.0" {
		t.Fatal(state)
	}
	if _, err := os.Stat(filepath.Join(Home(f.dataRoot, "req-old"), "state.json")); err != nil {
		t.Fatal("garbage collected old requirement")
	}
}

func TestPlanConfirmationRejectsForeignBotAndDifferentAction(t *testing.T) {
	f := newServiceFixture(t)
	roots := []PlanRoot{{DependencyID: "agent-x", Action: catalog.ActionInstall}}
	plan, err := f.svc.PreparePlan(f.ctx(), testBot, roots)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ConfirmPlan(f.ctx(), "foreign", plan.ID, roots); !errors.Is(err, ErrPlanChanged) {
		t.Fatal(err)
	}
	roots[0].Action = catalog.ActionUpdate
	if _, err := f.svc.ConfirmPlan(f.ctx(), testBot, plan.ID, roots); !errors.Is(err, ErrPlanChanged) {
		t.Fatal(err)
	}
	if _, err := f.svc.Install(RequireConfirmedPlan(f.ctx()), testBot, "agent-x", "", nil); !errors.Is(err, ErrPlanChanged) {
		t.Fatal(err)
	}
}

func TestPlanFreezesEveryPublicationAcrossRequestsAndRestart(t *testing.T) {
	f := newServiceFixture(t)
	provider, definitions, remote := providerFixture(t)
	f.svc.provider = provider
	snapshot, err := provider.Snapshot(f.ctx(), true)
	if err != nil {
		t.Fatal(err)
	}
	oldRoot := snapshot.Catalog.MustGet("codex")
	// The root revision context must never be reused to load the child definition.
	plan, err := f.svc.PreparePlan(WithDefinitionRevision(f.ctx(), oldRoot.Revision), testBot, []PlanRoot{{DependencyID: "codex", Action: catalog.ActionInstall}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Nodes) != 2 || plan.Nodes[0].DependencyID != "node" || plan.Nodes[0].Revision == oldRoot.Revision {
		t.Fatal(plan)
	}
	original := map[string]string{}
	for _, node := range plan.Nodes {
		original[node.DependencyID] = node.Script.Script
	}
	remote.updateScript(t, "node", "# changed child publication\n")
	remote.updateScript(t, "codex", "# changed root publication\n")
	if _, err := provider.Snapshot(f.ctx(), true); err != nil {
		t.Fatal(err)
	}
	// A new service loads the server-persisted plan and immutable definitions;
	// neither the root's old context nor an in-process catalog is available.
	restarted := NewService(Options{Workspace: f.ws, Store: f.store, Catalog: catalog.Empty(), Provider: provider})
	_, frozen, err := restarted.frozenPlan(f.ctx(), testBot, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range plan.Nodes {
		script, _ := frozen.Script(node.DependencyID, catalog.ActionInstall)
		if WrapScript(script) != original[node.DependencyID] {
			t.Fatal("plan changed to latest", node.DependencyID)
		}
	}
	definitions.mu.Lock()
	delete(definitions.definitions, DefinitionKey{plan.Nodes[0].SourceURL, plan.Nodes[0].DependencyID, plan.Nodes[0].Revision})
	definitions.mu.Unlock()
	if _, _, err := restarted.frozenPlan(f.ctx(), testBot, plan.ID); !errors.Is(err, ErrDefinitionUnavailable) {
		t.Fatal("missing immutable definition silently replaced", err)
	}
}

func TestConcurrentPlansSharePrerequisiteWithoutDuplicateExecution(t *testing.T) {
	f := realPlanFixture(t, map[string][]string{"req-a": {"req-shared"}, "req-b": {"req-shared"}, "req-shared": {}}, "")
	second := NewService(Options{Workspace: f.ws, Store: f.store, Catalog: f.cat})
	second.probe = func(context.Context, *bridge.Client) (Platform, error) { return f.platform, nil }
	second.discover = Discover
	firstPlan, err := f.svc.PreparePlan(f.ctx(), testBot, []PlanRoot{{DependencyID: "req-a", Action: catalog.ActionInstall}})
	if err != nil {
		t.Fatal(err)
	}
	secondPlan, err := second.PreparePlan(f.ctx(), testBot, []PlanRoot{{DependencyID: "req-b", Action: catalog.ActionInstall}})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	run := f.svc.run
	f.svc.run = func(ctx context.Context, client *bridge.Client, spec RunSpec, sink LogSink) (Result, error) {
		if spec.DepID == "req-shared" {
			close(entered)
			<-release
		}
		return run(ctx, client, spec, sink)
	}
	second.run = run
	done := make(chan error, 1)
	go func() { _, err := f.svc.ExecutePlan(f.ctx(), testBot, firstPlan.ID, nil); done <- err }()
	<-entered
	_, concurrentErr := second.ExecutePlan(f.ctx(), testBot, secondPlan.ID, nil)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !errors.Is(concurrentErr, ErrBusy) {
		t.Fatal("competing Server was admitted", concurrentErr)
	}
	if _, err := second.ExecutePlan(f.ctx(), testBot, secondPlan.ID, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(runIDs(f), []string{"req-shared", "req-a", "req-b"}) {
		t.Fatal(runIDs(f))
	}
	graph, _ := f.store.ReadGraph(f.ctx(), testBot)
	if !slices.Equal(graph["req-a"].Requires, []string{"req-shared"}) || !slices.Equal(graph["req-b"].Requires, []string{"req-shared"}) {
		t.Fatal(graph)
	}
}

func TestConfirmedUpdateRetryUsesDurableCompletion(t *testing.T) {
	f := realPlanFixture(t, map[string][]string{"req-tool": {}}, "")
	if _, err := f.svc.Install(f.ctx(), testBot, "req-tool", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	plan, err := f.svc.PreparePlan(f.ctx(), testBot, []PlanRoot{{DependencyID: "req-tool", Action: catalog.ActionUpdate, Version: "2.0.0"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ExecutePlan(f.ctx(), testBot, plan.ID, nil); err != nil {
		t.Fatal(err)
	}
	discover := f.svc.discover
	f.svc.discover = func(ctx context.Context, c *bridge.Client, cat *catalog.Catalog, root string, ids []string, p Platform) (map[string]Observed, error) {
		result, err := discover(ctx, c, cat, root, ids, p)
		observed := result["req-tool"]
		observed.Version = "display-version-differs-from-package-version"
		result["req-tool"] = observed
		return result, err
	}

	if _, err := f.svc.ExecutePlan(f.ctx(), testBot, plan.ID, nil); err != nil {
		t.Fatal(err)
	}
	if len(f.runSpecs()) != 2 {
		t.Fatal("retry repeated completed update", runIDs(f))
	}
}

func TestReadOnlyCheckDoesNotInstallMissingPrerequisites(t *testing.T) {
	f := newServiceFixture(t)
	f.svc.catalog = requiresCatalog(t, map[string][]string{"req-parent": {"req-child"}, "req-child": {}}, "")
	dep := f.svc.catalog.MustGet("req-parent")
	_, err := f.svc.checkUpdate(f.ctx(), f.client, f.dataRoot, f.platform, dep, "1.0.0")
	if !errors.Is(err, ErrPrerequisiteMissing) || len(f.runSpecs()) != 0 {
		t.Fatal(err, runIDs(f))
	}
}

func TestLegacyUnknownHistoryBlocksRemovalWithoutGuessingLatestRequires(t *testing.T) {
	f := newServiceFixture(t)
	f.store.seed(Installation{BotID: testBot, DependencyID: "legacy-parent", Source: InstallationSourceManaged, Status: StatusInstalled, InstalledVersion: "1.0.0"})
	_, err := f.svc.Remove(f.ctx(), testBot, "tool-y", nil)
	if !errors.Is(err, ErrDependencyReferenced) || len(f.runSpecs()) != 0 {
		t.Fatal(err, runIDs(f))
	}
	graph, _ := f.store.ReadGraph(f.ctx(), testBot)
	if graph["legacy-parent"].Known || !graph["legacy-parent"].Explicit {
		t.Fatal("legacy provenance was invented", graph)
	}
}

func TestRemovalPlanRejectsAReferenceAddedAfterConfirmation(t *testing.T) {
	f := realPlanFixture(t, map[string][]string{"req-parent": {"req-child"}, "req-child": {}}, "")
	if _, err := f.svc.Install(f.ctx(), testBot, "req-child", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	plan, err := f.svc.PreparePlan(f.ctx(), testBot, []PlanRoot{{DependencyID: "req-child", Action: catalog.ActionRemove}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Install(f.ctx(), testBot, "req-parent", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	before := len(f.runSpecs())
	if _, err := f.svc.ExecutePlan(f.ctx(), testBot, plan.ID, nil); !errors.Is(err, ErrDependencyReferenced) {
		t.Fatal(err)
	}
	if len(f.runSpecs()) != before {
		t.Fatal("removal ran through a new reference")
	}
}

func TestPlanCancellationBetweenNodesNeverReportsRootSuccess(t *testing.T) {
	f := realPlanFixture(t, map[string][]string{"req-parent": {"req-child"}, "req-child": {}}, "")
	plan, err := f.svc.PreparePlan(f.ctx(), testBot, []PlanRoot{{DependencyID: "req-parent", Action: catalog.ActionInstall}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(f.ctx())
	defer cancel()
	ctx = WithPlanEvents(ctx, func(event PlanEvent) {
		if event.DependencyID == "req-child" && event.Status == "installed" {
			cancel()
		}
	})
	if _, err := f.svc.ExecutePlan(ctx, testBot, plan.ID, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled root reported success", err)
	}
	if _, ok := f.store.get(f.key("req-parent")); ok {
		t.Fatal("root was recorded before its script")
	}
	if _, err := f.svc.ExecutePlan(f.ctx(), testBot, plan.ID, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(runIDs(f), []string{"req-child", "req-parent"}) {
		t.Fatal("retry repeated successful child", runIDs(f))
	}
}

func TestPlanRejectsAChangedReusedPublicationBeforeAnyScript(t *testing.T) {
	f := realPlanFixture(t, map[string][]string{"req-parent": {"req-child"}, "req-child": {}}, "")
	if _, err := f.svc.Install(f.ctx(), testBot, "req-child", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	plan, err := f.svc.PreparePlan(f.ctx(), testBot, []PlanRoot{{DependencyID: "req-parent", Action: catalog.ActionInstall}})
	if err != nil {
		t.Fatal(err)
	}
	state := f.readState(t, "req-child")
	state.DefinitionRevision = strings.Repeat("a", 64)
	f.writeState(t, "req-child", state)
	if _, err := f.svc.ExecutePlan(f.ctx(), testBot, plan.ID, nil); !errors.Is(err, ErrPlanChanged) {
		t.Fatal(err)
	}
	if !slices.Equal(runIDs(f), []string{"req-child"}) {
		t.Fatal("changed reuse executed a script", runIDs(f))
	}
}

func TestPlanStopsAllBranchesWhenAdmissionIsLost(t *testing.T) {
	f := newServiceFixture(t)
	plan, err := f.svc.PreparePlan(f.ctx(), testBot, []PlanRoot{{DependencyID: "agent-x", Action: catalog.ActionInstall}, {DependencyID: "tool-y", Action: catalog.ActionInstall}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithPlanEvents(f.ctx(), func(event PlanEvent) {
		if event.Status == "running" {
			f.store.mu.Lock()
			f.store.owners[testBot] = "new-owner"
			f.store.mu.Unlock()
		}
	})
	result, err := f.svc.ExecutePlan(ctx, testBot, plan.ID, nil)
	if !errors.Is(err, ErrBusy) || len(result.Nodes) != 1 || len(f.runSpecs()) != 0 {
		t.Fatalf("lost admission continued: %+v, %v", result, err)
	}
}
