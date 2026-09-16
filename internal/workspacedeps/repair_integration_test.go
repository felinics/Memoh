//go:build integration

package workspacedeps

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

type repairTestProvider struct {
	cached      *catalog.Catalog
	definitions map[string]catalog.Definition
	refuseLive  bool
	liveCalls   int
	storedCalls int
}

func (p *repairTestProvider) StoredDefinition(_ context.Context, key DefinitionKey) (catalog.Definition, error) {
	p.storedCalls++
	def, ok := p.definitions[key.Revision]
	if !ok {
		return catalog.Definition{}, ErrDefinitionUnavailable
	}
	return def, nil
}
func (p *repairTestProvider) Icon(context.Context, string) ([]byte, error) {
	return nil, ErrDependencyNotFound
}
func (p *repairTestProvider) Cached(context.Context) (CatalogResult, error) {
	return CatalogResult{Catalog: p.cached}, nil
}
func (p *repairTestProvider) Snapshot(context.Context, bool) (CatalogResult, error) {
	p.liveCalls++
	if p.refuseLive {
		return CatalogResult{}, errors.New("live recipe lookup forbidden during repair")
	}
	return CatalogResult{Catalog: p.cached}, nil
}
func (p *repairTestProvider) Definition(context.Context, string, string) (catalog.Definition, error) {
	p.liveCalls++
	return catalog.Definition{}, errors.New("live definition lookup forbidden during repair")
}

// Construct immutable publications through the real archive validator rather
// than mutating unexported catalog fields to simulate authorization.
func repairPublication(t *testing.T, extra string) catalog.Definition {
	return repairNamedPublication(t, "foo", nil, extra)
}

func repairNamedPublication(t *testing.T, id string, requires []string, extra string) catalog.Definition {
	t.Helper()
	yaml := "schema_version: '1'\n" + strings.ReplaceAll(strings.Replace(e2eFooYAML, "  remove: remove.sh", "  remove: remove.sh\n  version: version.sh", 1), "foo", id)
	if len(requires) > 0 {
		yaml += "requires: [" + strings.Join(requires, ", ") + "]\n"
	}
	files := map[string][]byte{"dependency.yaml": []byte(yaml), "install.sh": []byte(strings.ReplaceAll(isolatedFooInstall, "foo", id) + extra), "version.sh": []byte(strings.ReplaceAll(isolatedFooVersion, "foo", id)), "remove.sh": []byte("dep_result '{}'\n")}
	fsys := fstest.MapFS{}
	for name, data := range files {
		fsys[id+"/"+name] = &fstest.MapFile{Data: data}
	}
	for _, required := range requires {
		fsys[required+"/dependency.yaml"] = &fstest.MapFile{Data: []byte(strings.ReplaceAll(e2eFooYAML, "foo", required))}
		fsys[required+"/install.sh"] = &fstest.MapFile{Data: []byte(isolatedFooInstall)}
		fsys[required+"/remove.sh"] = &fstest.MapFile{Data: []byte("dep_result '{}'\n")}
	}
	cat, err := catalog.LoadFS(fsys)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(cat.MustGet(id))
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	var total int64
	for _, name := range []string{"dependency.yaml", "install.sh", "remove.sh", "version.sh"} {
		data := files[name]
		total += int64(len(data))
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(archive.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	digest := func(data []byte) string { hash := sha256.Sum256(data); return hex.EncodeToString(hash[:]) }
	release := catalog.Release{SchemaVersion: "1", RegistryID: "memoh", DependencyID: id, Manifest: manifest, ManifestDigest: catalog.DigestFiles(files), Artifact: catalog.Artifact{Format: "memoh_dependency_v1", Digest: digest(compressed.Bytes()), Size: int64(compressed.Len()), ArchiveSize: int64(archive.Len()), UncompressedSize: total, FileCount: len(files), ContentType: "application/gzip"}}
	metadata, err := json.Marshal(release)
	if err != nil {
		t.Fatal(err)
	}
	def, err := catalog.FromArtifact(metadata, digest(metadata), compressed.Bytes(), "https://frozen.example")
	if err != nil {
		t.Fatal(err)
	}
	return def
}

func TestDesiredRepairReinstallsLostRootfsWithFrozenRecipeAndPreservesHome(t *testing.T) {
	ctx := t.Context()
	pool := openDependencyPostgres(t, ctx)
	f := isolatedFixture(t)
	store := NewPostgresStore(postgresstore.NewQueries(dbsqlc.New(pool))).(*postgresStore)
	f.svc.store = store
	botID := createDependencyBot(t, ctx, pool)
	old := repairPublication(t, "")
	cat, err := catalog.New([]catalog.Definition{old})
	if err != nil {
		t.Fatal(err)
	}
	provider := &repairTestProvider{cached: cat, definitions: map[string]catalog.Definition{old.Dependency().Revision: old}}
	f.svc.provider = provider
	if _, err := f.svc.Install(WithRepairActor(ctx, "manager:alice"), botID, "foo", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	key := InstallationKey{BotID: botID, DependencyID: "foo"}
	first, err := store.GetDesired(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(f.dataRoot, ".codex", "agents", "test-agent")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	auth := filepath.Join(home, "auth.json")
	if err := os.WriteFile(auth, []byte(`{"test":"persistent"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	forbidden := filepath.Join(f.dataRoot, "wrong-publication-ran")
	newer := repairPublication(t, "touch "+shellQuote(forbidden)+"\n")
	provider.cached, err = catalog.New([]catalog.Definition{newer})
	if err != nil {
		t.Fatal(err)
	}
	provider.refuseLive = true
	provider.liveCalls = 0
	if err := os.RemoveAll(first.PayloadPath); err != nil {
		t.Fatal(err)
	}
	f.ws.reset(botID)
	// Observation-only calls may discover the missing copy but cannot run a
	// repair, even when a previously confirmed target exists.
	_, _ = f.svc.List(ctx, botID)
	if _, err := os.Stat(first.PayloadPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("list installed a payload: %v", err)
	}
	before, err := store.GetDesired(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if before.RepairStatus != RepairReady || before.RepairOperationID != "" {
		t.Fatalf("list queued a repair: %+v", before)
	}
	provider.liveCalls = 0
	if err := f.svc.EnsureDependenciesReady(ctx, botID, []string{"foo"}); err != nil {
		t.Fatal(err)
	}
	after, err := store.GetDesired(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != first.Version || after.DefinitionRevision != first.DefinitionRevision || after.Revision != first.Revision || after.AuthorizedByActor != first.AuthorizedByActor || after.InstallationID == first.InstallationID || after.RepairStatus != RepairReady {
		t.Fatalf("repair did not restore frozen target: %+v", after)
	}
	if provider.liveCalls != 0 || provider.storedCalls == 0 {
		t.Fatalf("repair used live catalog: live=%d stored=%d", provider.liveCalls, provider.storedCalls)
	}
	if _, err := os.Stat(forbidden); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new publication executed: %v", err)
	}
	if data, err := os.ReadFile(auth); err != nil || string(data) != `{"test":"persistent"}` {
		t.Fatalf("repair modified home: %s %v", data, err)
	}
	if _, err := os.Stat(after.PayloadPath); err != nil {
		t.Fatal(err)
	}
	// Intact payload + lost shim/current is a publication repair, not another
	// download or a new installation identity.
	if err := os.Remove(filepath.Join(Home(f.dataRoot, "foo"), "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(ShimDir(f.dataRoot), "foo")); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.EnsureDependenciesReady(ctx, botID, []string{"foo"}); err != nil {
		t.Fatal(err)
	}
	linked, err := store.GetDesired(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if linked.InstallationID != after.InstallationID || linked.PayloadPath != after.PayloadPath {
		t.Fatal("entrypoint repair unnecessarily reinstalled payload")
	}
}

func TestDesiredRepairWorkerDoesNotWakeStoppedBot(t *testing.T) {
	ctx := t.Context()
	pool := openDependencyPostgres(t, ctx)
	f := isolatedFixture(t)
	store := NewPostgresStore(postgresstore.NewQueries(dbsqlc.New(pool))).(*postgresStore)
	f.svc.store = store
	botID := createDependencyBot(t, ctx, pool)
	seedDesiredInstallation(t, ctx, store, InstallationKey{BotID: botID, DependencyID: "codex"}, strings.Repeat("7", 32), "0.154.0")
	f.ws.setState(botID, WorkspaceNotRunning)
	if err := f.svc.CheckRepairs(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.ws.ensureCalls) != 0 {
		t.Fatal("repair scan started stopped workspace")
	}
}

func TestDesiredRepairRestoresFrozenPrerequisiteGraphWithoutCurrentCatalog(t *testing.T) {
	for _, changed := range []string{"empty", "reversed_edges", "missing_frozen_prerequisite"} {
		t.Run(changed, func(t *testing.T) {
			ctx := t.Context()
			pool := openDependencyPostgres(t, ctx)
			f := isolatedFixture(t)
			store := NewPostgresStore(postgresstore.NewQueries(dbsqlc.New(pool))).(*postgresStore)
			f.svc.store = store
			botID := createDependencyBot(t, ctx, pool)
			trace := filepath.Join(f.dataRoot, "repair-order")
			extra := "printf '%s\\n' \"$MEMOH_DEP_ID\" >> " + shellQuote(trace) + "\n"
			base := repairNamedPublication(t, "bar", nil, extra)
			agent := repairNamedPublication(t, "foo", []string{"bar"}, extra)
			cat, err := catalog.New([]catalog.Definition{base, agent})
			if err != nil {
				t.Fatal(err)
			}
			provider := &repairTestProvider{cached: cat, definitions: map[string]catalog.Definition{base.Dependency().Revision: base, agent.Dependency().Revision: agent}}
			f.svc.provider = provider
			for _, id := range []string{"bar", "foo"} {
				if _, err := f.svc.Install(WithRepairActor(ctx, "manager:alice"), botID, id, "1.0.0", nil); err != nil {
					t.Fatal(err)
				}
			}
			targets, err := store.ListDesired(ctx, botID)
			if err != nil {
				t.Fatal(err)
			}
			for _, target := range targets {
				if err := os.RemoveAll(target.PayloadPath); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Remove(trace); err != nil {
				t.Fatal(err)
			}
			f.ws.reset(botID)
			provider.refuseLive = true
			provider.liveCalls = 0
			provider.cached = catalog.Empty()
			if changed == "reversed_edges" {
				latestBase := repairNamedPublication(t, "bar", []string{"foo"}, "exit 91\n")
				latestAgent := repairNamedPublication(t, "foo", nil, "exit 92\n")
				provider.cached, err = catalog.New([]catalog.Definition{latestBase, latestAgent})
				if err != nil {
					t.Fatal(err)
				}
			} else if changed == "missing_frozen_prerequisite" {
				delete(provider.definitions, base.Dependency().Revision)
			}
			err = f.svc.EnsureDependenciesReady(ctx, botID, []string{"foo"})
			if changed == "missing_frozen_prerequisite" {
				if !errors.Is(err, ErrDefinitionUnavailable) {
					t.Fatalf("missing immutable prerequisite was not rejected: %v", err)
				}
				if _, err := os.Stat(trace); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("executed without complete frozen prerequisite graph: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				order, err := os.ReadFile(trace)
				if err != nil || string(order) != "bar\nfoo\n" {
					t.Fatalf("frozen topology was not restored: %q %v", order, err)
				}
				for _, original := range targets {
					restored, err := store.GetDesired(ctx, original.InstallationKey)
					if err != nil || restored.Revision != original.Revision || restored.DefinitionRevision != original.DefinitionRevision || restored.RepairStatus != RepairReady || restored.InstallationID == original.InstallationID {
						t.Fatalf("target changed instead of restoring its frozen publication: %+v %v", restored, err)
					}
				}
			}
			if provider.liveCalls != 0 {
				t.Fatalf("repair made %d live catalog calls", provider.liveCalls)
			}
		})
	}
}

func TestDesiredReadinessInvalidatesPeerServerLauncherCache(t *testing.T) {
	ctx := t.Context()
	pool := openDependencyPostgres(t, ctx)
	f := isolatedFixture(t)
	store := NewPostgresStore(postgresstore.NewQueries(dbsqlc.New(pool))).(*postgresStore)
	f.svc.store = store
	botID := createDependencyBot(t, ctx, pool)
	def := repairPublication(t, "")
	cat, err := catalog.New([]catalog.Definition{def})
	if err != nil {
		t.Fatal(err)
	}
	provider := &repairTestProvider{cached: cat, definitions: map[string]catalog.Definition{def.Dependency().Revision: def}}
	f.svc.provider = provider
	if _, err := f.svc.Install(ctx, botID, "foo", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	first, err := f.svc.ResolveLauncher(ctx, botID, "foo")
	if err != nil {
		t.Fatal(err)
	}
	// A second Service owns its own process-local cache and commits a new
	// payload against the same real PostgreSQL and workspace bridge.
	peer := NewService(Options{Workspace: f.ws, Store: store, Provider: provider})
	peer.dependencyStoreRoot = f.svc.dependencyStoreRoot
	if _, err := peer.Update(ctx, botID, "foo", "2.0.0", nil); err != nil {
		t.Fatal(err)
	}
	stale, ok := f.svc.cache.Get(botID)
	if !ok || stale.Observed["foo"].State.Version != "1.0.0" {
		t.Fatal("test did not preserve the peer's stale launcher cache")
	}
	provider.refuseLive = true
	if err := f.svc.EnsureDependenciesReady(ctx, botID, []string{"foo"}); err != nil {
		t.Fatal(err)
	}
	launcher, err := f.svc.ResolveLauncher(ctx, botID, "foo")
	if err != nil {
		t.Fatal(err)
	}
	target, err := store.GetDesired(ctx, InstallationKey{BotID: botID, DependencyID: "foo"})
	if err != nil {
		t.Fatal(err)
	}
	if launcher.Version != "2.0.0" || launcher.Path == first.Path || launcher.Path != target.Entrypoints["foo"] {
		t.Fatalf("stale peer launched superseded payload: first=%+v next=%+v target=%+v", first, launcher, target)
	}
	if target.Version != "2.0.0" || target.RepairStatus != RepairReady || target.RepairAttempts != 0 {
		t.Fatalf("cache refresh altered authorization or installed again: %+v", target)
	}
	provider.refuseLive = false
	if _, err := peer.Remove(ctx, botID, "foo", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.svc.cache.Get(botID); !ok {
		t.Fatal("test did not retain removed peer's stale cache")
	}
	provider.refuseLive = true
	if err := f.svc.EnsureDependenciesReady(ctx, botID, []string{"foo"}); err != nil {
		t.Fatal(err)
	}
	if launcher, err := f.svc.ResolveLauncher(ctx, botID, "foo"); err == nil {
		t.Fatalf("peer launched a revoked payload from cache: %+v", launcher)
	}
}
