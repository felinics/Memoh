// Package binding publishes API-owned outbound delivery bindings to
// Channel.
//
// The Postgres adapter stays owner-private under
// domains/api/internal/postgres/identity/binding because it reads API's own
// schema. This package is the public seam: Channel composition constructs the
// store here instead of reaching into API internals.
package binding

import (
	"github.com/jackc/pgx/v5/pgxpool"

	bindingpostgres "github.com/memohai/memoh/domains/api/internal/postgres/identity/binding"
	"github.com/memohai/memoh/domains/channel/gateway"
)

// NewPostgresStore builds the API-owned channel identity binding store.
func NewPostgresStore(pool *pgxpool.Pool) gateway.IdentityBindingStore {
	return bindingpostgres.NewStore(pool)
}
