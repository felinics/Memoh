package email

import (
	"context"

	emailport "github.com/memohai/memoh/domains/channel/internal/port/email"
)

type gmailOAuthBackend interface {
	HasOAuthClient() bool
	EffectiveRedirectURI(fallback string) string
	AuthorizeURL(redirectURI, state string) (string, error)
	ExchangeCode(ctx context.Context, config map[string]any, providerID, code, redirectURI string) error
}

// GmailOAuth is the public Gmail OAuth surface for HTTP handlers.
type GmailOAuth struct {
	inner gmailOAuthBackend
}

func (g *GmailOAuth) HasOAuthClient() bool {
	if g == nil || g.inner == nil {
		return false
	}
	return g.inner.HasOAuthClient()
}

func (g *GmailOAuth) EffectiveRedirectURI(fallback string) string {
	if g == nil || g.inner == nil {
		return fallback
	}
	return g.inner.EffectiveRedirectURI(fallback)
}

func (g *GmailOAuth) AuthorizeURL(redirectURI, state string) (string, error) {
	return g.inner.AuthorizeURL(redirectURI, state)
}

func (g *GmailOAuth) ExchangeCode(ctx context.Context, config map[string]any, providerID, code, redirectURI string) error {
	return g.inner.ExchangeCode(ctx, config, providerID, code, redirectURI)
}

func adaptOAuthResolver(oauth OAuthClientResolver) emailport.OAuthClientResolver {
	if oauth == nil {
		return nil
	}
	return oauthResolverAdapter{inner: oauth}
}

type oauthResolverAdapter struct {
	inner OAuthClientResolver
}

func (a oauthResolverAdapter) Get(ref string) (emailport.OAuthClient, bool) {
	client, ok := a.inner.Get(ref)
	if !ok {
		return emailport.OAuthClient{}, false
	}
	return emailport.OAuthClient{
		ClientID:     client.ClientID,
		ClientSecret: client.ClientSecret,
		RedirectURI:  client.RedirectURI,
	}, true
}

func (a oauthResolverAdapter) HasUsableClient(ref string) bool {
	return a.inner.HasUsableClient(ref)
}
