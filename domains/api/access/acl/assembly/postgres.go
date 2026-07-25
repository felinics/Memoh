// Package assembly wires ACL application ports to owner-local adapters.
package assembly

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/domains/api/access/acl"
	aclpostgres "github.com/memohai/memoh/domains/api/internal/access/acl/postgres"
	apisqlc "github.com/memohai/memoh/domains/api/internal/postgres/sqlc"
)

func NewStore(pool *pgxpool.Pool) acl.Store {
	return aclpostgres.NewStore(apisqlc.New(pool), pool)
}
