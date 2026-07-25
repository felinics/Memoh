package assembly

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/domains/agent/extension/hooks"
	"github.com/memohai/memoh/domains/agent/extension/plugins"
	pluginspostgres "github.com/memohai/memoh/domains/agent/extension/plugins/postgres"
	"github.com/memohai/memoh/domains/agent/mcp"
	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
	"github.com/memohai/memoh/internal/config"
)

// NewHookService constructs the public hooks extension service.
func NewHookService(log *slog.Logger, provider bridge.Provider) *hooks.Service {
	return hooks.NewService(log, provider)
}

// NewPluginStore constructs Agent-owned plugin persistence.
func NewPluginStore(pool *pgxpool.Pool) plugins.Store {
	return pluginspostgres.NewStoreFromDB(pool)
}

// NewPluginService constructs the public plugins extension service.
func NewPluginService(
	log *slog.Logger,
	pool *pgxpool.Pool,
	mcpService *mcp.ConnectionService,
	oauthService *mcp.OAuthService,
	oauthClients *plugins.OAuthClientRegistry,
	bridges plugins.BridgeProvider,
) *plugins.Service {
	return plugins.NewService(log, NewPluginStore(pool), mcpService, oauthService, oauthClients, bridges)
}

// NewPluginOAuthClientRegistry constructs the plugin OAuth client registry.
func NewPluginOAuthClientRegistry(log *slog.Logger, cfg config.Config) *plugins.OAuthClientRegistry {
	return plugins.NewOAuthClientRegistry(log, cfg)
}
