package mcp

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	mcppostgres "github.com/memohai/memoh/domains/agent/internal/postgres/mcp"
	"github.com/memohai/memoh/domains/agent/mcp/persistence"
)

// NewPostgresConnectionStore constructs Agent-owned MCP connection persistence.
func NewPostgresConnectionStore(pool *pgxpool.Pool) persistence.ConnectionStore {
	return mcppostgres.NewConnectionStoreFromDB(pool)
}

// NewPostgresConnectionService constructs the MCP connection service.
func NewPostgresConnectionService(log *slog.Logger, pool *pgxpool.Pool) *ConnectionService {
	return NewConnectionService(log, NewPostgresConnectionStore(pool))
}

// NewPostgresOAuthStore constructs Agent-owned MCP OAuth persistence.
func NewPostgresOAuthStore(pool *pgxpool.Pool) persistence.OAuthStore {
	return mcppostgres.NewOAuthStoreFromDB(pool)
}

// NewPostgresOAuthService constructs the MCP OAuth service.
func NewPostgresOAuthService(log *slog.Logger, pool *pgxpool.Pool, callbackURL string) *OAuthService {
	return NewOAuthService(log, NewPostgresOAuthStore(pool), callbackURL)
}
