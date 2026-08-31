// Package claudecfg holds the Bot Agent configuration contract for the direct
// Claude Code runtime.
package claudecfg

import (
	"errors"
	"strings"
)

// AuthMode selects how the driver authenticates Claude Code.
type AuthMode string

const (
	// AuthAPIKey injects an Anthropic API key.
	AuthAPIKey AuthMode = "api_key"
	// AuthOAuthToken injects a Claude subscription OAuth token
	// (claude setup-token / claude.ai authorization).
	AuthOAuthToken AuthMode = "oauth_token" //nolint:gosec // mode name, not a credential
	// AuthWorkspace injects no credential: the CLI reads whatever the
	// workspace's CLAUDE_CONFIG_DIR already holds (e.g. credentials
	// established by a previous self-managed login).
	AuthWorkspace AuthMode = "workspace"
)

// ErrNotConfigured reports that the bot has no usable Claude Code configuration.
var ErrNotConfigured = errors.New("claude code runtime is not configured for this Agent")

// Config is one Claude Code Bot Agent's runtime configuration.
type Config struct {
	Auth       AuthMode
	APIKey     string //nolint:gosec // decrypted runtime credential, not a hardcoded secret
	OAuthToken string //nolint:gosec // decrypted runtime credential, not a hardcoded secret
	BaseURL    string
	// A per-turn model override wins over this value.
	Model string
}

func ParseAgentConfig(metadata map[string]any) (Config, error) {
	cfg := Config{
		Auth:    AuthMode(strings.TrimSpace(metadataString(metadata, "auth"))),
		BaseURL: strings.TrimSpace(metadataString(metadata, "base_url")),
		Model:   strings.TrimSpace(metadataString(metadata, "model")),
	}
	switch cfg.Auth {
	case AuthAPIKey, AuthOAuthToken, AuthWorkspace:
	default:
		return Config{}, ErrNotConfigured
	}
	return cfg, nil
}

func metadataString(meta map[string]any, key string) string {
	value, _ := meta[key].(string)
	return value
}
