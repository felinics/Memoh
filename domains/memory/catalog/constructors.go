package catalog

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	providerpostgres "github.com/memohai/memoh/domains/memory/internal/postgres/provider"
)

// NewPostgresService creates a memory provider catalog service backed by PostgreSQL.
func NewPostgresService(log *slog.Logger, pool *pgxpool.Pool) *Service {
	return NewService(log, providerpostgres.NewStoreFromPool(pool))
}
