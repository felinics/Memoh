package codex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCustomBaseURLModelCatalogUsesConfiguredEndpointAndCredential(t *testing.T) {
	t.Parallel()

	const credential = "credential-for-model-catalog-test" //nolint:gosec // Test server credential, never used externally.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Errorf("models request = %s %s, want GET /v1/models", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+credential {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "deepseek-v4-flash", "object": "model"},
				{"id": "deepseek-v4-pro", "object": "model"},
			},
		})
	}))
	defer server.Close()

	catalog, err := customBaseURLModelCatalog(context.Background(), Config{
		Auth:            AuthAPIKey,
		APIKey:          credential,
		BaseURL:         server.URL + "/v1/",
		Model:           "deepseek-v4-pro",
		ReasoningEffort: "high",
	}, server.Client())
	if err != nil {
		t.Fatalf("customBaseURLModelCatalog(): %v", err)
	}
	if catalog.ConfiguredModelID != "deepseek-v4-pro" || catalog.ConfiguredReasoningEffort != "high" {
		t.Fatalf("configured catalog values = (%q, %q)", catalog.ConfiguredModelID, catalog.ConfiguredReasoningEffort)
	}
	if len(catalog.Models) != 2 || catalog.Models[0].ID != "deepseek-v4-flash" || catalog.Models[1].ID != "deepseek-v4-pro" {
		t.Fatalf("models = %#v", catalog.Models)
	}
	if catalog.Models[0].Name != "deepseek-v4-flash" || catalog.Models[0].ReasoningEfforts == nil {
		t.Fatalf("normalized model = %#v", catalog.Models[0])
	}
}
