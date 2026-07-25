package email

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	emailgeneric "github.com/memohai/memoh/domains/channel/internal/email/generic"
	emailgmail "github.com/memohai/memoh/domains/channel/internal/email/gmail"
	emailmailgun "github.com/memohai/memoh/domains/channel/internal/email/mailgun"
	emailpostgres "github.com/memohai/memoh/domains/channel/internal/email/postgres"
	emailport "github.com/memohai/memoh/domains/channel/internal/port/email"
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

// NewDefaultRegistry registers the built-in generic/mailgun/gmail adapters.
func NewDefaultRegistry(log *slog.Logger, tokens OAuthTokenStore, oauth OAuthClientResolver) *Registry {
	reg := NewRegistry()
	reg.Register(emailgeneric.New(log))
	reg.Register(emailmailgun.New(log))
	reg.Register(emailgmail.New(log, portOAuthTokenStore(tokens), adaptOAuthResolver(oauth)))
	return reg
}

// NewGmailOAuth constructs the Gmail OAuth helper used by API handlers.
func NewGmailOAuth(log *slog.Logger, tokens OAuthTokenStore, oauth OAuthClientResolver) *GmailOAuth {
	return &GmailOAuth{inner: emailgmail.New(log, portOAuthTokenStore(tokens), adaptOAuthResolver(oauth))}
}

func portOAuthTokenStore(store OAuthTokenStore) emailport.OAuthTokenStore {
	if store == nil {
		return nil
	}
	if adapted, ok := store.(*oauthTokenStore); ok {
		return adapted.inner
	}
	return &oauthTokenStoreBridge{public: store}
}
