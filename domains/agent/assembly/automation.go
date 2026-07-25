package assembly

import (
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/domains/agent/automation/heartbeat"
	heartbeatpostgres "github.com/memohai/memoh/domains/agent/automation/heartbeat/postgres"
	"github.com/memohai/memoh/domains/agent/automation/schedule"
	schedulepostgres "github.com/memohai/memoh/domains/agent/automation/schedule/postgres"
)

// NewScheduleService constructs the public schedule automation service.
func NewScheduleService(
	log *slog.Logger,
	pool *pgxpool.Pool,
	bots schedule.BotReader,
	triggerer schedule.Triggerer,
	sessionCreator schedule.SessionCreator,
	jwtSecret string,
	location *time.Location,
) *schedule.Service {
	return schedule.NewService(log, schedulepostgres.NewStoreFromDB(pool, bots), triggerer, sessionCreator, jwtSecret, location)
}

// NewHeartbeatService constructs the public heartbeat automation service.
func NewHeartbeatService(
	log *slog.Logger,
	pool *pgxpool.Pool,
	bots heartbeat.BotReader,
	triggerer heartbeat.Triggerer,
	sessionCreator heartbeat.SessionCreator,
	jwtSecret string,
) *heartbeat.Service {
	return heartbeat.NewService(log, heartbeatpostgres.NewStoreFromDB(pool, bots), triggerer, sessionCreator, jwtSecret)
}
