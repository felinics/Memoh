package workspacedeps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/felinics/memoh/internal/supermarket"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

var (
	ErrCatalogUnavailable    = errors.New("workspace dependency catalog unavailable")
	ErrDefinitionInvalid     = errors.New("workspace dependency definition invalid")
	ErrDefinitionUnavailable = errors.New("workspace dependency definition unavailable")
)

const CatalogRefreshInterval = 10 * time.Minute

type CatalogResult struct {
	Catalog   *catalog.Catalog
	Stale     bool
	FetchedAt time.Time
}

type CatalogProvider interface {
	StoredDefinition(ctx context.Context, key DefinitionKey) (catalog.Definition, error)
	Icon(ctx context.Context, digest string) ([]byte, error)
	Snapshot(ctx context.Context, refresh bool) (CatalogResult, error)
	Cached(ctx context.Context) (CatalogResult, error)
	Definition(ctx context.Context, id, revision string) (catalog.Definition, error)
}

// StoredDefinition verifies a previously cached publication without making a
// network request. Workspace state may reference history, never authorize a
// new source or download.
func (p *RemoteCatalog) StoredDefinition(ctx context.Context, key DefinitionKey) (catalog.Definition, error) {
	if !catalog.ValidRevision(key.Revision) {
		return catalog.Definition{}, ErrDefinitionInvalid
	}
	cached, err := p.store.GetDefinition(ctx, key)
	if err != nil {
		return catalog.Definition{}, fmt.Errorf("%w: %w", ErrDefinitionUnavailable, err)
	}
	return p.validateSource(cached, key.SourceURL)
}

func (p *RemoteCatalog) Icon(ctx context.Context, digest string) ([]byte, error) {
	if !catalog.ValidRevision(digest) {
		return nil, ErrDefinitionInvalid
	}
	cached, err := p.store.FindIcon(ctx, p.sourceURL, digest)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDependencyNotFound, err)
	}
	definition, err := p.validate(cached)
	if err != nil {
		return nil, err
	}
	if definition.Dependency().IconDigest != digest {
		return nil, ErrDefinitionInvalid
	}
	return definition.Icon(), nil
}

// RemoteCatalog keeps the last validated catalog across Server restarts. Its
// mutex deduplicates refreshes locally; the store's generation CAS arbitrates
// concurrent publishers across Server replicas. Definitions are immutable.
type RemoteCatalog struct {
	client           *supermarket.Client
	store            CatalogStore
	sourceURL        string
	refreshGate      chan struct{}
	now              func() time.Time
	logger           *slog.Logger
	requiredCommands map[string]string
	refreshInterval  time.Duration
	offline          bool
	snapshotMu       sync.Mutex
	snapshotEntry    CachedCatalog
	snapshotCatalog  *catalog.Catalog
}

// Configure sets operator policy before the catalog worker starts. Offline mode
// permits persisted definitions but never contacts the configured registry.
func (p *RemoteCatalog) Configure(refreshInterval time.Duration, offline bool) {
	if refreshInterval > 0 {
		p.refreshInterval = refreshInterval
	}
	p.offline = offline
}

// SetRequiredCommands binds compiled runtime requirements before the provider
// is started. It carries command identities, not recipes or version policy.
func (p *RemoteCatalog) SetRequiredCommands(commands map[string]string) {
	p.requiredCommands = make(map[string]string, len(commands))
	for id, command := range commands {
		p.requiredCommands[id] = command
	}
}

func NewRemoteCatalog(rawURL string, store CatalogStore, client *supermarket.Client, logger *slog.Logger) (*RemoteCatalog, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, supermarket.ErrInvalidBaseURL
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	source := u.String()
	if store == nil {
		return nil, errors.New("catalog store is required")
	}
	if client == nil {
		client = supermarket.NewClient(source, nil)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &RemoteCatalog{client: client, store: store, sourceURL: source, refreshGate: make(chan struct{}, 1), now: time.Now, refreshInterval: CatalogRefreshInterval, logger: logger.With(slog.String("component", "dependency_catalog"))}, nil
}

type cachedReference struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
	Retired  bool   `json:"retired,omitempty"`
}
type cachedIndex struct {
	Entries []cachedReference `json:"entries"`
}

func (p *RemoteCatalog) Cached(ctx context.Context) (CatalogResult, error) {
	entry, err := p.store.GetCatalog(ctx, p.sourceURL)
	if errors.Is(err, ErrCatalogCacheMiss) {
		return CatalogResult{Catalog: catalog.Empty(), Stale: true}, nil
	}
	if err != nil {
		return CatalogResult{}, err
	}
	return p.hydrate(ctx, entry)
}

func (p *RemoteCatalog) Snapshot(ctx context.Context, refresh bool) (CatalogResult, error) {
	previous, err := p.store.GetCatalog(ctx, p.sourceURL)
	if err != nil && !errors.Is(err, ErrCatalogCacheMiss) {
		return CatalogResult{}, err
	}
	if p.offline {
		if errors.Is(err, ErrCatalogCacheMiss) {
			return CatalogResult{Catalog: catalog.Empty(), Stale: true}, nil
		}
		return p.hydrate(ctx, previous)
	}
	// Reads use the last validated generation immediately, including while the
	// background publisher is fetching a new catalog. Explicit refreshes may wait.
	if !refresh && len(previous.Data) > 0 {
		return p.hydrate(ctx, previous)
	}
	select {
	case p.refreshGate <- struct{}{}:
		defer func() { <-p.refreshGate }()
	case <-ctx.Done():
		return CatalogResult{}, ctx.Err()
	}
	// Another cold-cache caller may have published while we waited.
	previous, err = p.store.GetCatalog(ctx, p.sourceURL)
	if err != nil && !errors.Is(err, ErrCatalogCacheMiss) {
		return CatalogResult{}, err
	}
	if !refresh && len(previous.Data) > 0 {
		return p.hydrate(ctx, previous)
	}
	result, err := p.refresh(ctx, previous)
	if err == nil {
		return result, nil
	}
	if ctx.Err() != nil {
		return CatalogResult{}, ctx.Err()
	}
	// Invalid content remains an explicit error; only availability failures can
	// use the trusted persisted generation after a requested refresh fails.
	if errors.Is(err, ErrCatalogUnavailable) && len(previous.Data) > 0 {
		cached, cacheErr := p.hydrate(ctx, previous)
		if cacheErr != nil {
			return CatalogResult{}, cacheErr
		}
		cached.Stale = true
		p.logger.Warn("using cached dependency catalog", slog.Any("error", err))
		return cached, nil
	}
	return CatalogResult{}, err
}

// fetchIndex validates the entire paginated publication before retirement is
// computed. A truncated page or changing snapshot never retires cached entries.
func (p *RemoteCatalog) fetchIndex(ctx context.Context) ([]catalog.Descriptor, error) {
	const pageLimit = 256
	var all []catalog.Descriptor
	var total, limit int
	var revision string
	var budget int64 = catalog.MaxCatalogBytes
	seen := map[string]bool{}
	for page := 1; ; page++ {
		data, err := p.fetch(ctx, fmt.Sprintf("/api/dependencies?registry=memoh&page=%d&limit=%d", page, pageLimit), budget)
		if err != nil {
			return nil, err
		}
		budget -= int64(len(data))
		index, err := catalog.DecodeIndex(data)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrDefinitionInvalid, err)
		}
		if index.Page != page || index.Limit > pageLimit {
			return nil, fmt.Errorf("%w: unexpected catalog page", ErrDefinitionInvalid)
		}
		if page == 1 {
			total, revision, limit = index.Total, index.Revision, index.Limit
			if total > limit && revision == "" {
				return nil, fmt.Errorf("%w: paginated catalog requires a snapshot revision", ErrDefinitionInvalid)
			}
		}
		if index.Total != total || index.Revision != revision || index.Limit != limit {
			return nil, fmt.Errorf("%w: catalog changed during pagination", ErrDefinitionInvalid)
		}
		for _, descriptor := range index.Data {
			if seen[descriptor.DependencyID] {
				return nil, fmt.Errorf("%w: duplicate catalog entry across pages", ErrDefinitionInvalid)
			}
			seen[descriptor.DependencyID] = true
			all = append(all, descriptor)
		}
		if len(all) == total {
			return all, nil
		}
		if budget <= 0 {
			return nil, fmt.Errorf("%w: catalog exceeds byte budget", ErrDefinitionInvalid)
		}
	}
}

func (p *RemoteCatalog) refresh(ctx context.Context, previous CachedCatalog) (CatalogResult, error) {
	descriptors, err := p.fetchIndex(ctx)
	if err != nil {
		return CatalogResult{}, err
	}
	refs := make(map[string]cachedReference)
	if len(previous.Data) > 0 {
		prior, err := decodeCachedIndex(previous.Data)
		if err != nil {
			return CatalogResult{}, err
		}
		for _, ref := range prior.Entries {
			ref.Retired = true
			refs[ref.ID] = ref
		}
	}
	for _, descriptor := range descriptors {
		if _, err := p.Definition(ctx, descriptor.DependencyID, descriptor.Revision); err != nil {
			return CatalogResult{}, err
		}
		refs[descriptor.DependencyID] = cachedReference{ID: descriptor.DependencyID, Revision: descriptor.Revision}
	}
	entries := make([]cachedReference, 0, len(refs))
	for _, ref := range refs {
		entries = append(entries, ref)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	encoded, err := json.Marshal(cachedIndex{Entries: entries})
	if err != nil {
		return CatalogResult{}, err
	}
	if len(encoded) > catalog.MaxCatalogBytes {
		return CatalogResult{}, fmt.Errorf("%w: cached catalog exceeds budget", ErrDefinitionInvalid)
	}
	updated := CachedCatalog{TeamID: previous.TeamID, SourceURL: p.sourceURL, Data: encoded, Generation: previous.Generation, FetchedAt: p.now().UTC()}
	validatedCatalog, err := p.buildCatalog(ctx, updated)
	if err != nil {
		return CatalogResult{}, err
	}
	stored, err := p.store.PutCatalog(ctx, updated)
	if err != nil {
		return CatalogResult{}, err
	}
	if !stored {
		return p.Cached(ctx)
	}
	updated.Generation++
	p.cacheSnapshot(updated, validatedCatalog)
	if pruner, ok := p.store.(interface {
		PruneDefinitions(context.Context, string) error
	}); ok {
		if err := pruner.PruneDefinitions(ctx, p.sourceURL); err != nil {
			p.logger.Warn("prune unused dependency definitions", slog.Any("error", err))
		}
	}
	return p.catalogResult(updated, validatedCatalog), nil
}

func (p *RemoteCatalog) catalogResult(entry CachedCatalog, cat *catalog.Catalog) CatalogResult {
	return CatalogResult{Catalog: cat, FetchedAt: entry.FetchedAt, Stale: p.now().Sub(entry.FetchedAt) >= p.refreshInterval}
}

func (p *RemoteCatalog) hydrate(ctx context.Context, entry CachedCatalog) (CatalogResult, error) {
	p.snapshotMu.Lock()
	cachedEntry, cachedCatalog := p.snapshotEntry, p.snapshotCatalog
	p.snapshotMu.Unlock()
	if cachedCatalog != nil && entry.TeamID == cachedEntry.TeamID && entry.SourceURL == cachedEntry.SourceURL && bytes.Equal(entry.Data, cachedEntry.Data) {
		return p.catalogResult(entry, cachedCatalog), nil
	}
	cat, err := p.buildCatalog(ctx, entry)
	if err != nil {
		return CatalogResult{}, err
	}
	p.cacheSnapshot(entry, cat)
	return p.catalogResult(entry, cat), nil
}

// Only persisted generations enter this cache. Building an unpublished refresh
// must not evict or block the previous trusted generation used by readers.
func (p *RemoteCatalog) cacheSnapshot(entry CachedCatalog, cat *catalog.Catalog) {
	p.snapshotMu.Lock()
	defer p.snapshotMu.Unlock()
	if p.snapshotCatalog != nil && entry.TeamID == p.snapshotEntry.TeamID && entry.SourceURL == p.snapshotEntry.SourceURL && entry.Generation < p.snapshotEntry.Generation {
		return
	}
	p.snapshotEntry = entry
	p.snapshotEntry.Data = bytes.Clone(entry.Data)
	p.snapshotCatalog = cat
}

func (p *RemoteCatalog) buildCatalog(ctx context.Context, entry CachedCatalog) (*catalog.Catalog, error) {
	index, err := decodeCachedIndex(entry.Data)
	if err != nil {
		return nil, err
	}
	definitions := make([]catalog.Definition, 0, len(index.Entries))
	for _, ref := range index.Entries {
		// Hydration is strictly offline. A missing/corrupt persisted artifact is an
		// explicit error, never a request to a different source.
		cached, err := p.store.GetDefinition(ctx, DefinitionKey{SourceURL: p.sourceURL, DependencyID: ref.ID, Revision: ref.Revision})
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrDefinitionUnavailable, err)
		}
		definition, err := p.validate(cached)
		if err != nil {
			return nil, err
		}
		definitions = append(definitions, definition.WithRetired(ref.Retired))
	}
	cat, err := catalog.New(definitions)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDefinitionInvalid, err)
	}
	return cat, nil
}

func decodeCachedIndex(data []byte) (cachedIndex, error) {
	var index cachedIndex
	if len(data) > catalog.MaxCatalogBytes {
		return index, fmt.Errorf("%w: cached index exceeds budget", ErrDefinitionInvalid)
	}
	if err := json.Unmarshal(data, &index); err != nil {
		return index, fmt.Errorf("%w: %w", ErrDefinitionInvalid, err)
	}
	seen := map[string]bool{}
	for _, ref := range index.Entries {
		if ref.ID == "" || seen[ref.ID] || !catalog.ValidRevision(ref.Revision) {
			return index, fmt.Errorf("%w: invalid cached reference", ErrDefinitionInvalid)
		}
		seen[ref.ID] = true
	}
	return index, nil
}

func (p *RemoteCatalog) Definition(ctx context.Context, id, revision string) (catalog.Definition, error) {
	if !catalog.ValidRevision(revision) {
		return catalog.Definition{}, ErrDefinitionInvalid
	}
	key := DefinitionKey{SourceURL: p.sourceURL, DependencyID: id, Revision: revision}
	cached, err := p.store.GetDefinition(ctx, key)
	if err == nil {
		return p.validate(cached)
	}
	if !errors.Is(err, ErrCatalogCacheMiss) {
		return catalog.Definition{}, err
	}
	if p.offline {
		return catalog.Definition{}, ErrDefinitionUnavailable
	}
	metadata, err := p.fetch(ctx, "/api/registries/memoh/dependencies/"+url.PathEscape(id)+"/releases/"+revision, catalog.MaxArtifactBytes)
	if err != nil {
		return catalog.Definition{}, err
	}
	release, err := catalog.DecodeRelease(metadata, revision)
	if err != nil {
		return catalog.Definition{}, fmt.Errorf("%w: %w", ErrDefinitionInvalid, err)
	}
	if release.DependencyID != id {
		return catalog.Definition{}, fmt.Errorf("%w: release identity mismatch", ErrDefinitionInvalid)
	}
	archive, err := p.client.DownloadArtifact(ctx, supermarket.ArtifactDownloadDescriptor{
		Digest: release.Artifact.Digest, Size: release.Artifact.Size, DownloadURL: "/api/artifacts/dependency/" + release.Artifact.Digest,
	})
	if err != nil {
		if supermarket.ErrorKindOf(err) == supermarket.ErrorUnavailable {
			return catalog.Definition{}, fmt.Errorf("%w: %w", ErrCatalogUnavailable, err)
		}
		return catalog.Definition{}, fmt.Errorf("%w: %w", ErrDefinitionInvalid, err)
	}
	cached = CachedDefinition{DefinitionKey: key, Metadata: metadata, Archive: archive}
	definition, err := p.validate(cached)
	if err != nil {
		return catalog.Definition{}, err
	}
	if err := p.store.PutDefinition(ctx, cached); err != nil {
		return catalog.Definition{}, err
	}
	return definition, nil
}

func (p *RemoteCatalog) validate(cached CachedDefinition) (catalog.Definition, error) {
	return p.validateSource(cached, p.sourceURL)
}

func (p *RemoteCatalog) validateSource(cached CachedDefinition, sourceURL string) (catalog.Definition, error) {
	definition, err := catalog.FromArtifact(cached.Metadata, cached.Revision, cached.Archive, sourceURL)
	if err != nil {
		return catalog.Definition{}, fmt.Errorf("%w: %w", ErrDefinitionInvalid, err)
	}
	if definition.Dependency().ID != cached.DependencyID || cached.SourceURL != sourceURL {
		return catalog.Definition{}, ErrDefinitionInvalid
	}
	dep := definition.Dependency()
	if command := p.requiredCommands[dep.ID]; command != "" && (len(dep.Provides) == 0 || dep.Provides[0] != command) {
		return catalog.Definition{}, fmt.Errorf("%w: dependency %s does not provide the runtime launcher", ErrDefinitionInvalid, dep.ID)
	}
	return definition, nil
}

func (p *RemoteCatalog) fetch(ctx context.Context, path string, budget int64) ([]byte, error) {
	response, err := p.client.Get(ctx, path, "application/json")
	if err != nil {
		if errors.Is(err, supermarket.ErrCrossOrigin) || errors.Is(err, supermarket.ErrInvalidBaseURL) || errors.Is(err, supermarket.ErrRedirectLimit) {
			return nil, fmt.Errorf("%w: %w", ErrDefinitionInvalid, err)
		}
		return nil, fmt.Errorf("%w: %w", ErrCatalogUnavailable, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusNotFound && !strings.HasPrefix(path, "/api/dependencies?") {
			return nil, ErrDefinitionUnavailable
		}
		if response.StatusCode >= http.StatusInternalServerError || response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%w: HTTP %d", ErrCatalogUnavailable, response.StatusCode)
		}
		return nil, fmt.Errorf("%w: HTTP %d", ErrDefinitionInvalid, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, budget+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCatalogUnavailable, err)
	}
	if int64(len(data)) > budget {
		return nil, fmt.Errorf("%w: response exceeds byte budget", ErrDefinitionInvalid)
	}
	return data, nil
}

// Start refreshes in the background without making upstream availability a
// Server startup requirement. Its returned stop function joins the worker.
func (p *RemoteCatalog) Start(ctx context.Context) func() {
	if p.offline {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(p.refreshInterval)
		defer ticker.Stop()
		for {
			runCtx, stop := context.WithTimeout(ctx, 2*time.Minute)
			_, err := p.Snapshot(runCtx, true)
			stop()
			if err != nil && ctx.Err() == nil {
				p.logger.Warn("refresh dependency catalog", slog.Any("error", err))
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}
