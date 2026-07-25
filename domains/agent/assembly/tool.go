package assembly

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/domains/agent/tool"
	historypostgres "github.com/memohai/memoh/domains/agent/tool/postgres"
)

// NewHistorySearcher constructs Agent-owned message history search.
func NewHistorySearcher(pool *pgxpool.Pool) tool.HistorySearcher {
	return historypostgres.NewSearcherFromDB(pool)
}
