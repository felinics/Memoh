package profile

import (
	"errors"
	"strings"
)

const (
	codexProbeClientType      = "openai-responses"
	codexProbeBaseURL         = "https://api.openai.com/v1"
	claudeCodeProbeClientType = "anthropic-messages"
	claudeCodeProbeBaseURL    = "https://api.anthropic.com"
)

var (
	ErrCredentialProbeUnsupported   = errors.New("acp agent does not support api key credential testing")
	ErrCredentialProbeNotAPIKeyMode = errors.New("acp agent is not configured with api key setup")
	ErrCredentialProbeAPIKeyMissing = errors.New("acp agent api key is not configured")
)

// CredentialProbeTarget describes the provider endpoint that validates an ACP
// agent's managed API key without starting the agent process.
type CredentialProbeTarget struct {
	ClientType string
	BaseURL    string
	APIKey     string //nolint:gosec // runtime credential material used to construct SDK providers
}

// APIKeyProbeTarget resolves the endpoint probed to validate the managed API
// key of setup. The defaults mirror what the runtime materializes for each
// agent: Codex writes base_url into config.toml (wire_api "responses") and
// Claude Code only overrides ANTHROPIC_BASE_URL when base_url is set.
func APIKeyProbeTarget(setup AgentSetup) (CredentialProbeTarget, error) {
	if normalizeSetupMode(setup.Mode, setup.Managed) != setupModeAPIKey {
		return CredentialProbeTarget{}, ErrCredentialProbeNotAPIKeyMode
	}
	var target CredentialProbeTarget
	switch NormalizeAgentID(setup.AgentID) {
	case AgentCodexID:
		target = CredentialProbeTarget{
			ClientType: codexProbeClientType,
			BaseURL:    codexProbeBaseURL,
		}
	case AgentClaudeCodeID:
		target = CredentialProbeTarget{
			ClientType: claudeCodeProbeClientType,
			BaseURL:    claudeCodeProbeBaseURL,
		}
	default:
		return CredentialProbeTarget{}, ErrCredentialProbeUnsupported
	}
	target.APIKey = strings.TrimSpace(setup.Managed["api_key"])
	if target.APIKey == "" {
		return CredentialProbeTarget{}, ErrCredentialProbeAPIKeyMissing
	}
	if baseURL := strings.TrimRight(strings.TrimSpace(setup.Managed["base_url"]), "/"); baseURL != "" {
		target.BaseURL = baseURL
	}
	return target, nil
}
