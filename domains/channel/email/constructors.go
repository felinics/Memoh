package email

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	descriptorcatalog "github.com/memohai/memoh/domains/channel/internal/email/catalog"
	emailpostgres "github.com/memohai/memoh/domains/channel/internal/postgres/email"
)

// NewPostgresOAuthTokenStore creates the public OAuth token store over PostgreSQL.
func NewPostgresOAuthTokenStore(pool *pgxpool.Pool) OAuthTokenStore {
	return newOAuthTokenStore(emailpostgres.NewOAuthTokenStore(pool))
}

// NewPostgresService creates the email CRUD service over PostgreSQL stores.
func NewPostgresService(log *slog.Logger, pool *pgxpool.Pool, registry *Registry) *Service {
	return NewService(
		log,
		emailpostgres.NewProviderStore(pool),
		emailpostgres.NewBindingStore(pool),
		registry,
	)
}

// NewPostgresOutboxService creates the outbox audit service over PostgreSQL.
func NewPostgresOutboxService(log *slog.Logger, pool *pgxpool.Pool) *OutboxService {
	return NewOutboxService(log, emailpostgres.NewOutboxStore(pool))
}

// NewDescriptorRegistry returns the transport-free provider catalog used by
// every process profile. Runtime composition replaces these entries with
// concrete sender and receiver adapters on the same Registry instance.
func NewDescriptorRegistry() *Registry {
	registry := NewRegistry()
	registry.Register(descriptorcatalog.Generic())
	registry.Register(descriptorcatalog.Gmail())
	registry.Register(descriptorcatalog.Mailgun())
	return registry
}
