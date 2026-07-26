// Package gateway implements Channel-owned PostgreSQL persistence.
package gateway

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/domains/channel"
	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/channel/identity"
	channelsqlc "github.com/memohai/memoh/domains/channel/internal/postgres/sqlc"
	"github.com/memohai/memoh/domains/channel/route"
)

// Store adapts generated PostgreSQL queries to Channel-owned persistence ports.
// Owner sqlc covers config/identity/projection/email and owner-local routes.
// Legacy queries remain only for blocked cross-owner surfaces:
// API user_channel_bindings / identity links, Agent-locked route session ops,
// and API bot presence (TouchBotActivity / GetBotByID).
type Store struct {
	configs          configQueries
	identities       identityQueries
	projections      conversationProjectionQueries
	identityBindings gateway.IdentityBindingStore
	identityLinks    identityLinkReader
	routes           routeQueries
	routeSessions    routeSessionCoordinator
	bots             route.BotPresence
}

var (
	_ gateway.Persistence                  = (*Store)(nil)
	_ identity.Store                       = (*Store)(nil)
	_ channel.ConversationProjectionReader = (*Store)(nil)
	_ route.Store                          = (*Store)(nil)
)

func NewStore(
	pool *pgxpool.Pool,
	identityBindings gateway.IdentityBindingStore,
	identityLinks identityLinkReader,
	routeSessions routeSessionCoordinator,
	bots route.BotPresence,
) *Store {
	owner := channelsqlc.New(pool)
	return &Store{
		configs:          owner,
		identities:       owner,
		projections:      owner,
		identityBindings: identityBindings,
		identityLinks:    identityLinks,
		routes:           owner,
		routeSessions:    routeSessions,
		bots:             bots,
	}
}

func NewIdentityStoreFromPool(pool *pgxpool.Pool) identity.Store {
	return &Store{identities: channelsqlc.New(pool)}
}
