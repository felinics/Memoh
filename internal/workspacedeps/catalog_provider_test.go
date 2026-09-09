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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

type memoryCatalogStore struct {
	mu          sync.Mutex
	indexes     map[string]CachedCatalog
	definitions map[DefinitionKey]CachedDefinition
}

func newMemoryCatalogStore() *memoryCatalogStore {
	return &memoryCatalogStore{indexes: map[string]CachedCatalog{}, definitions: map[DefinitionKey]CachedDefinition{}}
}

func (m *memoryCatalogStore) GetDefinition(_ context.Context, key DefinitionKey) (CachedDefinition, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.definitions[key]
	if !ok {
		return value, ErrCatalogCacheMiss
	}
	value.Metadata = bytes.Clone(value.Metadata)
	value.Archive = bytes.Clone(value.Archive)
	return value, nil
}

func (m *memoryCatalogStore) PutDefinition(_ context.Context, value CachedDefinition) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.definitions[value.DefinitionKey]; !ok {
		value.Metadata = bytes.Clone(value.Metadata)
		value.Archive = bytes.Clone(value.Archive)
		m.definitions[value.DefinitionKey] = value
	}
	return nil
}

func (m *memoryCatalogStore) GetCatalog(_ context.Context, source string) (CachedCatalog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.indexes[source]
	if !ok {
		return value, ErrCatalogCacheMiss
	}
	value.Data = bytes.Clone(value.Data)
	return value, nil
}

func (m *memoryCatalogStore) PutCatalog(_ context.Context, value CachedCatalog) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	previous, ok := m.indexes[value.SourceURL]
	if ok && previous.Generation != value.Generation {
		return false, nil
	}
	value.Generation = previous.Generation + 1
	value.Data = bytes.Clone(value.Data)
	m.indexes[value.SourceURL] = value
	return true, nil
}

func (m *memoryCatalogStore) FindIcon(_ context.Context, source, digest string) (CachedDefinition, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, value := range m.definitions {
		release, err := catalog.DecodeRelease(value.Metadata, value.Revision)
		if err == nil && value.SourceURL == source && release.Icon != nil && release.Icon.Digest == digest {
			return value, nil
		}
	}
	return CachedDefinition{}, ErrCatalogCacheMiss
}

type catalogHTTPFixture struct {
	mu      sync.Mutex
	index   catalog.Index
	objects map[string][]byte
	status  int
	hits    atomic.Int64
	server  *httptest.Server
}

func newCatalogHTTPFixture(t *testing.T) *catalogHTTPFixture {
	t.Helper()
	root := "catalog/testdata/remote"
	raw, err := os.ReadFile(filepath.Join(root, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	index, err := catalog.DecodeIndex(raw)
	if err != nil {
		t.Fatal(err)
	}
	f := &catalogHTTPFixture{index: index, objects: map[string][]byte{}, status: http.StatusOK}
	for _, descriptor := range index.Data {
		metadata, err := os.ReadFile(filepath.Join(root, descriptor.DependencyID, "release.json"))
		if err != nil {
			t.Fatal(err)
		}
		archive, err := os.ReadFile(filepath.Join(root, descriptor.DependencyID, "artifact.tar.gz"))
		if err != nil {
			t.Fatal(err)
		}
		f.objects["/api/registries/memoh/dependencies/"+descriptor.DependencyID+"/releases/"+descriptor.Revision] = metadata
		f.objects["/api/artifacts/dependency/"+descriptor.Artifact.Digest] = archive
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.status != http.StatusOK {
			w.WriteHeader(f.status)
			return
		}
		if r.URL.Path == "/api/dependencies" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(f.index)
			return
		}
		if object, ok := f.objects[r.URL.Path]; ok {
			_, _ = w.Write(object)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func providerFixture(t *testing.T) (*RemoteCatalog, *memoryCatalogStore, *catalogHTTPFixture) {
	t.Helper()
	server := newCatalogHTTPFixture(t)
	store := newMemoryCatalogStore()
	provider, err := NewRemoteCatalog(server.server.URL, store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return provider, store, server
}

func TestRemoteCatalogPersistsVerifiedDefinitionsAndWorksOffline(t *testing.T) {
	p, store, server := providerFixture(t)
	if server.hits.Load() != 0 {
		t.Fatal("constructor reached the network")
	}
	result, err := p.Snapshot(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Catalog.List()) != 5 || result.Stale {
		t.Fatalf("catalog=%+v", result)
	}
	dep := result.Catalog.MustGet("codex")
	if dep.RegistryID != "memoh" || dep.Revision == "" || !catalog.ValidRevision(dep.IconDigest) {
		t.Fatalf("publication missing: %+v", dep)
	}
	hits := server.hits.Load()
	again, err := p.Snapshot(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	if again.Catalog.Fingerprint() != result.Catalog.Fingerprint() || server.hits.Load() != hits {
		t.Fatal("fresh cache unexpectedly fetched")
	}
	server.server.Close()
	restarted, err := NewRemoteCatalog(server.server.URL, store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	offline, err := restarted.Snapshot(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !offline.Stale || len(offline.Catalog.List()) != 5 {
		t.Fatal("offline cache was not reused")
	}
	if _, err := restarted.Definition(t.Context(), dep.ID, dep.Revision); err != nil {
		t.Fatal(err)
	}
	other, err := NewRemoteCatalog("http://other.example", store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	isolated, err := other.Cached(t.Context())
	if err != nil || len(isolated.Catalog.List()) != 0 {
		t.Fatal("catalog crossed source boundary")
	}
}

func TestRemoteCatalogNoCacheAndInvalidContentFailExplicitly(t *testing.T) {
	p, store, server := providerFixture(t)
	server.mu.Lock()
	server.status = 503
	server.mu.Unlock()
	if _, err := p.Snapshot(t.Context(), true); !errors.Is(err, ErrCatalogUnavailable) {
		t.Fatalf("unavailable: %v", err)
	}
	server.mu.Lock()
	server.status = 200
	server.mu.Unlock()
	if _, err := p.Snapshot(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	before, _ := store.GetCatalog(t.Context(), p.sourceURL)
	server.mu.Lock()
	server.index.Data[0].SchemaVersion = "invalid"
	server.mu.Unlock()
	if _, err := p.Snapshot(t.Context(), true); !errors.Is(err, ErrDefinitionInvalid) {
		t.Fatalf("invalid update was hidden by cache: %v", err)
	}
	after, _ := store.GetCatalog(t.Context(), p.sourceURL)
	if before.Generation != after.Generation || !bytes.Equal(before.Data, after.Data) {
		t.Fatal("invalid update changed trusted snapshot")
	}
}

func TestRemoteCatalogRetirementKeepsCachedManagementDefinition(t *testing.T) {
	p, _, server := providerFixture(t)
	first, err := p.Snapshot(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	old := first.Catalog.MustGet("codex")
	server.mu.Lock()
	remaining := []catalog.Descriptor{}
	for _, item := range server.index.Data {
		if item.DependencyID != "codex" {
			remaining = append(remaining, item)
		}
	}
	server.index.Data = remaining
	server.index.Total = len(remaining)
	server.mu.Unlock()
	result, err := p.Snapshot(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	retired := result.Catalog.MustGet("codex")
	if !retired.Retired || retired.Revision != old.Revision {
		t.Fatal("retired definition was lost")
	}
	svc := &Service{provider: p, catalog: catalog.Empty()}
	view, err := svc.Catalog(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range view.Items {
		if item.ID == "codex" {
			t.Fatal("retired definition offered for new installs")
		}
	}
}

func TestRemoteCatalogFreezesPreparedOperationAcrossPublication(t *testing.T) {
	p, _, server := providerFixture(t)
	svc := &Service{provider: p, catalog: catalog.Empty()}
	first, err := svc.operationCatalog(t.Context(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	old := first.MustGet("codex")
	original, _ := first.Script("codex", catalog.ActionInstall)
	server.updateScript(t, "codex", "# changed published installer\n")
	latest, err := svc.operationCatalog(t.Context(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if latest.MustGet("codex").Revision == old.Revision {
		t.Fatal("next operation did not resolve latest")
	}
	prepared, err := svc.operationCatalog(WithDefinitionRevision(t.Context(), old.Revision), "codex")
	if err != nil {
		t.Fatal(err)
	}
	script, _ := prepared.Script("codex", catalog.ActionInstall)
	if script != original {
		t.Fatal("prepared operation changed script after preview")
	}
	script, _ = first.Script("codex", catalog.ActionInstall)
	if script != original {
		t.Fatal("refresh mutated existing snapshot")
	}
}

func TestRemoteCatalogRejectsRuntimeCommandMismatch(t *testing.T) {
	p, _, _ := providerFixture(t)
	p.SetRequiredCommands(map[string]string{"codex": "different-launcher"})
	if _, err := p.Snapshot(t.Context(), true); !errors.Is(err, ErrDefinitionInvalid) {
		t.Fatalf("command mismatch: %v", err)
	}
}

func TestRemoteCatalogRefreshWaitRespectsCancellation(t *testing.T) {
	p, _, _ := providerFixture(t)
	p.refreshGate <- struct{}{}
	defer func() { <-p.refreshGate }()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := p.Snapshot(ctx, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
}

func TestCatalogStoreCompareAndSwapRejectsStaleWriter(t *testing.T) {
	store := newMemoryCatalogStore()
	entry := CachedCatalog{SourceURL: "http://catalog", Data: []byte("one"), FetchedAt: time.Now()}
	if ok, err := store.PutCatalog(t.Context(), entry); err != nil || !ok {
		t.Fatal(err)
	}
	if ok, err := store.PutCatalog(t.Context(), entry); err != nil || ok {
		t.Fatal("stale generation overwrote catalog")
	}
}

// Rebuild a publication as an independent producer, exercising the same
// archive/release wire boundary as a newly published Supermarket recipe.
func (f *catalogHTTPFixture) updateScript(t *testing.T, id, prefix string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, descriptor := range f.index.Data {
		if descriptor.DependencyID != id {
			continue
		}
		gz, err := gzip.NewReader(bytes.NewReader(f.objects["/api/artifacts/dependency/"+descriptor.Artifact.Digest]))
		if err != nil {
			t.Fatal(err)
		}
		reader := tar.NewReader(gz)
		files := map[string][]byte{}
		for {
			header, err := reader.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			files[header.Name], err = io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
		}
		_ = gz.Close()
		files["install.sh"] = append([]byte(prefix), files["install.sh"]...)
		var tarBytes bytes.Buffer
		tw := tar.NewWriter(&tarBytes)
		names := []string{}
		for name := range files {
			names = append(names, name)
		}
		sort.Strings(names)
		var total int64
		for _, name := range names {
			body := files[name]
			total += int64(len(body))
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
				t.Fatal(err)
			}
			_, _ = tw.Write(body)
		}
		_ = tw.Close()
		var compressed bytes.Buffer
		zw := gzip.NewWriter(&compressed)
		_, _ = zw.Write(tarBytes.Bytes())
		_ = zw.Close()
		hash := func(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
		release := descriptor.Release
		release.Artifact.Digest = hash(compressed.Bytes())
		release.Artifact.Size = int64(compressed.Len())
		release.Artifact.ArchiveSize = int64(tarBytes.Len())
		release.Artifact.UncompressedSize = total
		delete(files, "icon.svg")
		release.ManifestDigest = catalog.DigestFiles(files)
		metadata, err := json.Marshal(release)
		if err != nil {
			t.Fatal(err)
		}
		revision := hash(metadata)
		f.objects["/api/artifacts/dependency/"+release.Artifact.Digest] = compressed.Bytes()
		f.objects["/api/registries/memoh/dependencies/"+id+"/releases/"+revision] = metadata
		f.index.Data[i] = catalog.Descriptor{Release: release, Revision: revision}
		return
	}
	t.Fatal("fixture dependency not found")
}

func TestDiscoveryAdoptsOnlyVerifiedHistoricalPublication(t *testing.T) {
	provider, _, server := providerFixture(t)
	initial, err := provider.Snapshot(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	old := initial.Catalog.MustGet("codex")
	server.updateScript(t, "codex", "# new recipe\n")
	latest, err := provider.Snapshot(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{provider: provider}
	state := State{DependencyID: old.ID, SourceURL: old.SourceURL, RegistryID: old.RegistryID, DefinitionRevision: old.Revision, ManifestDigest: old.ManifestDigest}
	observed := Observed{Source: SourceManaged, State: &state}
	hits := server.hits.Load()
	publication := svc.observedPublication(t.Context(), latest.Catalog.MustGet("codex"), observed)
	if publication.Revision != old.Revision {
		t.Fatalf("adopted latest recipe instead of installed publication: %+v", publication)
	}
	state.ManifestDigest = "mismatched-workspace-state"
	if got := svc.observedPublication(t.Context(), old, observed); got.Revision != "" {
		t.Fatal("accepted a state whose manifest digest does not match its publication")
	}
	state.ManifestDigest = old.ManifestDigest
	state.SourceURL = "https://workspace-controlled.example"
	if got := svc.observedPublication(t.Context(), old, observed); got.Revision != "" {
		t.Fatal("accepted an unverified source from workspace state")
	}
	if server.hits.Load() != hits {
		t.Fatal("workspace state triggered a remote definition download")
	}
}
