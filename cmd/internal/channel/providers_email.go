package channel

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	emailpkg "github.com/memohai/memoh/domains/channel/email"
)

func provideEmailOAuthTokenStore(pool *pgxpool.Pool) emailpkg.OAuthTokenStore {
	return emailpkg.NewPostgresOAuthTokenStore(pool)
}

func provideEmailService(log *slog.Logger, pool *pgxpool.Pool, registry *emailpkg.Registry) *emailpkg.Service {
	return emailpkg.NewPostgresService(log, pool, registry)
}

func provideEmailOutboxService(log *slog.Logger, pool *pgxpool.Pool) *emailpkg.OutboxService {
	return emailpkg.NewPostgresOutboxService(log, pool)
}

func provideEmailRegistry() *emailpkg.Registry {
	return emailpkg.NewDescriptorRegistry()
}
