// Package catalog registers concrete email sender and receiver adapters into
// the process-owned email Registry.
package catalog

import (
	"context"
	"errors"
	"log/slog"

	emailpkg "github.com/memohai/memoh/domains/channel/email"
	emailgeneric "github.com/memohai/memoh/domains/channel/internal/email/generic"
	emailgmail "github.com/memohai/memoh/domains/channel/internal/email/gmail"
	emailmailgun "github.com/memohai/memoh/domains/channel/internal/email/mailgun"
	emailport "github.com/memohai/memoh/domains/channel/internal/port/email"
)

// RegisterDefaults replaces transport-free descriptors with the built-in
// concrete adapters on the same Registry instance held by email.Service.
func RegisterDefaults(registry *emailpkg.Registry, log *slog.Logger, tokens emailpkg.OAuthTokenStore, clients emailpkg.OAuthClientResolver) {
	registry.Register(emailgeneric.New(log))
	registry.Register(emailmailgun.New(log))
	registry.Register(emailgmail.New(log, adaptTokenStore(tokens), adaptOAuthResolver(clients)))
}

func adaptTokenStore(store emailpkg.OAuthTokenStore) emailport.OAuthTokenStore {
	if store == nil {
		return nil
	}
	return tokenStore{store: store}
}

type tokenStore struct {
	store emailpkg.OAuthTokenStore
}

func (s tokenStore) Get(ctx context.Context, providerID string) (*emailport.OAuthToken, error) {
	token, err := s.store.Get(ctx, providerID)
	if err != nil {
		return nil, toPortError(err)
	}
	return toPortToken(token), nil
}

func (s tokenStore) Save(ctx context.Context, token emailport.OAuthToken) error {
	return toPortError(s.store.Save(ctx, fromPortToken(token)))
}

func (s tokenStore) SetPendingState(ctx context.Context, providerID, state string) error {
	return toPortError(s.store.SetPendingState(ctx, providerID, state))
}

func (s tokenStore) GetByState(ctx context.Context, state string) (*emailport.OAuthToken, error) {
	token, err := s.store.GetByState(ctx, state)
	if err != nil {
		return nil, toPortError(err)
	}
	return toPortToken(token), nil
}

func (s tokenStore) Delete(ctx context.Context, providerID string) error {
	return toPortError(s.store.Delete(ctx, providerID))
}

func toPortError(err error) error {
	if errors.Is(err, emailpkg.ErrNotFound) {
		return emailport.ErrNotFound
	}
	return err
}

func toPortToken(token *emailpkg.OAuthToken) *emailport.OAuthToken {
	if token == nil {
		return nil
	}
	return &emailport.OAuthToken{
		ProviderID:   token.ProviderID,
		EmailAddress: token.EmailAddress,
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ExpiresAt:    token.ExpiresAt,
		Scope:        token.Scope,
	}
}

func fromPortToken(token emailport.OAuthToken) emailpkg.OAuthToken {
	return emailpkg.OAuthToken{
		ProviderID:   token.ProviderID,
		EmailAddress: token.EmailAddress,
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ExpiresAt:    token.ExpiresAt,
		Scope:        token.Scope,
	}
}

func adaptOAuthResolver(resolver emailpkg.OAuthClientResolver) emailport.OAuthClientResolver {
	if resolver == nil {
		return nil
	}
	return oauthResolver{resolver: resolver}
}

type oauthResolver struct {
	resolver emailpkg.OAuthClientResolver
}

func (r oauthResolver) Get(ref string) (emailport.OAuthClient, bool) {
	client, ok := r.resolver.Get(ref)
	if !ok {
		return emailport.OAuthClient{}, false
	}
	return emailport.OAuthClient{
		ClientID:     client.ClientID,
		ClientSecret: client.ClientSecret,
		RedirectURI:  client.RedirectURI,
	}, true
}

func (r oauthResolver) HasUsableClient(ref string) bool {
	return r.resolver.HasUsableClient(ref)
}
