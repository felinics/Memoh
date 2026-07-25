package fetch

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	fetchport "github.com/memohai/memoh/domains/model/internal/port/fetch"
	dbsqlc "github.com/memohai/memoh/domains/model/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type legacyQueries interface {
	CreateFetchProvider(context.Context, dbsqlc.CreateFetchProviderParams) (dbsqlc.ModelFetchProvider, error)
	GetFetchProviderByID(context.Context, pgtype.UUID) (dbsqlc.ModelFetchProvider, error)
	ListFetchProviders(context.Context) ([]dbsqlc.ModelFetchProvider, error)
	ListFetchProvidersByProvider(context.Context, string) ([]dbsqlc.ModelFetchProvider, error)
	UpdateFetchProvider(context.Context, dbsqlc.UpdateFetchProviderParams) (dbsqlc.ModelFetchProvider, error)
	DeleteFetchProvider(context.Context, pgtype.UUID) error
}

// Store adapts the current generated statements to the fetch persistence port.
type Store struct {
	queries legacyQueries
}

var _ fetchport.Store = (*Store)(nil)

// NewStore creates a postgres-backed fetch store from a connection pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{queries: dbsqlc.New(pool)}
}

// NewStoreWithQueries creates a store with an injected query surface (tests).
func NewStoreWithQueries(queries legacyQueries) *Store {
	return &Store{queries: queries}
}

func (s *Store) CreateProvider(ctx context.Context, value fetchport.ProviderWrite) (fetchport.ProviderRecord, error) {
	row, err := s.queries.CreateFetchProvider(ctx, dbsqlc.CreateFetchProviderParams{
		Name: value.Name, Provider: value.Provider, Config: value.Config, Enable: value.Enable,
	})
	if err != nil {
		return fetchport.ProviderRecord{}, err
	}
	return providerRecord(row), nil
}

func (s *Store) FindProvider(ctx context.Context, id string) (fetchport.ProviderRecord, error) {
	parsed, err := db.ParseUUID(id)
	if err != nil {
		return fetchport.ProviderRecord{}, err
	}
	row, err := s.queries.GetFetchProviderByID(ctx, parsed)
	if err != nil {
		return fetchport.ProviderRecord{}, err
	}
	return providerRecord(row), nil
}

func (s *Store) ListProviders(ctx context.Context, provider string) ([]fetchport.ProviderRecord, error) {
	var (
		rows []dbsqlc.ModelFetchProvider
		err  error
	)
	if provider == "" {
		rows, err = s.queries.ListFetchProviders(ctx)
	} else {
		rows, err = s.queries.ListFetchProvidersByProvider(ctx, provider)
	}
	if err != nil {
		return nil, err
	}
	providers := make([]fetchport.ProviderRecord, 0, len(rows))
	for _, row := range rows {
		providers = append(providers, providerRecord(row))
	}
	return providers, nil
}

func (s *Store) UpdateProvider(ctx context.Context, value fetchport.ProviderWrite) (fetchport.ProviderRecord, error) {
	parsed, err := db.ParseUUID(value.ID)
	if err != nil {
		return fetchport.ProviderRecord{}, err
	}
	row, err := s.queries.UpdateFetchProvider(ctx, dbsqlc.UpdateFetchProviderParams{
		ID: parsed, Name: value.Name, Provider: value.Provider, Config: value.Config, Enable: value.Enable,
	})
	if err != nil {
		return fetchport.ProviderRecord{}, err
	}
	return providerRecord(row), nil
}

func (s *Store) DeleteProvider(ctx context.Context, id string) error {
	parsed, err := db.ParseUUID(id)
	if err != nil {
		return err
	}
	return s.queries.DeleteFetchProvider(ctx, parsed)
}

func providerRecord(row dbsqlc.ModelFetchProvider) fetchport.ProviderRecord {
	return fetchport.ProviderRecord{
		ID: row.ID.String(), Name: row.Name, Provider: row.Provider,
		Config: append([]byte(nil), row.Config...), Enable: row.Enable,
		CreatedAt: db.TimeFromPg(row.CreatedAt), UpdatedAt: db.TimeFromPg(row.UpdatedAt),
	}
}
