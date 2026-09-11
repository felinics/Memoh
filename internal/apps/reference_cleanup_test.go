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

const genericPublicCause = "internal error; see the Server log"

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
	ensureErr error
	started   int
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

func (d *cleanupDeps) Refresh(ctx context.Context, bot, target string) (workspacedeps.ListResult, error) {
	return d.List(ctx, bot, target)
}

// EnsureRunning starts the injected workspace view the way the real
// service starts a stopped native container.
func (d *cleanupDeps) EnsureRunning(context.Context, string, string) error {
	if d.ensureErr != nil {
		return d.ensureErr
	}
	d.started++
	if d.view != nil {
		d.view.Workspace = workspacedeps.WorkspaceRunning
	}
	return nil
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

type cleanupFixture struct {
	*harness
	storeFaults *cleanupStore
	depFaults   *cleanupDeps
	inst        Installation
	v2          supermarket.AppDescriptor
}

// newCleanupFixture installs editor v1, which needs the node dependency and
// an authorized github connection, and publishes v2 without either.
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
		v2:          release("memoh", "editor", "b", "2", nil, nil, nil),
	}
	h.publish(f.v2)
	f.restartService()
	return f
}

func (f *cleanupFixture) restartService() {
	f.service = NewService(Options{Store: f.storeFaults, Registry: f.registry, Skills: f.publisher, Dependencies: f.depFaults, Connectors: f.connectors})
}

func (f *cleanupFixture) installation(t *testing.T) Installation {
	t.Helper()
	inst, err := f.store.GetByID(t.Context(), testBotID, f.inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	return inst
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
	inst := f.installation(t)
	if inst.Status != StatusFailed || inst.LastError == "" {
		t.Fatalf("failure must remain visible: status=%s last_error=%q", inst.Status, inst.LastError)
	}
	if strings.Contains(inst.LastError, errCleanupProbe.Error()) {
		t.Fatalf("last_error leaked the underlying cause: %q", inst.LastError)
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
	assertConnectionKept(t, f)
}

// assertConnectionKept checks that an update never revokes the bot-level
// connection: the reference goes, the authorization stays.
func assertConnectionKept(t *testing.T, f *cleanupFixture) {
	t.Helper()
	if len(f.connectors.deleted) != 0 {
		t.Fatalf("update must not disconnect connections: %v", f.connectors.deleted)
	}
	for _, conn := range f.connectors.connections {
		if conn.ConnectionID == "github-1" {
			return
		}
	}
	t.Fatalf("github-1 connection is gone: %v", f.connectors.connections)
}

func TestUpdateCleanupReadFailuresPreserveSharedResources(t *testing.T) {
	for _, failure := range []string{"dependency_refs", "connector_refs", "shared_dependencies"} {
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
			if len(f.deps.removed) != 0 {
				t.Fatal("retry deleted a dependency still used by the other App")
			}
		})
	}
}

func TestUpdateCleanupMutationFailuresRemainRetryable(t *testing.T) {
	for _, failure := range []string{"remove_dependency", "drop_dependency_ref", "drop_connector_ref"} {
		t.Run(failure, func(t *testing.T) {
			f := newCleanupFixture(t)
			if failure == "remove_dependency" {
				f.depFaults.removeErr = errCleanupProbe
			} else {
				f.storeFaults.failure = failure
			}
			rec := &recorder{}
			_, err := f.service.Update(t.Context(), testBotID, f.inst.ID, rec)
			assertCleanupFailed(t, f, rec, err)
			if !errors.Is(err, errCleanupProbe) {
				t.Fatalf("lost cause: %v", err)
			}
			for _, event := range rec.events {
				if event.Type == EventStepDone && event.Status == StepFailed && strings.Contains(event.Message, errCleanupProbe.Error()) {
					t.Fatalf("step message leaked the underlying cause: %q", event.Message)
				}
			}
			deps, _ := f.store.ListDependencyRefs(t.Context(), f.inst.ID)
			conns, _ := f.store.ListConnectorRefs(t.Context(), f.inst.ID)
			if strings.Contains(failure, "dependency") && len(deps) != 1 || len(conns) != 1 {
				t.Fatalf("lost unfinished cleanup: %v, %v", deps, conns)
			}
			f.depFaults.removeErr, f.storeFaults.failure = nil, ""
			f.restartService()
			rec = &recorder{}
			if _, err := f.service.UpdateSelection(t.Context(), testBotID, UpdateRequest{RegistryID: "memoh", AppID: "editor", Release: true}, rec); err != nil {
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
	for _, state := range []string{"query_error", "missing", "remote_offline", "discovery_error", "busy", "start_failed"} {
		t.Run(state, func(t *testing.T) {
			f := newCleanupFixture(t)
			view := f.deps.list()
			switch state {
			case "query_error":
				f.depFaults.listErr = errCleanupProbe
			case "missing":
				view.Workspace = workspacedeps.WorkspaceMissing
			case "remote_offline":
				view.Workspace = workspacedeps.WorkspaceRemoteOffline
			case "discovery_error":
				view.DiscoveryError = "probe interrupted"
			case "busy":
				view.Entries[0].Status = workspacedeps.StatusInstalling
			case "start_failed":
				view.Workspace = workspacedeps.WorkspaceNotRunning
				f.depFaults.ensureErr = errCleanupProbe
			}
			f.depFaults.view = &view
			rec := &recorder{}
			_, err := f.service.Update(t.Context(), testBotID, f.inst.ID, rec)
			assertCleanupFailed(t, f, rec, err)
			if len(f.deps.removed) != 0 || len(f.connectors.deleted) != 0 {
				t.Fatal("incomplete discovery must not authorize cleanup")
			}
			f.depFaults.view, f.depFaults.listErr, f.depFaults.ensureErr = nil, nil, nil
			f.restartService()
			rec = &recorder{}
			if _, err := f.service.Resume(t.Context(), testBotID, f.inst.ID, rec); err != nil {
				t.Fatal(err)
			}
			assertCleanupDone(t, f, rec)
		})
	}
}

func TestCleanupStartsStoppedNativeWorkspace(t *testing.T) {
	f := newCleanupFixture(t)
	view := f.deps.list()
	view.Workspace = workspacedeps.WorkspaceNotRunning
	f.depFaults.view = &view
	rec := &recorder{}
	if _, err := f.service.Update(t.Context(), testBotID, f.inst.ID, rec); err != nil {
		t.Fatalf("a stopped workspace must be started, not reported: %v", err)
	}
	assertCleanupDone(t, f, rec)
	if f.depFaults.started != 1 || strings.Join(f.deps.removed, ",") != "node" {
		t.Fatalf("started=%d removed=%v", f.depFaults.started, f.deps.removed)
	}
}

func TestCleanupDropsReferencesToDependenciesTheCatalogNoLongerLists(t *testing.T) {
	f := newCleanupFixture(t)
	view := f.deps.list()
	view.Entries = nil
	f.depFaults.view = &view
	rec := &recorder{}
	if _, err := f.service.Update(t.Context(), testBotID, f.inst.ID, rec); err != nil {
		t.Fatalf("an unlisted dependency has nothing to remove: %v", err)
	}
	assertCleanupDone(t, f, rec)
	if len(f.deps.removed) != 0 || !strings.Contains(rec.types(), "step_done:dependency:node=kept") {
		t.Fatalf("removed=%v events=%s", f.deps.removed, rec.types())
	}
}

func TestUpdateUnlinksConnectorButKeepsConnection(t *testing.T) {
	f := newCleanupFixture(t)
	rec := &recorder{}
	if _, err := f.service.Update(t.Context(), testBotID, f.inst.ID, rec); err != nil {
		t.Fatal(err)
	}
	assertCleanupDone(t, f, rec)
	if strings.Join(f.deps.removed, ",") != "node" || !strings.Contains(rec.types(), "step_done:connector:github=unlinked") {
		t.Fatalf("removed=%v events=%s", f.deps.removed, rec.types())
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
	if len(f.deps.removed) != 1 || len(f.publisher.published) != 1 {
		t.Fatal("same-release cleanup must remove old dependencies without republishing Skills")
	}
}

func TestSameRevisionUpdateOfPartialInstallationDoesNotRepublish(t *testing.T) {
	h := newHarness()
	v1 := release("memoh", "editor", "a", "1", []string{"edit"}, nil, []supermarket.AppConnectorReference{{Type: "github", Required: true}})
	h.publish(v1)
	installed, _ := h.install(t, v1)
	if installed.Installation.Status != StatusPartial {
		t.Fatalf("status = %s, want partial", installed.Installation.Status)
	}
	rec := &recorder{}
	result, err := h.service.Update(t.Context(), testBotID, installed.Installation.ID, rec)
	if err != nil || len(result.Steps) != 0 || len(h.publisher.published) != 1 {
		t.Fatalf("partial installation at the current release must be a no-op: err=%v steps=%v published=%v", err, result.Steps, h.publisher.published)
	}
	if !strings.HasSuffix(rec.types(), "done=partial") {
		t.Fatalf("events = %s", rec.types())
	}
}

func TestFailedPublicationDoesNotSkipSameRevisionRetry(t *testing.T) {
	f := newCleanupFixture(t)
	f.publisher.publishErr = errCleanupProbe
	rec := &recorder{}
	_, err := f.service.Update(t.Context(), testBotID, f.inst.ID, rec)
	assertCleanupFailed(t, f, rec, err)
	if got := f.installation(t).LastError; got != "publish Skills: "+genericPublicCause {
		t.Fatalf("last_error = %q", got)
	}
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

func TestFailedCleanupPersistsPublicMessageOnly(t *testing.T) {
	t.Run("store error is generic", func(t *testing.T) {
		f := newCleanupFixture(t)
		f.storeFaults.failure = "dependency_refs"
		_, err := f.service.Update(t.Context(), testBotID, f.inst.ID, &recorder{})
		if err == nil || !strings.Contains(err.Error(), errCleanupProbe.Error()) {
			t.Fatalf("callers keep the cause: %v", err)
		}
		if got := f.installation(t).LastError; got != "list dependency references for cleanup: "+genericPublicCause {
			t.Fatalf("last_error = %q", got)
		}
	})
	t.Run("sentinel keeps its text", func(t *testing.T) {
		f := newCleanupFixture(t)
		f.depFaults.removeErr = workspacedeps.ErrRemoteOffline
		if _, err := f.service.Update(t.Context(), testBotID, f.inst.ID, &recorder{}); !errors.Is(err, workspacedeps.ErrRemoteOffline) {
			t.Fatalf("err = %v", err)
		}
		if got := f.installation(t).LastError; got != "remove dependency node: "+workspacedeps.ErrRemoteOffline.Error() {
			t.Fatalf("last_error = %q", got)
		}
	})
}
