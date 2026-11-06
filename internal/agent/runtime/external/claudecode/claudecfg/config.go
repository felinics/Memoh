// Package claudecfg holds the Bot Agent configuration contract for the direct
// Claude Code runtime.
package claudecfg

import (
	"errors"
	"slices"
	"strings"
)

// AuthMode selects how the driver authenticates Claude Code.
type AuthMode string

const (
	// AuthAPIKey injects an Anthropic API key.
	AuthAPIKey AuthMode = "api_key"
	// AuthClaudeAccount is a Claude account the Agent's own Claude Code signs
	// in to and keeps in the Agent's config directory in the workspace.
	AuthClaudeAccount AuthMode = "claude_account"
)

// ErrNotConfigured reports that the bot has no usable Claude Code configuration.
var ErrNotConfigured = errors.New("claude code runtime is not configured for this Agent")

// Config is one Claude Code Bot Agent's runtime configuration.
type Config struct {
	Auth    AuthMode
	APIKey  string //nolint:gosec // decrypted runtime credential, not a hardcoded secret
	BaseURL string
	// A per-turn model override wins over this value.
	Model          string
	PermissionMode string
}

func ParseAgentConfig(metadata map[string]any) (Config, error) {
	cfg := Config{
		Auth:           AuthMode(strings.TrimSpace(metadataString(metadata, "auth"))),
		BaseURL:        strings.TrimSpace(metadataString(metadata, "base_url")),
		Model:          strings.TrimSpace(metadataString(metadata, "model")),
		PermissionMode: strings.TrimSpace(metadataString(metadata, "permission_mode")),
	}
	if !ValidPermissionMode(cfg.PermissionMode) {
		return Config{}, ErrNotConfigured
	}
	switch cfg.Auth {
	case AuthAPIKey, AuthClaudeAccount:
	default:
		return Config{}, ErrNotConfigured
	}
	return cfg, nil
}

// PermissionModes is Claude Code's permission menu. inherit, the default,
// keeps the workspace's own configuration for existing Agents.
var PermissionModes = []string{"inherit", "default", "acceptEdits", "auto", "bypassPermissions"}

const DefaultPermissionMode = "inherit"

// ValidPermissionMode also accepts an unset value, which means the default.
func ValidPermissionMode(mode string) bool {
	return mode == "" || slices.Contains(PermissionModes, mode)
}

func metadataString(meta map[string]any, key string) string {
	value, _ := meta[key].(string)
	return value
}
