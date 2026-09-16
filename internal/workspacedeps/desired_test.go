package workspacedeps

import (
	"errors"
	"slices"
	"testing"

	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

func TestPreparedReleaseLabelsArePreservedWithoutExpansion(t *testing.T) {
	for _, version := range []string{"1.2", "2025.2", "25.2.7.2", "4_24.2.7-0ubuntu0.24.04.4", "0.0.0+4ec6a1b09d42e10f", "1.2.3-rc.1"} {
		t.Run(version, func(t *testing.T) {
			f := newServiceFixture(t)
			prepared, err := f.svc.PrepareInstall(f.ctx(), testBot, "tool-y", catalog.ActionInstall, version)
			if err != nil || prepared.Version != version {
				t.Fatalf("version label changed or rejected: prepared=%+v err=%v", prepared, err)
			}
			if len(f.runSpecs()) != 0 || f.store.writeCount() != 0 {
				t.Fatal("exact version preparation executed a script")
			}
		})
	}
	for _, version := range []string{"latest", "stable", "nightly", "main", "canary", "^1.2.3", "~1.2.3", "1.2.*", "1.x", "1.X.0", "1.2.3-X", "1.2.3 || 2.0.0", "v1.2.3", "go1.2.3", "bun-v1.2.3"} {
		t.Run(version, func(t *testing.T) {
			f := newServiceFixture(t)
			if _, err := f.svc.PrepareInstall(f.ctx(), testBot, "tool-y", catalog.ActionInstall, version); !errors.Is(err, ErrInvalidVersion) {
				t.Fatalf("alias/range/non-normalized target accepted: %q %v", version, err)
			}
			if len(f.runSpecs()) != 0 || f.store.writeCount() != 0 {
				t.Fatal("invalid label reached execution")
			}
		})
	}
}

func TestPartialVersionCannotCommitExpandedRecipeResult(t *testing.T) {
	f := newServiceFixture(t)
	f.setRun(func(spec RunSpec) (Result, error) { return f.installResult(spec.DepID, "1.2.3"), nil })
	if _, err := f.svc.Install(f.ctx(), testBot, "tool-y", "1.2", nil); !errors.Is(err, errInvalidResult) {
		t.Fatalf("partially specified 1.2 silently committed 1.2.3: %v", err)
	}
	rec, err := f.store.Get(f.ctx(), InstallationKey{BotID: testBot, DependencyID: "tool-y"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status == StatusInstalled || rec.InstalledVersion == "1.2.3" {
		t.Fatalf("expanded target was installed: %+v", rec)
	}
}

func TestDesiredProjectionDistinguishesToolkitFromMissingManagedTarget(t *testing.T) {
	dep := catalog.Dependency{ID: "node", Provides: []string{"node"}, Scripts: catalog.Scripts{Install: "install.sh", Remove: "remove.sh"}}
	target := &DesiredInstallation{Version: "24.4.1", InstallationID: "target", PayloadPath: "/store/node/target", Entrypoints: map[string]string{"node": "/store/node/target/bin/node"}, RepairStatus: RepairReady}
	entry := Entry{Dependency: dep, Status: StatusInstalled, InstalledVersion: "24.14.0", Observed: Observed{Present: true, Source: SourceToolkit, Candidates: []Candidate{{Source: SourceToolkit, Path: "/toolkit/bin/node", Version: "24.14.0"}}}, Actions: []catalog.Action{catalog.ActionInstall}}
	projectDesired(&entry, target, true)
	if entry.Status != StatusMissing || !entry.Observed.Present || entry.InstalledVersion != "24.14.0" || !slices.Contains(entry.Actions, catalog.ActionRemove) {
		t.Fatalf("fallback hides missing target/removal: %+v", entry)
	}
	if target.RepairStatus != RepairReady || target.RepairOperationID != "" {
		t.Fatal("projection queued recovery")
	}
}
