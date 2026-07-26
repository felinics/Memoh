package schedule

import (
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/domains/agent/automation/schedule/persistence"
	schedulepostgres "github.com/memohai/memoh/domains/agent/internal/postgres/schedule"
)

// NewPostgresStore constructs schedule automation persistence backed by
// PostgreSQL, for composition that wires the Store and the Service separately.
func NewPostgresStore(pool *pgxpool.Pool, bots persistence.BotReader) persistence.Store {
	return schedulepostgres.NewStoreFromDB(pool, bots)
}

// NewPostgresService constructs the schedule automation service backed by
// PostgreSQL.
func NewPostgresService(
	log *slog.Logger,
	pool *pgxpool.Pool,
	bots persistence.BotReader,
	triggerer Triggerer,
	sessionCreator SessionCreator,
	jwtSecret string,
	location *time.Location,
) *Service {
	return NewService(log, schedulepostgres.NewStoreFromDB(pool, bots), triggerer, sessionCreator, jwtSecret, location)
}
