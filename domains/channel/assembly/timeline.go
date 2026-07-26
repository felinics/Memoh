package assembly

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/domains/agent/chat/timeline"
	timelinepostgres "github.com/memohai/memoh/domains/channel/internal/postgres/timeline"
)

// NewPostgresTimelineStore constructs the Channel-owned event timeline store.
func NewPostgresTimelineStore(pool *pgxpool.Pool) timeline.Store {
	return timelinepostgres.New(pool)
}
