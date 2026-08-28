package agentcredential

import "time"

const ( //nolint:gosec // Stable authentication-kind identifiers, not credential values.
	ProviderOpenAI     = "openai"
	ProviderAnthropic  = "anthropic"
	ProviderGoogle     = "google"
	ProviderOpenRouter = "openrouter"

	AuthKindOpenAIAPIKey     = "openai_api_key" //nolint:gosec // Stable authentication-kind identifier.
	AuthKindOpenAICodexOAuth = "openai_codex_oauth"
	AuthKindAnthropicAPIKey  = "anthropic_api_key" //nolint:gosec // Stable authentication-kind identifier.
	AuthKindClaudeCodeOAuth  = "claude_code_oauth"
	AuthKindGoogleAPIKey     = "google_api_key"
	AuthKindOpenRouterAPIKey = "openrouter_api_key" //nolint:gosec // Stable authentication-kind identifier.
)

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
	IsDefault         bool           `json:"is_default,omitempty"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

type CredentialList struct {
	Items []PublicCredential `json:"items"`
}

type CreateRequest struct {
	Provider        string            `json:"provider"`
	AuthKind        string            `json:"auth_kind"`
	Label           string            `json:"label"`
	Secret          map[string]string `json:"secret"`
	AccountMetadata map[string]any    `json:"account_metadata,omitempty"`
	ExpiresAt       *time.Time        `json:"expires_at,omitempty"`
}

type UpdateRequest struct {
	Label string `json:"label"`
}

type BindRequest struct {
	CredentialID string `json:"credential_id"`
	MakeDefault  bool   `json:"make_default,omitempty"`
}

type SetDefaultRequest struct {
	CredentialID string `json:"credential_id"`
}

type ResolvedCredential struct {
	PublicCredential
	Secret map[string]string
}

type BindingTarget struct {
	BotID   string
	AgentID string
}
