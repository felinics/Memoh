package audio

import (
	"testing"

	"github.com/felinics/memoh/internal/providers"
)

// The settings UI PUTs these masked values back through /providers/:id; the
// mask shape must be the providers package's single contract or the round-
// trip overwrites the stored secret with the masked literal.
func TestMaskSpeechProviderConfig_UsesSharedMaskContract(t *testing.T) {
	t.Parallel()

	cfg := map[string]any{
		"api_key":    "sk-real-key-1234567890",
		"access_key": "access-key-1234567890",
		"base_url":   "https://example.com/v1",
	}

	out := maskSpeechProviderConfig(cfg)

	for _, key := range []string{"api_key", "access_key"} {
		secret, _ := cfg[key].(string)
		want := providers.MaskAPIKey(secret)
		if got, _ := out[key].(string); got != want {
			t.Fatalf("key %s: mask = %q, want shared-contract %q", key, got, want)
		}
	}
	if got, _ := out["base_url"].(string); got != "https://example.com/v1" {
		t.Fatalf("non-secret field must pass through, got %q", got)
	}
}
