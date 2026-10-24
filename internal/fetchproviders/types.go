package fetchproviders

import (
	"context"
	"time"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// Fetcher executes the configured provider without selecting a bot or changing its configuration.
type Fetcher interface {
	Fetch(context.Context, sqlc.FetchProvider, string, string) (any, error)
}

type ProviderName string

const (
	ProviderNative             ProviderName = "native"
	ProviderJina               ProviderName = "jina"
	ProviderDeepSeek           ProviderName = "deepseek"
	ProviderCloudflareMarkdown ProviderName = "cloudflare_markdown"
)

type ProviderConfigSchema struct {
	Fields map[string]ProviderFieldSchema `json:"fields"`
}

type ProviderFieldSchema struct {
	Type        string   `json:"type"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Example     any      `json:"example,omitempty"`
}

type ProviderMeta struct {
	Provider     string               `json:"provider"`
	DisplayName  string               `json:"display_name"`
	ConfigSchema ProviderConfigSchema `json:"config_schema"`
}

type TestRequest struct {
	URL string `json:"url"`
}

type CreateRequest struct {
	Name     string         `json:"name"`
	Provider ProviderName   `json:"provider"`
	Config   map[string]any `json:"config,omitempty"`
}

type UpdateRequest struct {
	Name     *string        `json:"name,omitempty"`
	Provider *ProviderName  `json:"provider,omitempty"`
	Config   map[string]any `json:"config,omitempty"`
	Enable   *bool          `json:"enable,omitempty"`
}

type GetResponse struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Provider  string         `json:"provider"`
	Config    map[string]any `json:"config,omitempty"`
	Enable    bool           `json:"enable"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}
