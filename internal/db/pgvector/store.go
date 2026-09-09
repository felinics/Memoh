package pgvector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgxvec "github.com/pgvector/pgvector-go/pgx"

	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/db"
	pgvectorsqlc "github.com/felinics/memoh/internal/db/pgvector/sqlc"
)

// Store is the shared typed connection to the optional pgvector database.
// Schema migration and vector type registration happen once when it opens.
type Store struct {
	pool    *pgxpool.Pool
	queries *pgvectorsqlc.Queries
}

func Open(ctx context.Context, logger *slog.Logger, cfg config.PGVectorConfig) (*Store, error) {
	if err := MigrateUp(logger, cfg); err != nil {
		return nil, err
	}
	poolCfg, err := pgxpool.ParseConfig(db.DSN(cfg.PostgresConfig()))
	if err != nil {
		return nil, fmt.Errorf("pgvector: parse dsn: %w", err)
	}
	poolCfg.AfterConnect = pgxvec.RegisterTypes
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("pgvector: connect: %w", err)
	}
	return &Store{
		pool:    pool,
		queries: pgvectorsqlc.New(pool),
	}, nil
}

func (s *Store) Queries() *pgvectorsqlc.Queries {
	if s == nil {
		return nil
	}
	return s.queries
}

func (s *Store) Begin(ctx context.Context) (pgx.Tx, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("pgvector: store is not open")
	}
	return s.pool.Begin(ctx)
}

func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}
