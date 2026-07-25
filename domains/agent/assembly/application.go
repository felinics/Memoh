package assembly

import (
	"github.com/jackc/pgx/v5/pgxpool"

	applicationpostgres "github.com/memohai/memoh/domains/agent/application/postgres"
)

// NewApplicationReads constructs Agent-owned application read adapters.
func NewApplicationReads(pool *pgxpool.Pool) *applicationpostgres.Reads {
	return applicationpostgres.NewReadsFromDB(pool)
}
