package account

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/domains/iam/account/persistence"
	accountpostgres "github.com/memohai/memoh/domains/iam/internal/postgres/account"
)

// NewPostgresService creates the account service backed by PostgreSQL.
//
// Composition roots call this instead of assembling a Store themselves: the
// persistence adapter stays owner-private, and callers only ever hold the
// business surface.
func NewPostgresService(log *slog.Logger, pool *pgxpool.Pool, titleModels ...persistence.TitleModelValidator) *Service {
	return NewService(log, accountpostgres.NewStore(pool), titleModels...)
}

// NewPostgresCounter creates the bootstrap-only account counter.
//
// This stays separate from Service because it answers a repository-wide
// cardinality question that no account operation needs.
func NewPostgresCounter(pool *pgxpool.Pool) persistence.AccountCounter {
	return accountpostgres.NewStore(pool)
}

// NewPostgresRecovery creates admin account recovery backed by PostgreSQL.
func NewPostgresRecovery(pool *pgxpool.Pool) *Recovery {
	return NewRecovery(accountpostgres.NewRecoveryStore(pool))
}
