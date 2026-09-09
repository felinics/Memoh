package postgresstore

import (
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
)

type Store struct {
	*dbsqlc.Queries
	pool    *pgxpool.Pool
	queries *dbsqlc.Queries
}

func New(pool *pgxpool.Pool) (*Store, error) {
	if pool == nil {
		return nil, errors.New("postgres store requires a pgx pool")
	}
	return NewWithPool(pool, dbsqlc.New(pool)), nil
}

func NewWithQueries(queries *dbsqlc.Queries) *Store {
	return NewWithPool(nil, queries)
}

func NewWithPool(pool *pgxpool.Pool, queries *dbsqlc.Queries) *Store {
	return &Store{
		Queries: queries,
		pool:    pool,
		queries: queries,
	}
}

func (s *Store) Pool() *pgxpool.Pool {
	if s == nil {
		return nil
	}
	return s.pool
}

func (s *Store) SQLC() *dbsqlc.Queries {
	if s == nil {
		return nil
	}
	return s.queries
}
