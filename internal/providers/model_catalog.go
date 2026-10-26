package providers

import (
	"maps"
	"net/url"
	"strings"

	"github.com/felinics/memoh/internal/models"
)

// Older custom-form Go subscriptions have no catalog identity. Recover it only
// for the official endpoint (or the SDK default), so Refresh Models can import
// the limits that Go's ID-only /models response cannot supply. A custom proxy
// must opt in with a source; sharing a wire protocol is not a catalog identity.
func subscriptionCatalogMetadata(clientType string, config, metadata map[string]any) map[string]any {
	if clientType != string(models.ClientTypeOpenCodeGo) || PublicCatalogManaged(metadata) || PublicCatalogSource(metadata) != "" {
		return metadata
	}
	baseURL := strings.TrimSpace(configString(config, "base_url"))
	if baseURL != "" {
		u, err := url.Parse(baseURL)
		if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "opencode.ai") ||
			(u.Port() != "" && u.Port() != "443") || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
			strings.TrimRight(u.Path, "/") != "/zen/go/v1" {
			return metadata
		}
	}
	metadata = maps.Clone(metadata)
	if metadata == nil {
		metadata = make(map[string]any)
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
