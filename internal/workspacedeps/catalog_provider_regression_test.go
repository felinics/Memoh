package workspacedeps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

func TestRemoteCatalogCachedReadDoesNotWaitForRefresh(t *testing.T) {
	p, _, _ := providerFixture(t)
	first, err := p.Snapshot(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	// A publisher holds the gate for the entire network refresh. Both fresh and
	// stale readers must still receive the already validated generation.
	p.refreshGate <- struct{}{}
	defer func() { <-p.refreshGate }()
	for _, age := range []time.Duration{0, 2 * CatalogRefreshInterval} {
		p.now = func() time.Time { return first.FetchedAt.Add(age) }
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		got, err := p.Snapshot(ctx, false)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if got.Catalog.MustGet("codex").Revision != first.Catalog.MustGet("codex").Revision {
			t.Fatal("reader lost cached publication")
		}
		if got.Stale != (age > 0) {
			t.Fatalf("stale=%v at age %v", got.Stale, age)
		}
	}
}

type unavailableDefinitions struct{ *memoryCatalogStore }

func (unavailableDefinitions) GetDefinition(context.Context, DefinitionKey) (CachedDefinition, error) {
	return CachedDefinition{}, errors.New("definition storage temporarily unavailable")
}

func TestRemoteCatalogReusesValidatedSnapshot(t *testing.T) {
	p, store, _ := providerFixture(t)
	first, err := p.Snapshot(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	p.store = unavailableDefinitions{store}
	got, err := p.Cached(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got.Catalog.MustGet("codex").Revision != first.Catalog.MustGet("codex").Revision {
		t.Fatal("cached definition changed")
	}
	// A provider that has not verified this generation cannot trust only the
	// index. It still needs the stored release and archive.
	restarted, err := NewRemoteCatalog(p.sourceURL, p.store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Cached(t.Context()); !errors.Is(err, ErrDefinitionUnavailable) {
		t.Fatalf("unvalidated snapshot: %v", err)
	}
}

func TestRemoteCatalogOfflineNeverRequestsRegistry(t *testing.T) {
	p, _, server := providerFixture(t)
	p.Configure(time.Hour, true)
	stop := p.Start(t.Context())
	stop()
	cached, err := p.Snapshot(t.Context(), true)
	if err != nil || len(cached.Catalog.List()) != 0 || !cached.Stale {
		t.Fatalf("offline cache miss: %+v %v", cached, err)
	}
	if _, err := p.Definition(t.Context(), "codex", strings.Repeat("a", 64)); !errors.Is(err, ErrDefinitionUnavailable) {
		t.Fatal(err)
	}
	if server.hits.Load() != 0 {
		t.Fatal("offline mode contacted registry")
	}
	p.Configure(time.Hour, false)
	first, err := p.Snapshot(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	hits := server.hits.Load()
	p.Configure(time.Hour, true)
	p.now = func() time.Time { return first.FetchedAt.Add(30 * time.Minute) }
	cached, err = p.Snapshot(t.Context(), true)
	if err != nil || cached.Stale || len(cached.Catalog.List()) != 5 {
		t.Fatalf("offline persisted cache: %+v %v", cached, err)
	}
	if server.hits.Load() != hits {
		t.Fatal("offline refresh contacted registry")
	}
}

func TestRemoteCatalogFetchesCompletePaginatedIndex(t *testing.T) {
	fixture := newCatalogHTTPFixture(t)
	descriptor := fixture.index.Data[0]
	all := make([]catalog.Descriptor, 257)
	for i := range all {
		all[i] = descriptor
		all[i].DependencyID = fmt.Sprintf("dependency-%03d", i)
	}
	for _, fault := range []string{"", "truncated", "changed-revision", "duplicate", "changed-limit", "missing-revision"} {
		t.Run(fault, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				if page < 1 || page > 2 {
					http.Error(w, "unexpected page", http.StatusBadRequest)
					return
				}
				index := catalog.Index{Total: len(all), Page: page, Limit: 256, Revision: strings.Repeat("a", 64), Data: append([]catalog.Descriptor(nil), all[(page-1)*256:min(page*256, len(all))]...)}
				if fault == "missing-revision" {
					index.Revision = ""
				}
				if page == 2 {
					switch fault {
					case "truncated":
						index.Data = nil
					case "changed-revision":
						index.Revision = strings.Repeat("b", 64)
					case "duplicate":
						index.Data[0] = all[0]
					case "changed-limit":
						index.Limit = 257
					}
				}
				_ = json.NewEncoder(w).Encode(index)
			}))
			defer server.Close()
			p, err := NewRemoteCatalog(server.URL, newMemoryCatalogStore(), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			got, err := p.fetchIndex(t.Context())
			if fault != "" {
				if !errors.Is(err, ErrDefinitionInvalid) {
					t.Fatalf("accepted invalid pagination: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 257 || got[256].DependencyID != all[256].DependencyID {
				t.Fatalf("lost second page: %d", len(got))
			}
		})
	}
}

func TestRemoteCatalogRefreshKeepsEntriesBeyondFirstPageActive(t *testing.T) {
	fixture := newCatalogHTTPFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		if r.URL.Path == "/api/dependencies" {
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			if page < 1 || page > 3 {
				http.Error(w, "invalid page", http.StatusBadRequest)
				return
			}
			index := fixture.index
			index.Page, index.Limit = page, 2
			index.Data = index.Data[(page-1)*2 : min(page*2, len(index.Data))]
			_ = json.NewEncoder(w).Encode(index)
			return
		}
		if data, ok := fixture.objects[r.URL.Path]; ok {
			_, _ = w.Write(data)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	p, err := NewRemoteCatalog(server.URL, newMemoryCatalogStore(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		result, err := p.Snapshot(t.Context(), true)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Catalog.List()) != 5 {
			t.Fatalf("incomplete catalog: %d", len(result.Catalog.List()))
		}
		if result.Catalog.MustGet("uv").Retired {
			t.Fatal("dependency on final page was retired")
		}
	}
}

type blockedDefinitionStore struct {
	*memoryCatalogStore
	key     DefinitionKey
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockedDefinitionStore) GetDefinition(ctx context.Context, key DefinitionKey) (CachedDefinition, error) {
	if key == s.key {
		s.once.Do(func() { close(s.entered) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return CachedDefinition{}, ctx.Err()
		}
	}
	return s.memoryCatalogStore.GetDefinition(ctx, key)
}

func TestRemoteCatalogReadWhileNewGenerationHydrationIsBlocked(t *testing.T) {
	p, store, server := providerFixture(t)
	first, err := p.Snapshot(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := store.GetCatalog(t.Context(), p.sourceURL)
	if err != nil {
		t.Fatal(err)
	}
	uv := first.Catalog.MustGet("uv")
	blocked := &blockedDefinitionStore{memoryCatalogStore: store, key: DefinitionKey{SourceURL: p.sourceURL, DependencyID: "uv", Revision: uv.Revision}, entered: make(chan struct{}), release: make(chan struct{})}
	p.store = blocked
	// The retired definition is read only during new-generation hydration,
	// after the live publications have been resolved by the refresh.
	server.mu.Lock()
	remaining := []catalog.Descriptor{}
	for _, entry := range server.index.Data {
		if entry.DependencyID != "uv" {
			remaining = append(remaining, entry)
		}
	}
	server.index.Data = remaining
	server.index.Total = len(remaining)
	server.mu.Unlock()
	refreshCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	refreshed := make(chan error, 1)
	go func() { _, err := p.Snapshot(refreshCtx, true); refreshed <- err }()
	select {
	case <-blocked.entered:
	case err := <-refreshed:
		t.Fatalf("refresh finished before hydration block: %v", err)
	}
	// The timeout is only a failure bound; the entered signal establishes the
	// exact interleaving, independent of the scheduler and machine speed.
	readCtx, stop := context.WithTimeout(t.Context(), 5*time.Second)
	old, err := p.Snapshot(readCtx, false)
	stop()
	close(blocked.release)
	if err != nil {
		t.Fatal(err)
	}
	if old.Catalog.MustGet("uv").Retired {
		t.Fatal("unpublished retirement became visible")
	}
	if err := <-refreshed; err != nil {
		t.Fatal(err)
	}
	// An old reader finishing after publication may return its own generation,
	// but must never replace the newer cached generation.
	if _, err := p.hydrate(t.Context(), previous); err != nil {
		t.Fatal(err)
	}
	p.store = unavailableDefinitions{store}
	current, err := p.Cached(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !current.Catalog.MustGet("uv").Retired {
		t.Fatal("old hydration replaced new cached generation")
	}
}
