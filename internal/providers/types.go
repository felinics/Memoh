package providers

import (
	"time"

	"github.com/felinics/memoh/internal/models"
)

// CreateRequest represents a request to create a new provider.
type CreateRequest struct {
	Name       string         `json:"name" validate:"required"`
	ClientType string         `json:"client_type" validate:"required"`
	Icon       string         `json:"icon,omitempty"`
	Config     map[string]any `json:"config,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

type CreateFromTemplateRequest struct {
	TemplateID string         `json:"template_id" validate:"required"`
	Domain     string         `json:"domain,omitempty"`
	Name       string         `json:"name,omitempty"`
	Config     map[string]any `json:"config,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

// UpdateRequest represents a request to update an existing provider.
type UpdateRequest struct {
	Name       *string        `json:"name,omitempty"`
	ClientType *string        `json:"client_type,omitempty"`
	Icon       *string        `json:"icon,omitempty"`
	Enable     *bool          `json:"enable,omitempty"`
	Config     map[string]any `json:"config,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

// GetResponse represents the response for getting a provider.
type GetResponse struct {
	ID                 string         `json:"id"`
	ProviderTemplateID string         `json:"provider_template_id,omitempty"`
	Name               string         `json:"name"`
	ClientType         string         `json:"client_type"`
	Icon               string         `json:"icon,omitempty"`
	Enable             bool           `json:"enable"`
	Config             map[string]any `json:"config,omitempty"`
	Metadata           map[string]any `json:"metadata,omitempty"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
}

// ListResponse represents the response for listing providers.
type ListResponse struct {
	Providers []GetResponse `json:"providers"`
	Total     int64         `json:"total"`
}

// CountResponse represents the count response.
type CountResponse struct {
	Count int64 `json:"count"`
}

// TestStatus represents the outcome of testing a provider.
type TestStatus string

const (
	TestStatusOK        TestStatus = "ok"
	TestStatusAuthError TestStatus = "auth_error"
	TestStatusError     TestStatus = "error"
	// TestStatusUnverified means the endpoint answered but the models list
	// could not confirm it works (e.g. the provider does not implement model
	// listing). It is deliberately not a failure: only a real generation
	// against a concrete model can settle that (see models.Service.Test).
	TestStatusUnverified TestStatus = "unverified"
)

// TestResponse is returned by POST /providers/:id/test.
type TestResponse struct {
	Status    TestStatus `json:"status"`
	Reachable bool       `json:"reachable"`
	LatencyMs int64      `json:"latency_ms,omitempty"`
	Message   string     `json:"message,omitempty"`
}

// OAuthStatus is returned by GET /providers/:id/oauth/status.
type OAuthStatus struct {
	Configured  bool               `json:"configured"`
	Mode        string             `json:"mode,omitempty"`
	HasToken    bool               `json:"has_token"`
	Expired     bool               `json:"expired"`
	ExpiresAt   *time.Time         `json:"expires_at,omitempty"`
	CallbackURL string             `json:"callback_url"`
	Device      *OAuthDeviceStatus `json:"device,omitempty"`
	Account     *OAuthAccount      `json:"account,omitempty"`
}

type OAuthDeviceStatus struct {
	Pending         bool       `json:"pending"`
	UserCode        string     `json:"user_code,omitempty"`
	VerificationURI string     `json:"verification_uri,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	IntervalSeconds int64      `json:"interval_seconds,omitempty"`
}

type OAuthAccount struct {
	Label      string `json:"label,omitempty"`
	Login      string `json:"login,omitempty"`
	Name       string `json:"name,omitempty"`
	Email      string `json:"email,omitempty"`
	AvatarURL  string `json:"avatar_url,omitempty"`
	ProfileURL string `json:"profile_url,omitempty"`
}

type OAuthAuthorizeResponse struct {
	Mode    string             `json:"mode,omitempty"`
	AuthURL string             `json:"auth_url,omitempty"`
	Device  *OAuthDeviceStatus `json:"device,omitempty"`
}

// RemoteModel represents a model returned by the provider's /v1/models endpoint.
type RemoteModel struct {
	ID               string   `json:"id"`
	Description      *string  `json:"description,omitempty"`
	Object           string   `json:"object"`
	Created          int64    `json:"created"`
	OwnedBy          string   `json:"owned_by"`
	Name             string   `json:"name,omitempty"`
	DisplayName      string   `json:"display_name,omitempty"`
	Type             string   `json:"type,omitempty"`
	Compatibilities  []string `json:"compatibilities,omitempty"`
	ReasoningEfforts []string `json:"reasoning_efforts,omitempty"`
	ThinkingMode     string   `json:"thinking_mode,omitempty"`
	// ReasoningDialect and the budget bounds must survive the import path, not
	// just live in the template: an imported Gemini 2.5 row without a dialect
	// falls back to the tier wire and every request is a 400.
	ReasoningDialect string `json:"reasoning_dialect,omitempty"`
	// ReasoningOffSupport declares how the model answers an explicit disable.
	ReasoningOffSupport string `json:"reasoning_off_support,omitempty"`
	ReasoningDefaultOn  *bool  `json:"reasoning_default_on,omitempty"`
	ThinkingBudgetMin   *int   `json:"thinking_budget_min,omitempty"`
	ThinkingBudgetMax   *int   `json:"thinking_budget_max,omitempty"`
	ContextWindow       *int   `json:"context_window,omitempty"`
	Dimensions          *int   `json:"dimensions,omitempty"`
	CapabilitiesKnown   bool   `json:"-"`
}

// ImportModelsRequest carries caller-supplied capability defaults for chat
// models whose provider cannot describe them. Templates and trusted discovery
// remain authoritative.
type ImportModelsRequest struct {
	DefaultCompatibilities []string `json:"default_compatibilities,omitempty"`
}

// ImportModelsResponse represents the response for importing models.
type ImportModelsResponse struct {
	Created int      `json:"created"`
	Updated int      `json:"updated"`
	Skipped int      `json:"skipped"`
	Models  []string `json:"models"`
}

// IsManagedModelCatalogClientType reports whether model rows are reconciled
// against an account-scoped upstream catalog instead of manually maintained.
func IsManagedModelCatalogClientType(clientType models.ClientType) bool {
	return clientType == models.ClientTypeOpenAICodex || clientType == models.ClientTypeGitHubCopilot
}
