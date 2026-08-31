// Package codexcfg holds the Bot Agent configuration contract for the direct
// codex runtime.
package codexcfg

import (
	"errors"
	"strings"
)

// AuthMode selects how the driver authenticates codex.
type AuthMode string

const (
	// AuthAPIKey logs in with an OpenAI API key from bot configuration.
	AuthAPIKey AuthMode = "api_key"
	// AuthChatGPT uses a ChatGPT subscription login from the encrypted Agent
	// credential store, materialized into CODEX_HOME for the Codex process.
	AuthChatGPT AuthMode = "chatgpt"
)

var ErrNotConfigured = errors.New("codex runtime is not configured for this Agent")

// Config is one Codex Bot Agent's runtime configuration.
type Config struct {
	Auth    AuthMode
	APIKey  string //nolint:gosec // decrypted runtime credential, not a hardcoded secret
	BaseURL string
	// Per-turn model and reasoning overrides win over these values.
	Model           string
	ReasoningEffort string
}

func ParseAgentConfig(metadata map[string]any) (Config, error) {
	cfg := Config{
		Auth:            AuthMode(strings.TrimSpace(metadataString(metadata, "auth"))),
		BaseURL:         strings.TrimSpace(metadataString(metadata, "base_url")),
		Model:           strings.TrimSpace(metadataString(metadata, "model")),
		ReasoningEffort: strings.TrimSpace(metadataString(metadata, "reasoning_effort")),
	}
	switch cfg.Auth {
	case AuthAPIKey, AuthChatGPT:
	default:
		return Config{}, ErrNotConfigured
	}
	return cfg, nil
}

func metadataString(meta map[string]any, key string) string {
	value, _ := meta[key].(string)
	return value
}
