package fetch

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	fetchpostgres "github.com/memohai/memoh/domains/model/internal/postgres/fetch"
)

// NewPostgresService creates a fetch service backed by PostgreSQL via the
// owner-private adapter. cmd composition should call this constructor only.
func NewPostgresService(log *slog.Logger, pool *pgxpool.Pool) *Service {
	return NewService(log, fetchpostgres.NewStore(pool))
}

// NewMemoryService creates an in-memory fetch service for tests.
func NewMemoryService(log *slog.Logger) *Service {
	return NewService(log, newMemoryStore())
}
