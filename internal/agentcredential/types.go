package agentcredential

import "time"

const ( //nolint:gosec // Stable authentication-kind identifiers, not credential values.
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"

	AuthKindOpenAIAPIKey     = "openai_api_key" //nolint:gosec // Stable authentication-kind identifier.
	AuthKindOpenAICodexOAuth = "openai_codex_oauth"
	AuthKindAnthropicAPIKey  = "anthropic_api_key" //nolint:gosec // Stable authentication-kind identifier.
	AuthKindClaudeCodeOAuth  = "claude_code_oauth"
)

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
