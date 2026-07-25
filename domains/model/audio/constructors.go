package audio

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	audiopostgres "github.com/memohai/memoh/domains/model/internal/postgres/audio"
)

// NewPostgresService creates an audio service backed by PostgreSQL via the
// owner-private adapter. cmd composition should call this constructor only.
func NewPostgresService(log *slog.Logger, pool *pgxpool.Pool) *Service {
	return NewService(log, audiopostgres.NewStore(pool), NewRegistry())
}
