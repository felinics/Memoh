// Package grokcfg holds the Bot Agent configuration contract for the direct
// Grok Build runtime.
package grokcfg

import (
	"errors"
	"strings"
)

// AuthMode selects how the driver authenticates Grok Build.
type AuthMode string

const (
	// AuthAPIKey injects an xAI API key.
	AuthAPIKey AuthMode = "api_key"
	// AuthGrokAccount signs in with a Grok account. The login Grok wrote to
	// its home is kept in the encrypted Agent credential store and put back
	// before each process starts.
	AuthGrokAccount AuthMode = "grok_account"
)

// Permission modes are Grok Build's own three: ask before each side effect,
// let Grok decide what is safe, or approve everything.
const (
	PermissionAsk           = "ask"
	PermissionAuto          = "auto"
	PermissionAlwaysApprove = "always-approve"
)

var ErrNotConfigured = errors.New("grok build runtime is not configured for this Agent")

// Config is one Grok Build Bot Agent's runtime configuration.
type Config struct {
	Auth   AuthMode
	APIKey string //nolint:gosec // decrypted runtime credential, not a hardcoded secret
	// AuthJSON is the content of Grok's auth.json for a Grok account login.
	AuthJSON string //nolint:gosec // decrypted runtime credential, not a hardcoded secret
	// Per-turn model and reasoning overrides win over these values.
	Model           string
	ReasoningEffort string
	PermissionMode  string
}

func ParseAgentConfig(metadata map[string]any) (Config, error) {
	cfg := Config{
		Auth:            AuthMode(strings.TrimSpace(metadataString(metadata, "auth"))),
		Model:           strings.TrimSpace(metadataString(metadata, "model")),
		ReasoningEffort: strings.TrimSpace(metadataString(metadata, "reasoning_effort")),
		PermissionMode:  strings.TrimSpace(metadataString(metadata, "permission_mode")),
	}
	if (cfg.Auth != AuthAPIKey && cfg.Auth != AuthGrokAccount) || !ValidPermissionMode(cfg.PermissionMode) {
		return Config{}, ErrNotConfigured
	}
	return cfg, nil
}

// ValidPermissionMode also accepts an unset value, which means ask.
func ValidPermissionMode(mode string) bool {
	switch mode {
	case "", PermissionAsk, PermissionAuto, PermissionAlwaysApprove:
		return true
	default:
		return false
	}
}

func metadataString(meta map[string]any, key string) string {
	value, _ := meta[key].(string)
	return value
}
