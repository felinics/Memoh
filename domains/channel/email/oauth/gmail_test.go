package oauth

import (
	"strings"
	"testing"

	emailpkg "github.com/memohai/memoh/domains/channel/email"
)

type oauthResolver map[string]emailpkg.OAuthClient

func (r oauthResolver) Get(ref string) (emailpkg.OAuthClient, bool) {
	client, ok := r[ref]
	return client, ok
}

func (r oauthResolver) HasUsableClient(ref string) bool {
	client, ok := r.Get(ref)
	return ok && client.ClientID != "" && client.ClientSecret != ""
}

func TestGmailAuthorizationUsesServerOAuthClient(t *testing.T) {
	gmail := NewGmail(nil, oauthResolver{
		"gmail": {
			ClientID:     "server-client",
			ClientSecret: "server-secret",
			RedirectURI:  "https://example.com/callback",
		},
	})

	if !gmail.HasOAuthClient() {
		t.Fatal("configured OAuth client was not detected")
	}
	if got := gmail.EffectiveRedirectURI("https://fallback.example.com"); got != "https://example.com/callback" {
		t.Fatalf("redirect URI = %q", got)
	}
	authorizeURL, err := gmail.AuthorizeURL("https://fallback.example.com", "state-123")
	if err != nil {
		t.Fatalf("authorize URL: %v", err)
	}
	if !strings.Contains(authorizeURL, "client_id=server-client") || !strings.Contains(authorizeURL, "state=state-123") {
		t.Fatalf("authorize URL does not contain expected parameters: %s", authorizeURL)
	}
}
