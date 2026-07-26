// Package persistence defines the MCP connection and OAuth persistence ports
// and the records they exchange, separately from the services that consume them.
package persistence

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("mcp resource not found")

// ConnectionStore is the persistence boundary consumed by ConnectionService.
type ConnectionStore interface {
	ListConnections(ctx context.Context, botID string) ([]Connection, error)
	GetConnection(ctx context.Context, botID, connectionID string) (Connection, error)
	CreateConnection(ctx context.Context, input ConnectionWrite) (Connection, error)
	CreateManagedConnection(ctx context.Context, input ManagedConnectionWrite) (Connection, error)
	UpdateConnection(ctx context.Context, input ConnectionWrite) (Connection, error)
	UpsertConnectionByName(ctx context.Context, input ConnectionWrite) (Connection, error)
	DeleteConnection(ctx context.Context, botID, connectionID string) error
	SetPluginConnectionsActive(ctx context.Context, botID, installationID string, active bool) error
	DeletePluginConnections(ctx context.Context, botID, installationID string) error
	SaveConnectionProbe(ctx context.Context, input ConnectionProbeWrite) error
}

// ConnectionWrite carries validated connection data to persistence.
type ConnectionWrite struct {
	ID       string
	BotID    string
	Name     string
	Type     string
	Config   map[string]any
	Active   bool
	AuthType string
}

// ManagedConnectionWrite adds plugin ownership to a connection write.
type ManagedConnectionWrite struct {
	ConnectionWrite
	InstallationID string
	ResourceKey    string
	Visible        bool
	Metadata       map[string]any
}

// ConnectionProbeWrite is the persisted result of probing a connection.
type ConnectionProbeWrite struct {
	BotID         string
	ConnectionID  string
	Status        string
	Tools         []ToolDescriptor
	StatusMessage string
}

// OAuthStore is the persistence boundary consumed by OAuthService.
type OAuthStore interface {
	SaveDiscovery(ctx context.Context, connectionID string, result DiscoveryResult) error
	GetOAuthToken(ctx context.Context, connectionID string) (OAuthToken, error)
	GetOAuthTokenByState(ctx context.Context, state string) (OAuthToken, error)
	SavePKCEState(ctx context.Context, connectionID string, state PKCEState) error
	SaveClientSecret(ctx context.Context, connectionID, clientSecret string) error
	SaveTokens(ctx context.Context, connectionID string, tokens OAuthTokens) error
	SetConnectionAuthType(ctx context.Context, connectionID, authType string) error
	ClearTokens(ctx context.Context, connectionID string) error
}

// OAuthToken is the persisted state needed to run an MCP OAuth flow.
type OAuthToken struct {
	ConnectionID           string
	AuthorizationServerURL string
	AuthorizationEndpoint  string
	TokenEndpoint          string
	RegistrationEndpoint   string
	ScopesSupported        []string
	ClientID               string
	ClientSecret           string
	AccessToken            string
	RefreshToken           string
	ExpiresAt              *time.Time
	Scope                  string
	PKCECodeVerifier       string
	ResourceURI            string
	RedirectURI            string
}

// PKCEState is the temporary authorization state persisted before redirect.
type PKCEState struct {
	CodeVerifier string
	State        string
	ClientID     string
	RedirectURI  string
}

// OAuthTokens is the token response persisted after exchange or refresh.
type OAuthTokens struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	ExpiresAt    *time.Time
	Scope        string
}

// Connection represents a stored MCP connection for a bot.
type Connection struct {
	ID                            string           `json:"id"`
	BotID                         string           `json:"bot_id"`
	Name                          string           `json:"name"`
	Type                          string           `json:"type"`
	Config                        map[string]any   `json:"config"`
	Active                        bool             `json:"is_active"`
	Status                        string           `json:"status"`
	ToolsCache                    []ToolDescriptor `json:"tools_cache"`
	LastProbedAt                  *time.Time       `json:"last_probed_at,omitempty"`
	StatusMessage                 string           `json:"status_message"`
	AuthType                      string           `json:"auth_type"`
	ManagedByPluginInstallationID string           `json:"managed_by_plugin_installation_id,omitempty"`
	ManagedResourceKey            string           `json:"managed_resource_key,omitempty"`
	Visible                       bool             `json:"visible"`
	Metadata                      map[string]any   `json:"metadata,omitempty"`
	CreatedAt                     time.Time        `json:"created_at"`
	UpdatedAt                     time.Time        `json:"updated_at"`
}

// DiscoveryResult holds the result of an OAuth discovery flow.
type DiscoveryResult struct {
	ResourceMetadataURL    string   `json:"resource_metadata_url"`
	AuthorizationServerURL string   `json:"authorization_server_url"`
	AuthorizationEndpoint  string   `json:"authorization_endpoint"`
	TokenEndpoint          string   `json:"token_endpoint"`
	RegistrationEndpoint   string   `json:"registration_endpoint,omitempty"`
	ScopesSupported        []string `json:"scopes_supported,omitempty"`
	ResourceURI            string   `json:"resource_uri"`
}

// ToolDescriptor is the MCP tools/list item shape used by the gateway.
type ToolDescriptor struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
}
