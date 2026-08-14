package profile

import (
	"errors"
	"strings"
)

const (
	codexProbeClientType      = "openai-responses"
	codexProbeBaseURL         = "https://api.openai.com/v1"
	claudeCodeProbeClientType = "anthropic-messages"
	claudeCodeProbeBaseURL    = "https://api.anthropic.com/v1"
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
// key of setup, mirroring how each runtime consumes base_url: Codex writes it
// into config.toml verbatim including /v1 (wire_api "responses"), while Claude
// Code's ANTHROPIC_BASE_URL excludes /v1 (the agent appends /v1/... itself),
// so the probe appends /v1 to reach the same API surface.
func APIKeyProbeTarget(setup AgentSetup) (CredentialProbeTarget, error) {
	if normalizeSetupMode(setup.Mode, setup.Managed) != setupModeAPIKey {
		return CredentialProbeTarget{}, ErrCredentialProbeNotAPIKeyMode
	}
	baseURL := strings.TrimRight(strings.TrimSpace(setup.Managed["base_url"]), "/")
	var target CredentialProbeTarget
	switch NormalizeAgentID(setup.AgentID) {
	case AgentCodexID:
		target = CredentialProbeTarget{
			ClientType: codexProbeClientType,
			BaseURL:    codexProbeBaseURL,
		}
		if baseURL != "" {
			target.BaseURL = baseURL
		}
	case AgentClaudeCodeID:
		target = CredentialProbeTarget{
			ClientType: claudeCodeProbeClientType,
			BaseURL:    claudeCodeProbeBaseURL,
		}
		if baseURL != "" {
			target.BaseURL = baseURL + "/v1"
		}
	default:
		return CredentialProbeTarget{}, ErrCredentialProbeUnsupported
	}
	target.APIKey = strings.TrimSpace(setup.Managed["api_key"])
	if target.APIKey == "" {
		return CredentialProbeTarget{}, ErrCredentialProbeAPIKeyMissing
	}
	return target, nil
}
