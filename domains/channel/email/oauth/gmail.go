// Package oauth owns transport-free email OAuth flows used by the Server API.
package oauth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	emailpkg "github.com/memohai/memoh/domains/channel/email"
)

const (
	gmailScope     = "https://mail.google.com/"
	oauthClientRef = "gmail"
)

// Gmail performs the Gmail OAuth authorization flow without importing the
// Gmail SMTP/IMAP transport adapter.
type Gmail struct {
	tokens  emailpkg.OAuthTokenStore
	clients emailpkg.OAuthClientResolver
}

func NewGmail(tokens emailpkg.OAuthTokenStore, clients emailpkg.OAuthClientResolver) *Gmail {
	return &Gmail{tokens: tokens, clients: clients}
}

func (g *Gmail) HasOAuthClient() bool {
	client, ok := g.oauthClient()
	return ok && strings.TrimSpace(client.ClientID) != "" && strings.TrimSpace(client.ClientSecret) != ""
}

func (g *Gmail) EffectiveRedirectURI(fallback string) string {
	client, ok := g.oauthClient()
	if ok && strings.TrimSpace(client.RedirectURI) != "" {
		return strings.TrimSpace(client.RedirectURI)
	}
	return fallback
}

func (g *Gmail) AuthorizeURL(redirectURI, state string) (string, error) {
	config, err := g.oauth2Config(redirectURI)
	if err != nil {
		return "", err
	}
	return config.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent")), nil
}

func (g *Gmail) ExchangeCode(ctx context.Context, config map[string]any, providerID, code, redirectURI string) error {
	if g == nil || g.tokens == nil {
		return errors.New("gmail oauth token store is not configured")
	}
	oauthConfig, err := g.oauth2Config(redirectURI)
	if err != nil {
		return err
	}
	token, err := oauthConfig.Exchange(ctx, code)
	if err != nil {
		return fmt.Errorf("gmail token exchange: %w", err)
	}
	emailAddress, _ := config["email_address"].(string)
	return g.tokens.Save(ctx, emailpkg.OAuthToken{
		ProviderID:   providerID,
		EmailAddress: emailAddress,
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ExpiresAt:    token.Expiry,
		Scope:        gmailScope,
	})
}

func (g *Gmail) oauth2Config(redirectURI string) (*oauth2.Config, error) {
	client, ok := g.oauthClient()
	if !ok || strings.TrimSpace(client.ClientID) == "" || strings.TrimSpace(client.ClientSecret) == "" {
		return nil, errors.New("gmail oauth client is not configured")
	}
	return &oauth2.Config{
		ClientID:     client.ClientID,
		ClientSecret: client.ClientSecret,
		RedirectURL:  g.EffectiveRedirectURI(redirectURI),
		Scopes:       []string{gmailScope},
		Endpoint:     google.Endpoint,
	}, nil
}

func (g *Gmail) oauthClient() (emailpkg.OAuthClient, bool) {
	if g == nil || g.clients == nil {
		return emailpkg.OAuthClient{}, false
	}
	return g.clients.Get(oauthClientRef)
}
