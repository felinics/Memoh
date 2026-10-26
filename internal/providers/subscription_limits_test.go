package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/models"
)

func TestLegacyGoSubscriptionCatalogIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, baseURL, metadata, want string
	}{
		{"official", "opencode-go", "https://opencode.ai/zen/go/v1/", `{}`, "opencode-go"},
		{"SDK default", "opencode-go", "", `{}`, "opencode-go"},
		{"custom proxy", "opencode-go", "https://proxy.example/v1", `{}`, ""},
		{"other endpoint", "opencode-go", "https://opencode.ai/zen/v1", `{}`, ""},
		{"other protocol", "openai-completions", "https://opencode.ai/zen/go/v1", `{}`, ""},
		{"explicit source", "opencode-go", "", `{"source":"custom"}`, "custom"},
		{"managed", "opencode-go", "", `{"managed":true}`, ""},
		{"legacy preset", "opencode-go", "", `{"preset":{"source":"opencode-go.yaml"}}`, "opencode-go"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, _ := json.Marshal(map[string]string{"base_url": tc.baseURL})
			provider := sqlc.Provider{ClientType: tc.protocol, Config: config, Metadata: []byte(tc.metadata)}
			got := (&Service{}).toGetResponse(provider)
			if source := PublicCatalogSource(got.Metadata); source != tc.want {
				t.Fatalf("source = %q, want %q", source, tc.want)
			}
			if string(provider.Metadata) != tc.metadata {
				t.Fatal("reading the provider must not rewrite its stored metadata")
			}
		})
	}
}

func TestSubscriptionCatalogLimits(t *testing.T) {
	t.Run("Codex keeps the default window and leaves output unknown", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"models":[
				{"slug":"default","visibility":"list","context_window":272000,"max_context_window":872000,"truncation_policy":{"mode":"tokens","limit":10000},"auto_compact_token_limit":250000},
				{"slug":"fallback","visibility":"list","context_window":0,"max_context_window":272000},
				{"slug":"unknown","visibility":"list","context_window":-1,"max_context_window":0}
			]}`))
		}))
		defer upstream.Close()
		got, err := (&Service{httpClient: upstream.Client()}).listCodexRemoteModels(context.Background(), upstream.URL, ModelCredentials{})
		if err != nil || len(got) != 3 {
			t.Fatalf("models = %+v, error = %v", got, err)
		}
		for i, want := range []int{272000, 272000, 0} {
			window := 0
			if got[i].ContextWindow != nil {
				window = *got[i].ContextWindow
			}
			if window != want || got[i].MaxOutputTokens != nil {
				t.Fatalf("model %s: window = %v, output = %v", got[i].ID, got[i].ContextWindow, got[i].MaxOutputTokens)
			}
		}
		if models.EnforcesMaxOutputTokens(models.ClientTypeOpenAICodex) {
			t.Fatal("catalog metadata must not enable an unsupported wire output cap")
		}
	})

	t.Run("Copilot does not import nonpositive limits", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"data":[{"id":"model","capabilities":{"type":"chat","limits":{"max_context_window_tokens":0,"max_output_tokens":-1}}}]}`))
		}))
		defer upstream.Close()
		got, err := (&Service{httpClient: upstream.Client()}).listGitHubCopilotRemoteModels(context.Background(), upstream.URL, "test")
		if err != nil || len(got) != 1 {
			t.Fatalf("models = %+v, error = %v", got, err)
		}
		if got[0].ContextWindow != nil || got[0].MaxOutputTokens != nil {
			t.Fatal("nonpositive catalog limits must stay unknown")
		}
	})
}
