package workspacedeps

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

var ErrCatalogCacheMiss = errors.New("workspace dependency catalog cache miss")

type DefinitionKey struct {
	SourceURL    string
	DependencyID string
	Revision     string
}

type CachedDefinition struct {
	DefinitionKey
	Metadata []byte
	Archive  []byte
}

type CachedCatalog struct {
	TeamID     string
	SourceURL  string
	Data       []byte
	Generation int64
	FetchedAt  time.Time
}

// CatalogStore persists verified definitions separately from installation
// intent. PostgreSQL applies the same connection-bound team RLS as Store.
type CatalogStore interface {
	FindIcon(context.Context, string, string) (CachedDefinition, error)
	GetDefinition(context.Context, DefinitionKey) (CachedDefinition, error)
	PutDefinition(context.Context, CachedDefinition) error
	GetCatalog(context.Context, string) (CachedCatalog, error)
	PutCatalog(context.Context, CachedCatalog) (bool, error)
}

func (s *postgresCatalogStore) FindIcon(ctx context.Context, source, digest string) (CachedDefinition, error) {
	row, err := s.q.FindWorkspaceDependencyIcon(ctx, dbsqlc.FindWorkspaceDependencyIconParams{SourceUrl: source, Digest: digest})
	if errors.Is(err, pgx.ErrNoRows) {
		return CachedDefinition{}, ErrCatalogCacheMiss
	}
	return CachedDefinition{DefinitionKey: DefinitionKey{SourceURL: row.SourceUrl, DependencyID: row.DependencyID, Revision: row.Revision}, Metadata: row.ReleaseBytes, Archive: row.ArtifactBytes}, err
}

type postgresCatalogStore struct{ q dbstore.Queries }

func NewPostgresCatalogStore(q dbstore.Queries) CatalogStore { return &postgresCatalogStore{q: q} }

func (s *postgresCatalogStore) GetDefinition(ctx context.Context, key DefinitionKey) (CachedDefinition, error) {
	row, err := s.q.GetWorkspaceDependencyDefinition(ctx, dbsqlc.GetWorkspaceDependencyDefinitionParams{
		SourceUrl: key.SourceURL, RegistryID: catalog.OfficialRegistry, DependencyID: key.DependencyID, Revision: key.Revision,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return CachedDefinition{}, ErrCatalogCacheMiss
	}
	return CachedDefinition{DefinitionKey: key, Metadata: row.ReleaseBytes, Archive: row.ArtifactBytes}, err
}

func (s *postgresCatalogStore) PutDefinition(ctx context.Context, entry CachedDefinition) error {
	release, err := catalog.DecodeRelease(entry.Metadata, entry.Revision)
	if err != nil {
		return err
	}
	iconDigest := ""
	if release.Icon != nil {
		iconDigest = release.Icon.Digest
	}
	_, err = s.q.CacheWorkspaceDependencyDefinition(ctx, dbsqlc.CacheWorkspaceDependencyDefinitionParams{
		SourceUrl: entry.SourceURL, RegistryID: catalog.OfficialRegistry, DependencyID: entry.DependencyID, Revision: entry.Revision,
		ReleaseBytes: entry.Metadata, ArtifactBytes: entry.Archive, IconDigest: iconDigest,
	})
	return err
}

func (s *postgresCatalogStore) GetCatalog(ctx context.Context, sourceURL string) (CachedCatalog, error) {
	row, err := s.q.GetWorkspaceDependencyCatalog(ctx, sourceURL)
	if errors.Is(err, pgx.ErrNoRows) {
		return CachedCatalog{}, ErrCatalogCacheMiss
	}
	return CachedCatalog{TeamID: uuidString(row.TeamID), SourceURL: sourceURL, Data: row.CatalogBytes, Generation: row.Generation, FetchedAt: db.TimeFromPg(row.FetchedAt)}, err
}

func (s *postgresCatalogStore) PutCatalog(ctx context.Context, entry CachedCatalog) (bool, error) {
	n, err := s.q.CacheWorkspaceDependencyCatalog(ctx, dbsqlc.CacheWorkspaceDependencyCatalogParams{
		SourceUrl: entry.SourceURL, CatalogBytes: entry.Data, ExpectedGeneration: entry.Generation,
	})
	return n == 1, err
}

func (s *postgresCatalogStore) PruneDefinitions(ctx context.Context, source string) error {
	_, err := s.q.PruneWorkspaceDependencyDefinitions(ctx, source)
	return err
}
