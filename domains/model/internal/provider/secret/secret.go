package secret

import (
	"strings"

	modeldomain "github.com/memohai/memoh/domains/model"
)

const OAuthClientSecretKey = "oauth_client_secret" //nolint:gosec // Metadata key name, not a credential literal.

// Clone returns a shallow copy of a provider config map.
func Clone(cfg map[string]any) map[string]any {
	result := make(map[string]any, len(cfg))
	for k, v := range cfg {
		result[k] = v
	}
	return result
}

// Merge overlays incoming onto existing without mutating either map.
func Merge(existing, incoming map[string]any) map[string]any {
	result := Clone(existing)
	for k, v := range incoming {
		result[k] = v
	}
	return result
}

// ConfigString reads a string-valued config key.
func ConfigString(cfg map[string]any, key string) string {
	if cfg == nil {
		return ""
	}
	value, _ := cfg[key].(string)
	return strings.TrimSpace(value)
}

// PreserveMasked keeps the existing secret when the incoming value is the masked placeholder.
func PreserveMasked(merged, existing, incoming map[string]any, key string) {
	existingValue := ConfigString(existing, key)
	newValue := ConfigString(incoming, key)
	if existingValue == "" || newValue == "" {
		return
	}
	if newValue == MaskAPIKey(existingValue) {
		merged[key] = existingValue
	}
}

// Normalize keeps provider-specific secrets under stable keys while preserving
// backward compatibility for legacy stored configs.
func Normalize(clientType string, cfg map[string]any) map[string]any {
	result := Clone(cfg)
	if modeldomain.ClientType(clientType) == modeldomain.ClientTypeGitHubCopilot {
		delete(result, "api_key")
		delete(result, OAuthClientSecretKey)
	}
	return result
}

// MaskConfig returns a copy of config with known secret fields masked.
func MaskConfig(clientType string, cfg map[string]any) map[string]any {
	result := Normalize(clientType, cfg)
	for _, key := range []string{"api_key", OAuthClientSecretKey} {
		if value, _ := result[key].(string); value != "" {
			result[key] = MaskAPIKey(value)
		}
	}
	return result
}

// MaskAPIKey masks an API key for security.
func MaskAPIKey(apiKey string) string {
	if apiKey == "" {
		return ""
	}
	if len(apiKey) <= 8 {
		return strings.Repeat("*", len(apiKey))
	}
	return apiKey[:8] + strings.Repeat("*", len(apiKey)-8)
}
