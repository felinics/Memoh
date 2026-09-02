package codex

import (
	"context"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

type recordingCodexConfigWriter struct {
	writes map[string][]byte
}

func (w *recordingCodexConfigWriter) WriteFile(_ context.Context, target string, content []byte) error {
	if w.writes == nil {
		w.writes = map[string][]byte{}
	}
	w.writes[target] = append([]byte(nil), content...)
	return nil
}

func TestMaterializeCodexConfig(t *testing.T) {
	t.Parallel()

	writer := &recordingCodexConfigWriter{}
	home := codexHome("agent")
	baseURL := "https://relay.example/v1"
	secret := "credential-must-not-be-materialized" //nolint:gosec // Test-only sentinel proves the value is excluded.
	if err := materializeCodexConfig(context.Background(), writer, home, Config{
		Auth:    AuthAPIKey,
		APIKey:  secret,
		BaseURL: baseURL,
		Model:   "model-must-not-be-materialized",
	}); err != nil {
		t.Fatalf("materializeCodexConfig(): %v", err)
	}

	payload := writer.writes[home+"/config.toml"]
	assertManagedCodexConfig(t, payload, baseURL)
	if strings.Contains(string(payload), secret) || strings.Contains(string(payload), "model-must-not-be-materialized") {
		t.Fatalf("managed config contains a credential or protocol-owned model: %q", payload)
	}
	if err := materializeCodexConfig(context.Background(), writer, home, Config{Auth: AuthChatGPT, BaseURL: "https://stale.example/v1"}); err != nil {
		t.Fatalf("clear materialized config: %v", err)
	}
	assertManagedCodexConfig(t, writer.writes[home+"/config.toml"], "")

	for _, entry := range codexAppServerEnv(home) {
		if strings.HasPrefix(entry, "OPENAI_BASE_URL=") || strings.HasPrefix(entry, "OPENAI_API_KEY=") || strings.HasPrefix(entry, "MEMOH_CODEX_API_KEY=") {
			t.Fatalf("Codex app-server environment leaks provider configuration or credentials: %q", entry)
		}
	}
}

func assertManagedCodexConfig(t *testing.T, payload []byte, wantBaseURL string) {
	t.Helper()
	if !strings.HasPrefix(string(payload), codexManagedConfigHeader) {
		t.Fatalf("managed config header missing: %q", payload)
	}
	var decoded codexManagedConfig
	if _, err := toml.Decode(string(payload), &decoded); err != nil {
		t.Fatalf("decode managed config: %v", err)
	}
	if decoded.OpenAIBaseURL != wantBaseURL {
		t.Fatalf("openai_base_url = %q, want %q", decoded.OpenAIBaseURL, wantBaseURL)
	}
	if wantBaseURL == "" && strings.Contains(string(payload), "openai_base_url") {
		t.Fatalf("cleared managed config retained openai_base_url: %q", payload)
	}
}
