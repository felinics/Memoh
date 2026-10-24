package providers

import "testing"

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
