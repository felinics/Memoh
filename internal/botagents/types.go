package botagents

import (
	"time"

	"github.com/felinics/memoh/internal/runtimekind"
)

// BotAgent runtimes: RuntimeACP is bot-agent-domain vocabulary (the ACP row
// kind, distinct from the session runtime "acp_agent"); the direct kinds
// are pinned to the shared runtime vocabulary — a direct agent's runtime IS
// its session runtime type.
const (
	RuntimeACP          = "acp"
	RuntimeCodex        = string(runtimekind.Codex)
	RuntimeClaudeCode   = string(runtimekind.ClaudeCode)
	MetadataProviderKey = "provider"
)

// BotAgent is a user-managed Agent entry attached to a bot. Native is the
// built-in fallback and is intentionally represented by the absence of a row.
type BotAgent struct {
	ID      string `json:"id"`
	BotID   string `json:"bot_id"`
	Name    string `json:"name"`
	Runtime string `json:"runtime"`
	Enabled bool   `json:"enabled"`
	// AgentCredentialID points at the encrypted credential this instance uses;
	// empty means not connected (legacy metadata path).
	AgentCredentialID string         `json:"agent_credential_id,omitempty"`
	Metadata          map[string]any `json:"metadata"`
	// Dependency is the workspace dependency the agent's runtime declares
	// (design §9.3). It is derived from the driver at read time, never
	// persisted, and omitted for runtimes without a declaration (ACP).
	Dependency *DependencyRequirement `json:"dependency,omitempty"`
	CreatedAt  time.Time              `json:"created_at"`
	UpdatedAt  time.Time              `json:"updated_at"`
	DeletedAt  *time.Time             `json:"deleted_at,omitempty"`
}

// DependencyRequirement names the managed workspace dependency a direct
// runtime needs. No version is declared: the dependency manager installs
// whatever version the user asks for (latest by default). The web preflight
// (POST /bots/{bot_id}/dependencies/preflight) keys on the id.
type DependencyRequirement struct {
	DependencyID string `json:"dependency_id"`
}

type CreateRequest struct {
	Name    string `json:"name"`
	Runtime string `json:"runtime"`
	// Enabled defaults to true when omitted. The web passes false for direct
	// runtimes so the dependency preflight runs before the agent goes live.
	Enabled  *bool          `json:"enabled,omitempty"`
	Metadata map[string]any `json:"metadata"`
}

type UpdateRequest struct {
	Name     *string        `json:"name,omitempty"`
	Enabled  *bool          `json:"enabled,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type ListResponse struct {
	Items []BotAgent `json:"items"`
}

// Descriptor is the stable runtime selection projected into a session. The
// provider is temporary ACP compatibility metadata, not a first-class model.
type Descriptor struct {
	BotAgentID string
	Runtime    string
	Provider   string
}
