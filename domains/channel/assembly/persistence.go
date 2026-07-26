package assembly

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	channel "github.com/memohai/memoh/domains/channel"
	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/channel/identity"
	channelpostgres "github.com/memohai/memoh/domains/channel/internal/postgres/gateway"
	"github.com/memohai/memoh/domains/channel/route"
)

// IdentityLinkReader is the API-owned identity projection Channel persistence
// needs to resolve linked accounts.
type IdentityLinkReader interface {
	ListUserIDsByChannelIdentity(context.Context, string) ([]string, error)
}

// RouteSessionCoordinator preserves Agent-owned transaction ordering while
// Channel mutates the active Thread attached to a route.
type RouteSessionCoordinator interface {
	WithLockedRouteSessions(context.Context, string, func(pgx.Tx) error) error
	WithLockedSession(context.Context, string, func(pgx.Tx) error) error
}

// Store is the complete set of Channel persistence ports implemented by the
// owner-private PostgreSQL adapter. Process composition projects this bundle
// into the exact interfaces each consumer requires.
type Store interface {
	gateway.Persistence
	identity.Store
	channel.ConversationProjectionReader
	route.Store
}

// NewPostgresStore constructs the Channel-owned PostgreSQL persistence bundle.
func NewPostgresStore(
	pool *pgxpool.Pool,
	identityBindings gateway.IdentityBindingStore,
	identityLinks IdentityLinkReader,
	routeSessions RouteSessionCoordinator,
	bots route.BotPresence,
) Store {
	return channelpostgres.NewStore(
		pool,
		identityBindings,
		identityLinks,
		routeSessions,
		bots,
	)
}
