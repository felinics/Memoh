package providers

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type providerScopedOAuthQueries struct {
	dbstore.Queries
	provider           sqlc.Provider
	token              sqlc.ProviderOauthToken
	providerTokenReads int
}

func (q *providerScopedOAuthQueries) GetProviderByID(context.Context, pgtype.UUID) (sqlc.Provider, error) {
	return q.provider, nil
}

func (q *providerScopedOAuthQueries) GetProviderOAuthTokenByProvider(context.Context, pgtype.UUID) (sqlc.ProviderOauthToken, error) {
	q.providerTokenReads++
	return q.token, nil
}

func TestGitHubCopilotOAuthUsesProviderScopedToken(t *testing.T) {
	t.Parallel()

	providerID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	queries := &providerScopedOAuthQueries{
		provider: sqlc.Provider{
			ID:         providerID,
			ClientType: "github-copilot",
		},
		token: sqlc.ProviderOauthToken{
			ProviderID:  providerID,
			AccessToken: "shared-github-token",
		},
	}
	service := NewService(nil, queries, "")

	token, err := service.GetValidAccessToken(context.Background(), providerID.String())
	if err != nil {
		t.Fatalf("get shared Copilot OAuth token: %v", err)
	}
	if token != "shared-github-token" {
		t.Fatalf("token = %q, want shared provider token", token)
	}
	if queries.providerTokenReads != 1 {
		t.Fatalf("provider token reads = %d, want 1", queries.providerTokenReads)
	}
}
