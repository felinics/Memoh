package agentcredential

import (
	"encoding/base64"
	"testing"

	"github.com/felinics/memoh/internal/config"
)

func TestServiceEncryptDecrypt(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	svc := NewService(nil, config.Config{Auth: config.AuthConfig{AgentCredentialsEncryptionKey: base64.StdEncoding.EncodeToString(key)}})
	if svc.aead == nil {
		t.Fatal("AEAD not configured")
	}
	want := map[string]string{"api_key": "SECRET", "refresh_token": "ROTATING"}
	ciphertext, nonce, err := svc.encrypt(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(ciphertext) == "SECRET" {
		t.Fatal("ciphertext exposed plaintext")
	}
	got, err := svc.decrypt(ciphertext, nonce, 1)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("%s = %q", key, got[key])
		}
	}
}

func TestServiceRejectsMissingEncryptionKey(t *testing.T) {
	svc := NewService(nil, config.Config{})
	if svc.aead != nil {
		t.Fatal("unexpected AEAD")
	}
}

func TestCompatibilityMatrix(t *testing.T) {
	for _, tc := range []struct {
		agent, kind string
		want        bool
	}{
		{"codex", AuthKindOpenAIAPIKey, true},
		{"codex", AuthKindOpenAICodexOAuth, true},
		{"claude-code", AuthKindClaudeCodeOAuth, true},
		{"claude-code", AuthKindOpenAIAPIKey, false},
		{"claude-code", AuthKindAnthropicAPIKey, true},
	} {
		if got := Compatible(tc.agent, tc.kind); got != tc.want {
			t.Fatalf("Compatible(%q,%q) = %v", tc.agent, tc.kind, got)
		}
	}
}
