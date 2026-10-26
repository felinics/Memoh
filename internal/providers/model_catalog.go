package providers

import (
	"maps"
	"strings"

	"github.com/felinics/memoh/internal/models"
)

const openCodeGoBaseURL = "https://opencode.ai/zen/go/v1"

// A Go subscription added in the custom form before that form recorded the
// gallery entry has no source. On the official endpoint it is that
// subscription, so it imports the catalog like one added from the gallery; the
// web matches it the same way (subscriptionForProvider).
func withLegacyGoSource(clientType string, config, metadata map[string]any) map[string]any {
	if clientType != string(models.ClientTypeOpenCodeGo) || PublicCatalogSource(metadata) != "" {
		return metadata
	}
	if base := strings.TrimRight(strings.TrimSpace(configString(config, "base_url")), "/"); base != "" && !strings.EqualFold(base, openCodeGoBaseURL) {
		return metadata
	}
	metadata = maps.Clone(metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["source"] = "opencode-go"
	return metadata
}

// PublicCatalogSource reads the explicit source or the identity persisted by a
// shipped YAML template. Gallery IDs are presentation metadata, not source IDs.
func PublicCatalogSource(metadata map[string]any) string {
	if source, _ := metadata["source"].(string); strings.TrimSpace(source) != "" {
		return strings.TrimSpace(source)
	}
	for _, key := range []string{"preset", "registry"} {
		if entry, ok := metadata[key].(map[string]any); ok {
			if name, ok := entry["source"].(string); ok {
				return legacyCatalogSources[strings.TrimSuffix(strings.TrimSpace(name), ".yaml")]
			}
		}
	}
	return ""
}

// Only shipped templates with public sources belong here. Local endpoints,
// deployment-name providers and account-scoped catalogs use endpoint discovery.
var legacyCatalogSources = map[string]string{
	"anthropic": "anthropic", "cerebras": "cerebras", "cloudflare": "cloudflare-workers-ai",
	"deepseek": "deepseek", "fireworks": "fireworks-ai", "google": "google", "groq": "groq",
	"huggingface": "huggingface", "minimax": "minimax", "mistral": "mistral", "moonshot": "moonshotai-cn",
	"openai": "openai", "opencode-go": "opencode-go", "openrouter": "openrouter", "perplexity": "perplexity",
	"qwen": "alibaba-cn", "together": "togetherai", "xai": "xai", "zai": "zai", "zhipu": "zhipuai",
}

// PublicCatalogManaged is an additional boundary for the public import path.
// Cloud keeps its own complete managed-provider guard and authenticated sync.
func PublicCatalogManaged(metadata map[string]any) bool {
	managed, _ := metadata["managed"].(bool)
	source, _ := metadata["source"].(string)
	return managed || strings.EqualFold(strings.TrimSpace(source), "builtin")
}
