package workspacedeps

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

func requiresManifest(id, requires, platforms string, scripts bool) string {
	manifest := "id: " + id + "\nname: " + id + "\ncategory: tool\n"
	if scripts {
		manifest += "source: managed\n"
	} else {
		manifest += "source: image\n"
	}
	if requires != "" {
		manifest += "requires: [" + requires + "]\n"
	}
	manifest += "provides: [" + id + "]\nplatforms:\n" + platforms
	if scripts {
		manifest += "scripts:\n  install: install.sh\n  remove: remove.sh\n"
	}
	return manifest
}

// requiresCatalog is a chain (top -> mid -> base, top -> base), a
// prerequisite the Linux fixture platform cannot run, and an image
// prerequisite without scripts.
func requiresCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	anywhere := "  - { os: linux, arch: [amd64, arm64], libc: glibc }\n  - { os: darwin, arch: [arm64, amd64] }\n"
	macOnly := "  - { os: darwin, arch: [arm64] }\n"
	fsys := fstest.MapFS{}
	add := func(id, requires, platforms string, scripts bool) {
		fsys[id+"/dependency.yaml"] = &fstest.MapFile{Data: []byte(requiresManifest(id, requires, platforms, scripts))}
		if scripts {
			fsys[id+"/install.sh"] = &fstest.MapFile{Data: []byte("dep_log install " + id + "\n")}
			fsys[id+"/remove.sh"] = &fstest.MapFile{Data: []byte("dep_log remove " + id + "\n")}
		}
	}
	add("base", "", anywhere, true)
	add("mid", "base", anywhere, true)
	add("top", "mid, base", anywhere, true)
	add("mac-pre", "", macOnly, true)
	add("needs-mac", "mac-pre", anywhere, true)
	add("img", "", anywhere, false)
	add("needs-img", "img", anywhere, true)
	cat, err := catalog.LoadFS(fsys)
	if err != nil {
		t.Fatalf("LoadFS: %v", err)
	}
	return cat
}

func newRequiresFixture(t *testing.T) *serviceFixture {
	t.Helper()
	f := newServiceFixture(t)
	f.cat = requiresCatalog(t)
	f.svc.catalog = f.cat
	f.setRun(func(spec RunSpec) (Result, error) {
		if spec.Action == catalog.ActionRemove {
			return Result{}, nil
		}
		return f.installResult(spec.DepID, "1.0.0"), nil
	})
	return f
}

func runOrder(f *serviceFixture) []string {
	var ids []string
	for _, spec := range f.runSpecs() {
		ids = append(ids, string(spec.Action)+":"+spec.DepID)
	}
	return ids
}

func TestInstallOrderPutsPrerequisitesFirst(t *testing.T) {
	cat := requiresCatalog(t)
	for _, tc := range []struct {
		ids  []string
		want []string
	}{
		{[]string{"top"}, []string{"base", "mid", "top"}},
		{[]string{"mid", "top"}, []string{"base", "mid", "top"}},
		{[]string{"base", "needs-img"}, []string{"base", "img", "needs-img"}},
	} {
		got, err := installOrder(cat, tc.ids)
		if err != nil {
			t.Fatalf("installOrder(%v): %v", tc.ids, err)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("installOrder(%v) = %v, want %v", tc.ids, got, tc.want)
		}
	}
	if _, err := installOrder(cat, []string{"nope"}); !errors.Is(err, ErrDependencyNotFound) {
		t.Errorf("unknown id error = %v, want ErrDependencyNotFound", err)
	}
}

func TestInstallRunsMissingPrerequisitesFirst(t *testing.T) {
	f := newRequiresFixture(t)
	sink := newRecordingSink()

	result, err := f.svc.Install(f.ctx(), testBot, "top", "", sink)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if result.DependencyID != "top" || result.Installation.Status != StatusInstalled {
		t.Errorf("result = %+v", result)
	}
	if got, want := runOrder(f), []string{"install:base", "install:mid", "install:top"}; !slices.Equal(got, want) {
		t.Fatalf("runs = %v, want %v", got, want)
	}
	for _, id := range []string{"base", "mid", "top"} {
		if rec, ok := f.store.get(f.key(id)); !ok || rec.Status != StatusInstalled {
			t.Errorf("record %s = %+v (exists %v), want installed", id, rec, ok)
		}
	}
}

func TestInstallReusesPresentPrerequisites(t *testing.T) {
	f := newRequiresFixture(t)
	f.present("base", SourceToolkit, "0.9.0", nil)

	if _, err := f.svc.Install(f.ctx(), testBot, "top", "", nil); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got, want := runOrder(f), []string{"install:mid", "install:top"}; !slices.Equal(got, want) {
		t.Fatalf("runs = %v, want %v", got, want)
	}
}

func TestInstallRefusesUnusablePrerequisitesBeforeAnyScript(t *testing.T) {
	for _, tc := range []struct {
		dep  string
		want error
	}{
		{"needs-mac", ErrPlatformUnsupported},
		{"needs-img", ErrActionUnsupported},
	} {
		t.Run(tc.dep, func(t *testing.T) {
			f := newRequiresFixture(t)
			if _, err := f.svc.Install(f.ctx(), testBot, tc.dep, "", nil); !errors.Is(err, tc.want) {
				t.Fatalf("Install error = %v, want %v", err, tc.want)
			}
			if runs := runOrder(f); len(runs) != 0 {
				t.Errorf("runs = %v, want none", runs)
			}
			if _, ok := f.store.get(f.key(tc.dep)); ok {
				t.Errorf("record for %s written although nothing ran", tc.dep)
			}
		})
	}
}

func TestInstallStopsWhenPrerequisiteFails(t *testing.T) {
	f := newRequiresFixture(t)
	f.setRun(func(spec RunSpec) (Result, error) {
		if spec.DepID == "mid" {
			return Result{}, errors.New("mid install failed")
		}
		return f.installResult(spec.DepID, "1.0.0"), nil
	})

	_, err := f.svc.Install(f.ctx(), testBot, "top", "", nil)
	if err == nil {
		t.Fatal("Install succeeded although a prerequisite failed")
	}
	if got, want := runOrder(f), []string{"install:base", "install:mid"}; !slices.Equal(got, want) {
		t.Fatalf("runs = %v, want %v", got, want)
	}
	if rec, ok := f.store.get(f.key("base")); !ok || rec.Status != StatusInstalled {
		t.Errorf("installed prerequisite record = %+v, want it kept installed", rec)
	}
	if rec, ok := f.store.get(f.key("mid")); !ok || rec.Status != StatusFailed {
		t.Errorf("failed prerequisite record = %+v, want failed", rec)
	}
	if _, ok := f.store.get(f.key("top")); ok {
		t.Error("dependent record written although its prerequisite failed")
	}
}

func TestRemoveRefusesDependencyOthersRequire(t *testing.T) {
	f := newRequiresFixture(t)
	for _, id := range []string{"base", "mid", "top"} {
		f.store.seed(Installation{BotID: testBot, DependencyID: id, Source: InstallationSourceManaged, Status: StatusInstalled, InstalledVersion: "1.0.0", ManifestDigest: f.cat.MustGet(id).ManifestDigest})
		f.present(id, SourceManaged, "1.0.0", nil)
	}

	_, err := f.svc.Remove(f.ctx(), testBot, "base", nil)
	var required *RequiredError
	if !errors.As(err, &required) || !errors.Is(err, ErrRequired) {
		t.Fatalf("Remove error = %v, want RequiredError", err)
	}
	if !slices.Equal(required.Dependents, []string{"mid", "top"}) {
		t.Errorf("dependents = %v, want [mid top]", required.Dependents)
	}
	if runs := runOrder(f); len(runs) != 0 {
		t.Errorf("runs = %v, want none", runs)
	}
	if rec, _ := f.store.get(f.key("base")); rec.Status != StatusInstalled {
		t.Errorf("refused removal changed the record: %+v", rec)
	}

	if _, err := f.svc.Remove(f.ctx(), testBot, "top", nil); err != nil {
		t.Fatalf("Remove top: %v", err)
	}
	dependents, err := f.svc.Dependents(f.ctx(), testBot, "base")
	if err != nil || !slices.Equal(dependents, []string{"mid"}) {
		t.Errorf("Dependents(base) after removing top = %v, %v; want [mid]", dependents, err)
	}
}

func TestDependentsIgnoreCopiesNoScriptInstalled(t *testing.T) {
	f := newRequiresFixture(t)
	f.store.seed(Installation{BotID: testBot, DependencyID: "mid", Source: InstallationSourceImage, Status: StatusInstalled})
	f.seed("top", StatusFailed, "")
	// A PATH copy discovery adopted is recorded as managed without a digest.
	f.seed("needs-img", StatusInstalled, "1.0.0")

	for _, id := range []string{"base", "img"} {
		dependents, err := f.svc.Dependents(f.ctx(), testBot, id)
		if err != nil {
			t.Fatalf("Dependents(%s): %v", id, err)
		}
		if len(dependents) != 0 {
			t.Errorf("Dependents(%s) = %v, want none", id, dependents)
		}
	}
}

// snapshotCatalog serves one catalog the way the Supermarket provider does;
// the Service then has no static catalog of its own.
type snapshotCatalog struct {
	CatalogProvider
	cat *catalog.Catalog
}

func (p *snapshotCatalog) Snapshot(context.Context, bool) (CatalogResult, error) {
	return CatalogResult{Catalog: p.cat}, nil
}

func (p *snapshotCatalog) Cached(context.Context) (CatalogResult, error) {
	return CatalogResult{Catalog: p.cat}, nil
}

func TestInstallReusesPresentPrerequisitesFromProviderCatalog(t *testing.T) {
	f := newRequiresFixture(t)
	f.svc.provider = &snapshotCatalog{cat: f.cat}
	f.svc.catalog = catalog.Empty()
	f.present("base", SourceToolkit, "0.9.0", nil)

	if _, err := f.svc.Install(f.ctx(), testBot, "top", "", nil); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got, want := runOrder(f), []string{"install:mid", "install:top"}; !slices.Equal(got, want) {
		t.Fatalf("runs = %v, want %v", got, want)
	}
}
