package apps

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/connectors"
	"github.com/felinics/memoh/internal/supermarket"
	"github.com/felinics/memoh/internal/workspacedeps"
)

var errCleanupProbe = errors.New("injected cleanup failure")

type cleanupStore struct {
	Store
	failure string
}

func (s *cleanupStore) ListDependencyRefs(ctx context.Context, id string) ([]DependencyRef, error) {
	if s.failure == "dependency_refs" {
		return nil, errCleanupProbe
	}
	return s.Store.ListDependencyRefs(ctx, id)
}

func (s *cleanupStore) ListConnectorRefs(ctx context.Context, id string) ([]ConnectorRef, error) {
	if s.failure == "connector_refs" {
		return nil, errCleanupProbe
	}
	return s.Store.ListConnectorRefs(ctx, id)
}

func (s *cleanupStore) ListTargetDependencyRefs(ctx context.Context, bot, target string) ([]TargetDependencyRef, error) {
	if s.failure == "shared_dependencies" {
		return nil, errCleanupProbe
	}
	return s.Store.ListTargetDependencyRefs(ctx, bot, target)
}

func (s *cleanupStore) ListBotConnectorRefs(ctx context.Context, bot string) ([]BotConnectorRef, error) {
	if s.failure == "shared_connectors" {
		return nil, errCleanupProbe
	}
	return s.Store.ListBotConnectorRefs(ctx, bot)
}

func (s *cleanupStore) RemoveDependencyRef(ctx context.Context, id, dep string) error {
	if s.failure == "drop_dependency_ref" {
		return errCleanupProbe
	}
	return s.Store.RemoveDependencyRef(ctx, id, dep)
}

func (s *cleanupStore) RemoveConnectorRef(ctx context.Context, id, kind string) error {
	if s.failure == "drop_connector_ref" {
		return errCleanupProbe
	}
	return s.Store.RemoveConnectorRef(ctx, id, kind)
}

type cleanupDeps struct {
	*fakeDeps
	removeErr error
	view      *workspacedeps.ListResult
	listErr   error
}

func (d *cleanupDeps) List(ctx context.Context, bot, target string) (workspacedeps.ListResult, error) {
	if d.listErr != nil {
		return workspacedeps.ListResult{}, d.listErr
	}
	if d.view != nil {
		return *d.view, nil
	}
	return d.fakeDeps.List(ctx, bot, target)
}

func (d *cleanupDeps) Remove(ctx context.Context, bot, target, dep string, sink workspacedeps.LogSink) (workspacedeps.OperationResult, error) {
	if d.removeErr != nil {
		return workspacedeps.OperationResult{}, d.removeErr
	}
	result, err := d.fakeDeps.Remove(ctx, bot, target, dep, sink)
	// Real discovery still lists the catalog entry after its copy is removed.
	d.present[dep] = absentDep(dep)
	return result, err
}

type cleanupConnectors struct {
	*fakeConnectors
	deleteErr error
}

func (c *cleanupConnectors) Delete(ctx context.Context, bot, connection string) error {
	if c.deleteErr != nil {
		return c.deleteErr
	}
	return c.fakeConnectors.Delete(ctx, bot, connection)
}

type cleanupFixture struct {
	*harness
	storeFaults *cleanupStore
	depFaults   *cleanupDeps
	connFaults  *cleanupConnectors
	inst        Installation
	v2          supermarket.AppDescriptor
}

func newCleanupFixture(t *testing.T) *cleanupFixture {
	t.Helper()
	h := newHarness()
	h.connectors.connections = []connectors.Connector{{ConnectionID: "github-1", ConnectorType: "github", Status: "active"}}
	v1 := release("memoh", "editor", "a", "1", nil, []string{"node"}, []supermarket.AppConnectorReference{{Type: "github", Required: true}})
	h.publish(v1)
	installed, _ := h.install(t, v1)
	f := &cleanupFixture{
		harness: h, inst: installed.Installation,
		storeFaults: &cleanupStore{Store: h.store},
		depFaults:   &cleanupDeps{fakeDeps: h.deps},
		connFaults:  &cleanupConnectors{fakeConnectors: h.connectors},
		v2:          release("memoh", "editor", "b", "2", nil, nil, nil),
	}
	h.publish(f.v2)
	f.restartService()
	return f
}

func (f *cleanupFixture) restartService() {
	f.service = NewService(Options{Store: f.storeFaults, Registry: f.registry, Skills: f.publisher, Dependencies: f.depFaults, Connectors: f.connFaults})
}

func assertCleanupFailed(t *testing.T, f *cleanupFixture, rec *recorder, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("cleanup failure must fail the operation")
	}
	for _, event := range rec.events {
		if event.Type == EventDone {
			t.Fatalf("failed cleanup reported done: %s", rec.types())
		}
	}
	inst, lookupErr := f.store.GetByID(t.Context(), testBotID, f.inst.ID)
	if lookupErr != nil || inst.Status != StatusFailed || inst.LastError == "" {
		t.Fatalf("failure must remain visible: status=%s last_error=%q lookup=%v", inst.Status, inst.LastError, lookupErr)
	}
}

func assertCleanupDone(t *testing.T, f *cleanupFixture, rec *recorder) {
	t.Helper()
	deps, err := f.store.ListDependencyRefs(t.Context(), f.inst.ID)
	if err != nil || len(deps) != 0 {
		t.Fatalf("pending dependencies: %v, %v", deps, err)
	}
	conns, err := f.store.ListConnectorRefs(t.Context(), f.inst.ID)
	if err != nil || len(conns) != 0 {
		t.Fatalf("pending connectors: %v, %v", conns, err)
	}
	done := 0
	for i, event := range rec.events {
		if event.Type == EventDone {
			done++
			if i != len(rec.events)-1 || event.Status != string(StatusInstalled) {
				t.Fatalf("done must follow cleanup: %s", rec.types())
			}
		}
	}
	if done != 1 {
		t.Fatalf("expected one final done: %s", rec.types())
	}
}

func TestUpdateCleanupReadFailuresPreserveSharedResources(t *testing.T) {
	for _, failure := range []string{"dependency_refs", "connector_refs", "shared_dependencies", "shared_connectors"} {
		t.Run(failure, func(t *testing.T) {
			f := newCleanupFixture(t)
			other := release("memoh", "other", "c", "1", nil, []string{"node"}, []supermarket.AppConnectorReference{{Type: "github", Required: true}})
			f.publish(other)
			f.install(t, other)
			f.storeFaults.failure = failure
			rec := &recorder{}
			_, err := f.service.Update(t.Context(), testBotID, f.inst.ID, rec)
			assertCleanupFailed(t, f, rec, err)
			if !errors.Is(err, errCleanupProbe) || len(f.deps.removed) != 0 || len(f.connectors.deleted) != 0 {
				t.Fatalf("query failure authorized deletion: %v, %v, %v", err, f.deps.removed, f.connectors.deleted)
			}
			deps, _ := f.store.ListDependencyRefs(t.Context(), f.inst.ID)
			conns, _ := f.store.ListConnectorRefs(t.Context(), f.inst.ID)
			if len(deps) != 1 || len(conns) != 1 {
				t.Fatalf("lost recovery references: %v, %v", deps, conns)
			}
			f.storeFaults.failure = ""
			f.restartService()
			rec = &recorder{}
			if _, err := f.service.Update(t.Context(), testBotID, f.inst.ID, rec); err != nil {
				t.Fatal(err)
			}
			assertCleanupDone(t, f, rec)
			if len(f.deps.removed) != 0 || len(f.connectors.deleted) != 0 {
				t.Fatal("retry deleted resources still used by the other App")
			}
		})
	}
}

func TestUpdateCleanupMutationFailuresRemainRetryable(t *testing.T) {
	for _, failure := range []string{"remove_dependency", "delete_connector", "drop_dependency_ref", "drop_connector_ref"} {
		t.Run(failure, func(t *testing.T) {
			f := newCleanupFixture(t)
			switch failure {
			case "remove_dependency":
				f.depFaults.removeErr = errCleanupProbe
			case "delete_connector":
				f.connFaults.deleteErr = errCleanupProbe
			default:
				f.storeFaults.failure = failure
			}
			rec := &recorder{}
			_, err := f.service.Update(t.Context(), testBotID, f.inst.ID, rec)
			assertCleanupFailed(t, f, rec, err)
			if !errors.Is(err, errCleanupProbe) {
				t.Fatalf("lost cause: %v", err)
			}
			deps, _ := f.store.ListDependencyRefs(t.Context(), f.inst.ID)
			conns, _ := f.store.ListConnectorRefs(t.Context(), f.inst.ID)
			if strings.Contains(failure, "dependency") && len(deps) != 1 || len(conns) != 1 {
				t.Fatalf("lost unfinished cleanup: %v, %v", deps, conns)
			}
			f.depFaults.removeErr, f.connFaults.deleteErr, f.storeFaults.failure = nil, nil, ""
			f.restartService()
			rec = &recorder{}
			if failure == "delete_connector" {
				_, err = f.service.Install(t.Context(), testBotID, InstallRequest{RegistryID: "memoh", AppID: "editor", Revision: f.v2.Revision}, rec)
			} else {
				_, err = f.service.UpdateSelection(t.Context(), testBotID, UpdateRequest{RegistryID: "memoh", AppID: "editor", Release: true}, rec)
			}
			if err != nil {
				t.Fatal(err)
			}
			assertCleanupDone(t, f, rec)
			if len(f.deps.removed) != 1 {
				t.Fatalf("retry repeated completed dependency removal: %v", f.deps.removed)
			}
		})
	}
}

func TestCleanupRejectsIncompleteDependencyDiscovery(t *testing.T) {
	for _, state := range []string{"query_error", "stopped", "discovery_error", "unknown", "busy"} {
		t.Run(state, func(t *testing.T) {
			f := newCleanupFixture(t)
			view := f.deps.list()
			switch state {
			case "query_error":
				f.depFaults.listErr = errCleanupProbe
			case "stopped":
				view.Workspace = workspacedeps.WorkspaceNotRunning
			case "discovery_error":
				view.DiscoveryError = "probe interrupted"
			case "unknown":
				view.Entries = nil
			case "busy":
				view.Entries[0].Status = workspacedeps.StatusInstalling
			}
			f.depFaults.view = &view
			rec := &recorder{}
			_, err := f.service.Update(t.Context(), testBotID, f.inst.ID, rec)
			assertCleanupFailed(t, f, rec, err)
			if len(f.deps.removed) != 0 || len(f.connectors.deleted) != 0 {
				t.Fatal("incomplete discovery must not authorize cleanup")
			}
			f.depFaults.view, f.depFaults.listErr = nil, nil
			f.restartService()
			rec = &recorder{}
			if _, err := f.service.Resume(t.Context(), testBotID, f.inst.ID, rec); err != nil {
				t.Fatal(err)
			}
			assertCleanupDone(t, f, rec)
		})
	}
}

func TestMatchingReleaseStillCleansRetainedReferences(t *testing.T) {
	f := newCleanupFixture(t)
	// Simulate an older Server that published v2 and reported installed but
	// retained v1 references. A revision-only no-op would abandon this work.
	if _, err := f.store.SetRelease(t.Context(), testBotID, f.inst.ID, f.v2.Revision, f.v2.Version, nil); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	if _, err := f.service.Update(t.Context(), testBotID, f.inst.ID, rec); err != nil {
		t.Fatal(err)
	}
	assertCleanupDone(t, f, rec)
	if len(f.deps.removed) != 1 || len(f.connectors.deleted) != 1 || len(f.publisher.published) != 1 {
		t.Fatal("same-release cleanup must remove old resources without republishing Skills")
	}
}

func TestCleanupKeepsConnectorReferencedOnAnotherTarget(t *testing.T) {
	f := newCleanupFixture(t)
	other := release("memoh", "other", "c", "1", nil, nil, []supermarket.AppConnectorReference{{Type: "github", Required: true}})
	f.publish(other)
	if _, err := f.service.Install(t.Context(), testBotID, InstallRequest{RegistryID: "memoh", AppID: "other", Revision: other.Revision, WorkspaceTargetID: "remote-1"}, nil); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	if _, err := f.service.Update(t.Context(), testBotID, f.inst.ID, rec); err != nil {
		t.Fatal(err)
	}
	assertCleanupDone(t, f, rec)
	if len(f.deps.removed) != 1 || len(f.connectors.deleted) != 0 {
		t.Fatalf("connection sharing must span targets: removed deps=%v connectors=%v", f.deps.removed, f.connectors.deleted)
	}
}

func TestFailedPublicationDoesNotSkipSameRevisionRetry(t *testing.T) {
	f := newCleanupFixture(t)
	f.publisher.publishErr = errCleanupProbe
	rec := &recorder{}
	_, err := f.service.Update(t.Context(), testBotID, f.inst.ID, rec)
	assertCleanupFailed(t, f, rec, err)
	f.publisher.publishErr = nil
	f.restartService()
	rec = &recorder{}
	if _, err := f.service.UpdateSelection(t.Context(), testBotID, UpdateRequest{RegistryID: "memoh", AppID: "editor", Release: true}, rec); err != nil {
		t.Fatal(err)
	}
	assertCleanupDone(t, f, rec)
	if len(f.publisher.published) != 2 || f.publisher.published[1] != "editor@"+f.v2.Revision {
		t.Fatalf("retry skipped publication: %v", f.publisher.published)
	}
}
