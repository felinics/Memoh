package usage

import (
	"github.com/jackc/pgx/v5/pgxpool"

	usagepersistence "github.com/memohai/memoh/domains/agent/chat/usage/persistence"
	usagepostgres "github.com/memohai/memoh/domains/agent/internal/postgres/usage"
)

// NewPostgresService constructs the Agent-owned usage reader and Model
// enrichment service backed by PostgreSQL.
func NewPostgresService(pool *pgxpool.Pool, models usagepersistence.ModelProjectionReader) *Service {
	return NewService(usagepostgres.New(pool), models)
}
