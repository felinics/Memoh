package gmail

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/oauthclients"
)

type testOAuthResolver map[string]oauthclients.Client

func (r testOAuthResolver) Get(ref string) (oauthclients.Client, bool) {
	client, ok := r[ref]
	return client, ok
}

func (r testOAuthResolver) HasUsableClient(ref string) bool {
	client, ok := r.Get(ref)
	return ok && strings.TrimSpace(client.ClientID) != "" && strings.TrimSpace(client.ClientSecret) != ""
}

func TestMetaOnlyExposesGmailAddress(t *testing.T) {
	adapter := New(slog.Default(), nil, nil)

	fields := adapter.Meta().ConfigSchema.Fields
	if len(fields) != 1 {
		t.Fatalf("expected one gmail config field, got %d", len(fields))
	}
	if fields[0].Key != "email_address" {
		t.Fatalf("expected only email_address field, got %q", fields[0].Key)
	}
}

func TestNormalizeConfigStripsLegacyOAuthSecrets(t *testing.T) {
	adapter := New(slog.Default(), nil, nil)

	clean, err := adapter.NormalizeConfig(map[string]any{
		"email_address": "person@gmail.com",
		"client_id":     "legacy-client",
		"client_secret": "legacy-secret",
		"label":         "primary",
	})
	if err != nil {
		t.Fatalf("NormalizeConfig returned error: %v", err)
	}
	if _, ok := clean["client_id"]; ok {
		t.Fatal("client_id should be stripped from gmail config")
	}
	if _, ok := clean["client_secret"]; ok {
		t.Fatal("client_secret should be stripped from gmail config")
	}
	if clean["email_address"] != "person@gmail.com" {
		t.Fatalf("email_address was not preserved: %#v", clean)
	}

	empty, err := adapter.NormalizeConfig(map[string]any{})
	if err != nil {
		t.Fatalf("empty config should be allowed for default gmail provider: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty config should stay empty, got %#v", empty)
	}

	if _, err := adapter.NormalizeConfig(map[string]any{"label": "primary"}); err == nil {
		t.Fatal("non-empty config without email_address should fail")
	}
}

func TestOAuthUsesServerSideClient(t *testing.T) {
	adapter := New(slog.Default(), nil, testOAuthResolver{
		"gmail": {
			ClientID:     "server-client",
			ClientSecret: "server-secret",
			RedirectURI:  "https://example.com/fixed-callback",
		},
	})

	if !adapter.HasOAuthClient() {
		t.Fatal("expected configured server-side gmail oauth client")
	}
	if got := adapter.EffectiveRedirectURI("https://request/callback"); got != "https://example.com/fixed-callback" {
		t.Fatalf("expected fixed redirect URI, got %q", got)
	}
	authURL, err := adapter.AuthorizeURL("https://request/callback", "state-123")
	if err != nil {
		t.Fatalf("AuthorizeURL returned error: %v", err)
	}
	if !strings.Contains(authURL, "client_id=server-client") {
		t.Fatalf("authorize URL should contain server client id, got %q", authURL)
	}
	if !strings.Contains(authURL, "state=state-123") {
		t.Fatalf("authorize URL should contain state, got %q", authURL)
	}

	missingSecret := New(slog.Default(), nil, testOAuthResolver{
		"gmail": {ClientID: "server-client"},
	})
	if missingSecret.HasOAuthClient() {
		t.Fatal("oauth client without secret should not be usable for gmail")
	}
	if _, err := missingSecret.AuthorizeURL("https://request/callback", "state"); err == nil {
		t.Fatal("AuthorizeURL should fail when server-side gmail oauth secret is missing")
	}
}

func TestExchangeCodeRequiresServerOAuthClient(t *testing.T) {
	adapter := New(slog.Default(), nil, nil)

	err := adapter.ExchangeCode(context.Background(), map[string]any{"email_address": "person@gmail.com"}, "provider-1", "code", "https://request/callback")
	if err == nil {
		t.Fatal("ExchangeCode should fail before network access when server oauth client is missing")
	}
	if !strings.Contains(err.Error(), "gmail oauth client is not configured") {
		t.Fatalf("unexpected error: %v", err)
	}
}
