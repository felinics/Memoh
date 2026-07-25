package search

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	searchport "github.com/memohai/memoh/domains/model/internal/port/search"
	dbsqlc "github.com/memohai/memoh/domains/model/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type legacyQueries interface {
	CreateSearchProvider(context.Context, dbsqlc.CreateSearchProviderParams) (dbsqlc.ModelSearchProvider, error)
	GetSearchProviderByID(context.Context, pgtype.UUID) (dbsqlc.ModelSearchProvider, error)
	ListSearchProviders(context.Context) ([]dbsqlc.ModelSearchProvider, error)
	ListSearchProvidersByProvider(context.Context, string) ([]dbsqlc.ModelSearchProvider, error)
	UpdateSearchProvider(context.Context, dbsqlc.UpdateSearchProviderParams) (dbsqlc.ModelSearchProvider, error)
	DeleteSearchProvider(context.Context, pgtype.UUID) error
}

// Store adapts the current generated statements to the search persistence port.
type Store struct {
	queries legacyQueries
}

var _ searchport.Store = (*Store)(nil)

// NewStore creates a postgres-backed search store from a connection pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{queries: dbsqlc.New(pool)}
}

// NewStoreWithQueries creates a store with an injected query surface (tests).
func NewStoreWithQueries(queries legacyQueries) *Store {
	return &Store{queries: queries}
}

func (s *Store) CreateProvider(ctx context.Context, value searchport.ProviderWrite) (searchport.ProviderRecord, error) {
	row, err := s.queries.CreateSearchProvider(ctx, dbsqlc.CreateSearchProviderParams{
		Name: value.Name, Provider: value.Provider, Config: value.Config, Enable: value.Enable,
	})
	if err != nil {
		return searchport.ProviderRecord{}, classifyWriteError(err)
	}
	return providerRecord(row), nil
}

func (s *Store) FindProvider(ctx context.Context, id string) (searchport.ProviderRecord, error) {
	parsed, err := db.ParseUUID(id)
	if err != nil {
		return searchport.ProviderRecord{}, err
	}
	row, err := s.queries.GetSearchProviderByID(ctx, parsed)
	if err != nil {
		return searchport.ProviderRecord{}, err
	}
	return providerRecord(row), nil
}

func (s *Store) ListProviders(ctx context.Context, provider string) ([]searchport.ProviderRecord, error) {
	var (
		rows []dbsqlc.ModelSearchProvider
		err  error
	)
	if provider == "" {
		rows, err = s.queries.ListSearchProviders(ctx)
	} else {
		rows, err = s.queries.ListSearchProvidersByProvider(ctx, provider)
	}
	if err != nil {
		return nil, err
	}
	providers := make([]searchport.ProviderRecord, 0, len(rows))
	for _, row := range rows {
		providers = append(providers, providerRecord(row))
	}
	return providers, nil
}

func (s *Store) UpdateProvider(ctx context.Context, value searchport.ProviderWrite) (searchport.ProviderRecord, error) {
	parsed, err := db.ParseUUID(value.ID)
	if err != nil {
		return searchport.ProviderRecord{}, err
	}
	row, err := s.queries.UpdateSearchProvider(ctx, dbsqlc.UpdateSearchProviderParams{
		ID: parsed, Name: value.Name, Provider: value.Provider, Config: value.Config, Enable: value.Enable,
	})
	if err != nil {
		return searchport.ProviderRecord{}, classifyWriteError(err)
	}
	return providerRecord(row), nil
}

func (s *Store) DeleteProvider(ctx context.Context, id string) error {
	parsed, err := db.ParseUUID(id)
	if err != nil {
		return err
	}
	return s.queries.DeleteSearchProvider(ctx, parsed)
}

func providerRecord(row dbsqlc.ModelSearchProvider) searchport.ProviderRecord {
	return searchport.ProviderRecord{
		ID: row.ID.String(), Name: row.Name, Provider: row.Provider,
		Config: append([]byte(nil), row.Config...), Enable: row.Enable,
		CreatedAt: db.TimeFromPg(row.CreatedAt), UpdatedAt: db.TimeFromPg(row.UpdatedAt),
	}
}

func classifyWriteError(err error) error {
	if !db.IsUniqueViolation(err) {
		return err
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && strings.HasSuffix(pgErr.ConstraintName, "provider_unique") {
		return fmt.Errorf("%w: %w", searchport.ErrProviderTypeConflict, err)
	}
	return fmt.Errorf("%w: %w", searchport.ErrProviderNameTaken, err)
}
