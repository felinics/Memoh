package video

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	videopostgres "github.com/memohai/memoh/domains/model/internal/postgres/video"
)

// NewPostgresService creates a video service backed by PostgreSQL via the
// owner-private adapter. cmd composition should call this constructor only.
func NewPostgresService(log *slog.Logger, pool *pgxpool.Pool) *Service {
	return NewService(log, videopostgres.NewStore(pool), NewRegistry())
}
