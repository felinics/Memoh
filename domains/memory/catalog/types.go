package catalog

import "time"

type ProviderType string

const (
	ProviderBuiltin    ProviderType = "builtin"
	ProviderMem0       ProviderType = "mem0"
	ProviderOpenViking ProviderType = "openviking"
)

type ProviderCreateRequest struct {
	Name     string         `json:"name"`
	Provider ProviderType   `json:"provider"`
	Config   map[string]any `json:"config,omitempty"`
}

type ProviderUpdateRequest struct {
	Name   *string        `json:"name,omitempty"`
	Config map[string]any `json:"config,omitempty"`
}

type ProviderGetResponse struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Provider  string         `json:"provider"`
	Config    map[string]any `json:"config,omitempty"`
	IsDefault bool           `json:"is_default"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type ProviderConfigSchema struct {
	Fields map[string]ProviderFieldSchema `json:"fields"`
}

type ProviderFieldSchema struct {
	Type        string `json:"type"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
	Example     any    `json:"example,omitempty"`
}

type ProviderMeta struct {
	Provider     string               `json:"provider"`
	DisplayName  string               `json:"display_name"`
	ConfigSchema ProviderConfigSchema `json:"config_schema"`
}

type ProviderCollectionStatus struct {
	Name   string       `json:"name"`
	Exists bool         `json:"exists"`
	Points int          `json:"points"`
	Health HealthStatus `json:"health"`
}

type HealthStatus struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type ProviderStatusResponse struct {
	ProviderType     string                     `json:"provider_type"`
	MemoryMode       string                     `json:"memory_mode,omitempty"`
	EmbeddingModelID string                     `json:"embedding_model_id,omitempty"`
	Collections      []ProviderCollectionStatus `json:"collections,omitempty"`
}
