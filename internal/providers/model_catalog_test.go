package providers

import (
	"encoding/json"
	"testing"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func TestPublicCatalogSourceUsesExplicitSourceOrShippedTemplate(t *testing.T) {
	for _, tc := range []struct {
		metadata map[string]any
		want     string
	}{
		{map[string]any{"source": "deepseek"}, "deepseek"},
		{map[string]any{"registry": map[string]any{"source": "qwen.yaml"}}, "alibaba-cn"},
		{map[string]any{"registry": map[string]any{"source": "moonshot.yaml"}}, "moonshotai-cn"},
		{map[string]any{"preset": map[string]any{"source": "ollama.yaml"}}, ""},
		{map[string]any{"catalog": "azure-openai"}, ""},
		{map[string]any{"catalog": "deepseek"}, ""},
		{map[string]any{"registry": map[string]any{"source": "unknown.yaml"}}, ""},
	} {
		if got := PublicCatalogSource(tc.metadata); got != tc.want {
			t.Errorf("%v: got %q want %q", tc.metadata, got, tc.want)
		}
	}
}

func TestLegacyGoSubscriptionReadsItsCatalogSource(t *testing.T) {
	for _, tc := range []struct{ baseURL, metadata, want string }{
		{"https://OpenCode.ai/zen/go/v1/", `{}`, "opencode-go"},
		{"", `{}`, "opencode-go"},
		{"https://proxy.example/v1", `{}`, ""},
		{"", `{"source":"custom"}`, "custom"},
	} {
		config, _ := json.Marshal(map[string]string{"base_url": tc.baseURL})
		got := (&Service{}).toGetResponse(sqlc.Provider{ClientType: "opencode-go", Config: config, Metadata: []byte(tc.metadata)})
		if source := PublicCatalogSource(got.Metadata); source != tc.want {
			t.Errorf("%q %s: source = %q, want %q", tc.baseURL, tc.metadata, source, tc.want)
		}
	}
}
