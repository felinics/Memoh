package assembly

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/domains/agent/mcp"
	mcppostgres "github.com/memohai/memoh/domains/agent/mcp/postgres"
)

// NewMCPConnectionStore constructs Agent-owned MCP connection persistence.
func NewMCPConnectionStore(pool *pgxpool.Pool) mcp.ConnectionStore {
	return mcppostgres.NewConnectionStoreFromDB(pool)
}

// NewMCPConnectionService constructs the public MCP connection service.
func NewMCPConnectionService(log *slog.Logger, pool *pgxpool.Pool) *mcp.ConnectionService {
	return mcp.NewConnectionService(log, NewMCPConnectionStore(pool))
}

// NewMCPOAuthStore constructs Agent-owned MCP OAuth persistence.
func NewMCPOAuthStore(pool *pgxpool.Pool) mcp.OAuthStore {
	return mcppostgres.NewOAuthStoreFromDB(pool)
}

// NewMCPOAuthService constructs the public MCP OAuth service.
func NewMCPOAuthService(log *slog.Logger, pool *pgxpool.Pool, callbackURL string) *mcp.OAuthService {
	return mcp.NewOAuthService(log, NewMCPOAuthStore(pool), callbackURL)
}
