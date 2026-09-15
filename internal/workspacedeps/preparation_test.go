package workspacedeps

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

func TestPrepareInstallExactTargetNeverStartsWorkspaceOrProvisioning(t *testing.T) {
	for _, tc := range []struct {
		name, depID, requested, want string
	}{
		{"explicit version", "tool-y", "1.2.0", "1.2.0"},
		{"recipe pin", "agent-x", "", "2.0.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newServiceFixture(t)
			f.ws.states[testBot] = WorkspaceNotRunning
			f.ws.ensureErr = errors.New("metadata preparation must not start the workspace")
			prepared, err := f.svc.PrepareInstall(f.ctx(), testBot, tc.depID, catalog.ActionInstall, tc.requested)
			if err != nil || prepared.Version != tc.want || prepared.DependencyID != tc.depID {
				t.Fatalf("prepared=%+v, error=%v", prepared, err)
			}
			if len(f.ws.ensureCalls) != 0 || len(f.runSpecs()) != 0 || f.store.writeCount() != 0 || f.discovered() != 0 {
				t.Fatalf("exact preparation touched execution: starts=%v, runs=%v, writes=%d, discoveries=%d", f.ws.ensureCalls, f.runSpecs(), f.store.writeCount(), f.discovered())
			}
		})
	}
}

func TestPrepareInstallResolvesVersionOnlyThroughCheckUpdate(t *testing.T) {
	f := newServiceFixture(t)
	f.setRun(func(spec RunSpec) (Result, error) {
		if spec.Action != catalog.ActionCheckUpdate {
			t.Fatalf("preparation ran a provision action: %+v", spec)
		}
		return Result{Raw: json.RawMessage(`{"latest":"1.2.0","update_available":true}`)}, nil
	})
	prepared, err := f.svc.PrepareInstall(f.ctx(), testBot, "tool-y", catalog.ActionUpdate, "")
	if err != nil || prepared.Version != "1.2.0" || prepared.Action != catalog.ActionUpdate {
		t.Fatalf("prepared=%+v, error=%v", prepared, err)
	}
	if specs := f.runSpecs(); len(specs) != 1 || specs[0].DepID != "tool-y" || specs[0].Action != catalog.ActionCheckUpdate {
		t.Fatalf("preparation must only check the requested dependency: %+v", specs)
	}
	if f.store.writeCount() != 0 {
		t.Fatal("preparation created an installation or authorization record")
	}
}

func TestPrepareInstallRejectsMutableUpstreamAnswer(t *testing.T) {
	for _, version := range []string{"", "latest", "nightly", "^1.2.0"} {
		t.Run(version, func(t *testing.T) {
			f := newServiceFixture(t)
			f.setRun(func(RunSpec) (Result, error) {
				payload, err := json.Marshal(map[string]string{"latest": version})
				if err != nil {
					t.Fatal(err)
				}
				return Result{Raw: payload}, nil
			})
			if _, err := f.svc.PrepareInstall(f.ctx(), testBot, "tool-y", catalog.ActionInstall, ""); !errors.Is(err, ErrInvalidVersion) {
				t.Fatalf("mutable upstream answer %q accepted: %v", version, err)
			}
			if specs := f.runSpecs(); len(specs) != 1 || specs[0].Action != catalog.ActionCheckUpdate || f.store.writeCount() != 0 {
				t.Fatalf("invalid upstream answer triggered execution: runs=%+v, writes=%d", specs, f.store.writeCount())
			}
		})
	}
}

func TestPrepareInstallRejectsInvalidTargetWithoutExecution(t *testing.T) {
	for _, tc := range []struct {
		name, depID, version string
		action               catalog.Action
		want                 error
	}{
		{"pin conflict", "agent-x", "1.9.0", catalog.ActionInstall, ErrInvalidVersion},
		{"alias", "tool-y", "latest", catalog.ActionInstall, ErrInvalidVersion},
		{"range", "tool-y", "^1.2.0", catalog.ActionInstall, ErrInvalidVersion},
		{"shell expression", "tool-y", "1.2.0;exit", catalog.ActionInstall, ErrInvalidVersion},
		{"remove action", "tool-y", "1.2.0", catalog.ActionRemove, ErrActionUnsupported},
		{"immutable image", "img-z", "1.2.0", catalog.ActionInstall, ErrActionUnsupported},
		{"unknown dependency", "not-found", "1.2.0", catalog.ActionInstall, ErrDependencyNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newServiceFixture(t)
			if _, err := f.svc.PrepareInstall(f.ctx(), testBot, tc.depID, tc.action, tc.version); !errors.Is(err, tc.want) {
				t.Fatalf("error=%v, want %v", err, tc.want)
			}
			if len(f.ws.ensureCalls) != 0 || len(f.runSpecs()) != 0 || f.store.writeCount() != 0 {
				t.Fatalf("invalid target reached execution: starts=%v, runs=%v, writes=%d", f.ws.ensureCalls, f.runSpecs(), f.store.writeCount())
			}
		})
	}
}

func TestPrepareInstallKeepsFrozenPublicationWhenRegistryChanges(t *testing.T) {
	provider, _, registry := providerFixture(t)
	snapshot, err := provider.Snapshot(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	dep := snapshot.Catalog.MustGet("codex")
	hits := registry.hits.Load()
	registry.mu.Lock()
	registry.status = http.StatusServiceUnavailable
	registry.mu.Unlock()
	f := newServiceFixture(t)
	f.svc.provider = provider
	prepared, err := f.svc.PrepareInstall(WithDefinitionRevision(f.ctx(), dep.Revision), testBot, "codex", catalog.ActionInstall, "0.151.0")
	if err != nil {
		t.Fatal(err)
	}
	if prepared.DefinitionRevision != dep.Revision || prepared.ManifestDigest != dep.ManifestDigest || prepared.SourceURL != dep.SourceURL || prepared.Version != "0.151.0" {
		t.Fatalf("frozen publication replaced during preparation: %+v", prepared)
	}
	if registry.hits.Load() != hits || len(f.runSpecs()) != 0 || f.store.writeCount() != 0 {
		t.Fatal("preparing a cached exact target contacted the registry or executed a script")
	}
}
