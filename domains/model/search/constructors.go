package search

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	searchpostgres "github.com/memohai/memoh/domains/model/internal/postgres/search"
)

// NewPostgresService creates a search service backed by PostgreSQL via the
// owner-private adapter. cmd composition should call this constructor only.
func NewPostgresService(log *slog.Logger, pool *pgxpool.Pool) *Service {
	return NewService(log, searchpostgres.NewStore(pool))
}

// NewMemoryService creates an in-memory search service for tests.
func NewMemoryService(log *slog.Logger) *Service {
	return NewService(log, newMemoryStore())
}
