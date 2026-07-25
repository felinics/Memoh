package provider

import (
	"context"
	"testing"
)

type providerScopedOAuthStore struct {
	ProviderStore
	OAuthStore
	provider           ProviderRecord
	token              OAuthTokenRecord
	providerTokenReads int
}

func (s *providerScopedOAuthStore) GetProvider(context.Context, string) (ProviderRecord, error) {
	return s.provider, nil
}

func (s *providerScopedOAuthStore) GetOAuthToken(context.Context, string) (OAuthTokenRecord, error) {
	s.providerTokenReads++
	return s.token, nil
}

func TestGitHubCopilotOAuthUsesProviderScopedToken(t *testing.T) {
	t.Parallel()

	providerID := "01010101-0101-0101-0101-010101010101"
	store := &providerScopedOAuthStore{
		provider: ProviderRecord{
			ID:         providerID,
			ClientType: "github-copilot",
		},
		token: OAuthTokenRecord{
			ProviderID:  providerID,
			AccessToken: "shared-github-token",
		},
	}
	service := NewService(nil, store, store, "", "")

	token, err := service.GetValidAccessToken(context.Background(), providerID)
	if err != nil {
		t.Fatalf("get shared Copilot OAuth token: %v", err)
	}
	if token != "shared-github-token" {
		t.Fatalf("token = %q, want shared provider token", token)
	}
	if store.providerTokenReads != 1 {
		t.Fatalf("provider token reads = %d, want 1", store.providerTokenReads)
	}
}
