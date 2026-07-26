package acl

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/domains/api/bot/access/acl/persistence"
	aclpostgres "github.com/memohai/memoh/domains/api/internal/postgres/bot/access/acl"
)

// NewPostgresStore constructs ACL persistence backed by PostgreSQL.
func NewPostgresStore(pool *pgxpool.Pool) persistence.Store {
	return aclpostgres.NewStore(pool)
}
