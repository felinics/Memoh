package agentcredential

import (
	"time"

	"github.com/felinics/memoh/internal/runtimekind"
)

const ( //nolint:gosec // Stable authentication-kind identifiers, not credential values.
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"
	ProviderXAI       = "xai"

	AuthKindOpenAIAPIKey    = "openai_api_key"    //nolint:gosec // Stable authentication-kind identifier.
	AuthKindAnthropicAPIKey = "anthropic_api_key" //nolint:gosec // Stable authentication-kind identifier.
	AuthKindClaudeCodeOAuth = "claude_code_oauth"
	AuthKindXAIAPIKey       = "xai_api_key" //nolint:gosec // Stable authentication-kind identifier.
	AuthKindXAIGrokOAuth    = "xai_grok_oauth"
)

// authKinds keeps what an auth kind means in one table so a new credential
// kind is one row, not an edit to every switch over kinds.
type authKindSpec struct {
	provider string
	runtime  runtimekind.Kind
	secret   []string
	label    string
}

var authKinds = map[string]authKindSpec{
	AuthKindOpenAIAPIKey:    {provider: ProviderOpenAI, runtime: runtimekind.Codex, secret: []string{"api_key"}, label: "API key"},
	AuthKindAnthropicAPIKey: {provider: ProviderAnthropic, runtime: runtimekind.ClaudeCode, secret: []string{"api_key"}, label: "API key"},
	AuthKindClaudeCodeOAuth: {provider: ProviderAnthropic, runtime: runtimekind.ClaudeCode, secret: []string{"oauth_token"}, label: "Claude Code"},
	AuthKindXAIAPIKey:       {provider: ProviderXAI, runtime: runtimekind.GrokBuild, secret: []string{"api_key"}, label: "API key"},
	// Grok's login file is stored whole and only Grok writes it: the sign-in
	// runs in the Agent's process, and Grok rewrites the file when it
	// refreshes its tokens, so the stored copy has to take whatever it wrote.
	AuthKindXAIGrokOAuth: {provider: ProviderXAI, runtime: runtimekind.GrokBuild, secret: []string{"auth_json"}, label: "Grok"},
}

// PublicCredential is the redacted view of a stored credential. The secret
// never leaves the service; only labels and account metadata do.
type PublicCredential struct {
	ID                string         `json:"id"`
	OwnerUserID       string         `json:"owner_user_id"`
	Provider          string         `json:"provider"`
	AuthKind          string         `json:"auth_kind"`
	Label             string         `json:"label"`
	AccountMetadata   map[string]any `json:"account_metadata,omitempty"`
	ExpiresAt         *time.Time     `json:"expires_at,omitempty"`
	CredentialVersion int64          `json:"credential_version"`
	Revoked           bool           `json:"revoked"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

// CreateRequest carries a new secret into the store. Label may be empty; the
// service derives one from the auth kind when absent.
type CreateRequest struct {
	Provider        string            `json:"provider"`
	AuthKind        string            `json:"auth_kind"`
	Label           string            `json:"label,omitempty"`
	Secret          map[string]string `json:"secret"`
	AccountMetadata map[string]any    `json:"account_metadata,omitempty"`
	ExpiresAt       *time.Time        `json:"expires_at,omitempty"`
}

type ResolvedCredential struct {
	PublicCredential
	AgentRuntime string
	Secret       map[string]string
}
