package assembly

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/domains/agent/chat/usage"
	usagepostgres "github.com/memohai/memoh/domains/agent/chat/usage/postgres"
	agentsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
)

// NewUsageReader constructs the Agent-owned usage reader and Model enrichment service.
func NewUsageReader(pool *pgxpool.Pool, models usage.ModelProjectionReader) *usage.Service {
	return usage.NewService(usagepostgres.New(agentsqlc.New(pool)), models)
}
