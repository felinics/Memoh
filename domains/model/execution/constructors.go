package execution

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	executionpostgres "github.com/memohai/memoh/domains/model/internal/postgres/execution"
	"github.com/memohai/memoh/domains/model/provider"
)

// ProviderCredentialResolver resolves runtime credentials for a provider record.
type ProviderCredentialResolver interface {
	ResolveModelCredentials(context.Context, provider.ProviderRecord) (provider.ResolvedCredentials, error)
}

// NewPostgresResolver creates an execution Resolver with the owner-private
// PostgreSQL provider source. models is a consumer-owned catalog read port so
// execution never imports catalog concrete types.
func NewPostgresResolver(
	models ModelReader,
	pool *pgxpool.Pool,
	credentials ProviderCredentialResolver,
	opts ...ResolverOption,
) *Resolver {
	return NewResolver(models, executionpostgres.NewProviderSource(pool, credentials), opts...)
}
