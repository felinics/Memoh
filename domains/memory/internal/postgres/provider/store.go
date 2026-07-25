// Package provider implements Memory provider registry persistence.
package provider

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	memport "github.com/memohai/memoh/domains/memory/internal/port"
	dbsqlc "github.com/memohai/memoh/domains/memory/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type queries interface {
	CreateMemoryProvider(context.Context, dbsqlc.CreateMemoryProviderParams) (dbsqlc.MemoryMemoryProvider, error)
	GetMemoryProviderByID(context.Context, pgtype.UUID) (dbsqlc.MemoryMemoryProvider, error)
	ListMemoryProviders(context.Context) ([]dbsqlc.MemoryMemoryProvider, error)
	UpdateMemoryProvider(context.Context, dbsqlc.UpdateMemoryProviderParams) (dbsqlc.MemoryMemoryProvider, error)
	DeleteMemoryProvider(context.Context, pgtype.UUID) error
	GetDefaultMemoryProvider(context.Context) (dbsqlc.MemoryMemoryProvider, error)
}

// Store adapts owner-generated statements to the Memory provider port.
type Store struct {
	queries queries
}

var _ memport.ProviderStore = (*Store)(nil)

func NewStore(q queries) *Store {
	return &Store{queries: q}
}

// NewStoreFromPool constructs a provider store from a PostgreSQL pool.
func NewStoreFromPool(pool *pgxpool.Pool) *Store {
	return NewStore(dbsqlc.New(pool))
}

func (s *Store) CreateProvider(ctx context.Context, value memport.ProviderCreate) (memport.ProviderRecord, error) {
	row, err := s.queries.CreateMemoryProvider(ctx, dbsqlc.CreateMemoryProviderParams{
		Name:      value.Name,
		Provider:  value.Provider,
		Config:    value.Config,
		IsDefault: value.IsDefault,
	})
	if err != nil {
		return memport.ProviderRecord{}, err
	}
	return providerRecord(row), nil
}

func (s *Store) FindProvider(ctx context.Context, id string) (memport.ProviderRecord, error) {
	parsed, err := db.ParseUUID(id)
	if err != nil {
		return memport.ProviderRecord{}, err
	}
	row, err := s.queries.GetMemoryProviderByID(ctx, parsed)
	if err != nil {
		return memport.ProviderRecord{}, err
	}
	return providerRecord(row), nil
}

func (s *Store) ListProviders(ctx context.Context) ([]memport.ProviderRecord, error) {
	rows, err := s.queries.ListMemoryProviders(ctx)
	if err != nil {
		return nil, err
	}
	providers := make([]memport.ProviderRecord, 0, len(rows))
	for _, row := range rows {
		providers = append(providers, providerRecord(row))
	}
	return providers, nil
}

func (s *Store) UpdateProvider(ctx context.Context, value memport.ProviderUpdate) (memport.ProviderRecord, error) {
	parsed, err := db.ParseUUID(value.ID)
	if err != nil {
		return memport.ProviderRecord{}, err
	}
	row, err := s.queries.UpdateMemoryProvider(ctx, dbsqlc.UpdateMemoryProviderParams{
		ID:     parsed,
		Name:   value.Name,
		Config: value.Config,
	})
	if err != nil {
		return memport.ProviderRecord{}, err
	}
	return providerRecord(row), nil
}

func (s *Store) DeleteProvider(ctx context.Context, id string) error {
	parsed, err := db.ParseUUID(id)
	if err != nil {
		return err
	}
	return s.queries.DeleteMemoryProvider(ctx, parsed)
}

func (s *Store) FindDefaultProvider(ctx context.Context) (memport.ProviderRecord, error) {
	row, err := s.queries.GetDefaultMemoryProvider(ctx)
	if err != nil {
		return memport.ProviderRecord{}, err
	}
	return providerRecord(row), nil
}

func providerRecord(row dbsqlc.MemoryMemoryProvider) memport.ProviderRecord {
	return memport.ProviderRecord{
		ID:        row.ID.String(),
		Name:      row.Name,
		Provider:  row.Provider,
		Config:    append([]byte(nil), row.Config...),
		IsDefault: row.IsDefault,
		CreatedAt: db.TimeFromPg(row.CreatedAt),
		UpdatedAt: db.TimeFromPg(row.UpdatedAt),
	}
}
