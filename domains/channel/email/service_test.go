package email

import "testing"

func TestSanitizeProviderConfigRemovesGmailOAuthSecrets(t *testing.T) {
	clean := sanitizeProviderConfig("gmail", map[string]any{
		"email_address": "person@gmail.com",
		"client_id":     "legacy-client",
		"client_secret": "legacy-secret",
	})

	if _, ok := clean["client_id"]; ok {
		t.Fatal("client_id should not be returned for gmail providers")
	}
	if _, ok := clean["client_secret"]; ok {
		t.Fatal("client_secret should not be returned for gmail providers")
	}
	if clean["email_address"] != "person@gmail.com" {
		t.Fatalf("email_address was not preserved: %#v", clean)
	}
}

func TestSanitizeProviderConfigKeepsNonGmailSecrets(t *testing.T) {
	clean := sanitizeProviderConfig("smtp", map[string]any{
		"client_id":     "smtp-client",
		"client_secret": "smtp-secret",
	})

	if clean["client_id"] != "smtp-client" {
		t.Fatalf("client_id should be preserved for non-gmail providers: %#v", clean)
	}
	if clean["client_secret"] != "smtp-secret" {
		t.Fatalf("client_secret should be preserved for non-gmail providers: %#v", clean)
	}
}
