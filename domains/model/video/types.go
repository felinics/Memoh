package video

import "time"

type FieldSchema struct {
	Key         string   `json:"key"`
	Type        string   `json:"type"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Advanced    bool     `json:"advanced,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Example     any      `json:"example,omitempty"`
	Order       int      `json:"order"`
}

type ConfigSchema struct {
	Fields []FieldSchema `json:"fields"`
}

type ModelInfo struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Description  string       `json:"description,omitempty"`
	ConfigSchema ConfigSchema `json:"config_schema,omitempty"`
}

type ProviderMetaResponse struct {
	Provider     string       `json:"provider"`
	DisplayName  string       `json:"display_name"`
	Description  string       `json:"description"`
	ConfigSchema ConfigSchema `json:"config_schema,omitempty"`
	DefaultModel string       `json:"default_model,omitempty"`
	Models       []ModelInfo  `json:"models,omitempty"`
	SupportsList bool         `json:"supports_list,omitempty"`
}

type ProviderResponse struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	ClientType string         `json:"client_type"`
	Icon       string         `json:"icon,omitempty"`
	Enable     bool           `json:"enable"`
	Config     map[string]any `json:"config,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

type ModelResponse struct {
	ID           string         `json:"id"`
	ModelID      string         `json:"model_id"`
	Name         string         `json:"name"`
	ProviderID   string         `json:"provider_id"`
	ProviderType string         `json:"provider_type,omitempty"`
	Config       map[string]any `json:"config,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

type UpdateModelRequest struct {
	Name   *string        `json:"name,omitempty"`
	Config map[string]any `json:"config,omitempty"`
}

type ImportModelsResponse struct {
	Created int      `json:"created"`
	Skipped int      `json:"skipped"`
	Models  []string `json:"models"`
}
