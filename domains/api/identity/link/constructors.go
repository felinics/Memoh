package link

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/domains/api/identity/link/persistence"
	linkpostgres "github.com/memohai/memoh/domains/api/internal/postgres/identity/link"
)

// NewPostgresStore constructs identity-link persistence backed by PostgreSQL.
func NewPostgresStore(pool *pgxpool.Pool) persistence.Store {
	return linkpostgres.NewStore(pool)
}
