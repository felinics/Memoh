package codex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/external"
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

func TestApplyNativeReasoningFollowsCodexModelLookup(t *testing.T) {
	t.Parallel()

	type want struct {
		defaultEffort string
		efforts       int
	}
	native := []external.ModelOption{
		{ID: "gpt-5.4", DefaultReasoningEffort: "medium", ReasoningEfforts: []external.ReasoningEffortOption{{ID: "low"}, {ID: "medium"}}},
		{ID: "gpt-5.4-mini", DefaultReasoningEffort: "low", ReasoningEfforts: []external.ReasoningEffortOption{{ID: "low"}}},
	}
	// A model Codex cannot place gets every generic level and no default, so
	// nothing is sent until the user picks one.
	unplaced := want{efforts: len(genericReasoningEfforts)}
	cases := map[string]want{
		"gpt-5.4":              {defaultEffort: "medium", efforts: 2},
		"gpt-5.4-mini-2026":    {defaultEffort: "low", efforts: 1},
		"openai/gpt-5.4-codex": {defaultEffort: "medium", efforts: 2},
		"deepseek-v4-pro":      unplaced,
		"relay/openai/gpt-5.4": unplaced,
		"open ai/gpt-5.4":      unplaced,
	}
	for id, want := range cases {
		models := []external.ModelOption{{ID: id}}
		applyNativeReasoning(models, native)
		if got := models[0]; got.DefaultReasoningEffort != want.defaultEffort || len(got.ReasoningEfforts) != want.efforts {
			t.Errorf("%s: default = %q, efforts = %#v", id, got.DefaultReasoningEffort, got.ReasoningEfforts)
		}
	}
}
