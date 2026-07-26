package plugins

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/domains/agent/extension/plugins/persistence"
	pluginspostgres "github.com/memohai/memoh/domains/agent/internal/postgres/plugins"
	"github.com/memohai/memoh/domains/agent/mcp"
)

// NewPostgresStore constructs Agent-owned plugin persistence.
func NewPostgresStore(pool *pgxpool.Pool) persistence.Store {
	return pluginspostgres.NewStoreFromDB(pool)
}

// NewPostgresService constructs the plugins extension service backed by
// PostgreSQL.
func NewPostgresService(
	log *slog.Logger,
	pool *pgxpool.Pool,
	mcpService *mcp.ConnectionService,
	oauthService *mcp.OAuthService,
	oauthClients *OAuthClientRegistry,
	bridges BridgeProvider,
) *Service {
	return NewService(log, NewPostgresStore(pool), mcpService, oauthService, oauthClients, bridges)
}
