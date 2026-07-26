package heartbeat

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/domains/agent/automation/heartbeat/persistence"
	heartbeatpostgres "github.com/memohai/memoh/domains/agent/internal/postgres/heartbeat"
)

// NewPostgresStore constructs heartbeat automation persistence backed by
// PostgreSQL, for composition that wires the Store and the Service separately.
func NewPostgresStore(pool *pgxpool.Pool, bots persistence.BotReader) persistence.Store {
	return heartbeatpostgres.NewStoreFromDB(pool, bots)
}

// NewPostgresService constructs the heartbeat automation service backed by
// PostgreSQL. Composition roots call this instead of assembling a Store.
func NewPostgresService(
	log *slog.Logger,
	pool *pgxpool.Pool,
	bots persistence.BotReader,
	triggerer Triggerer,
	sessionCreator SessionCreator,
	jwtSecret string,
) *Service {
	return NewService(log, heartbeatpostgres.NewStoreFromDB(pool, bots), triggerer, sessionCreator, jwtSecret)
}
