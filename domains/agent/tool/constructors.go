package tool

import (
	"github.com/jackc/pgx/v5/pgxpool"

	historypostgres "github.com/memohai/memoh/domains/agent/internal/postgres/tool"
	"github.com/memohai/memoh/domains/agent/tool/persistence"
)

// NewPostgresHistorySearcher constructs Agent-owned message history search
// backed by PostgreSQL.
func NewPostgresHistorySearcher(pool *pgxpool.Pool) persistence.HistorySearcher {
	return historypostgres.NewSearcherFromDB(pool)
}
