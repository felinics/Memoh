package catalog

import (
	"strings"

	modeldomain "github.com/memohai/memoh/domains/model"
)

func normalizeModelConfig(config modeldomain.ModelConfig) modeldomain.ModelConfig {
	if config.Description != nil {
		description := strings.TrimSpace(*config.Description)
		config.Description = &description
	}
	return config
}

// ResolveEnable returns the effective enable flag: when the override is nil,
// the current value is preserved; otherwise the override wins. Used by
// Service.Create (current=true default) and Service.UpdateByID (current=stored).
func ResolveEnable(override *bool, current bool) bool {
	if override == nil {
		return current
	}
	return *override
}

// AddRequest is the payload for creating a new model. Enable is a pointer so
// the server can default to true when the field is absent from the request.
type AddRequest struct {
	ModelID    string                  `json:"model_id"`
	Name       string                  `json:"name,omitempty"`
	ProviderID string                  `json:"provider_id"`
	Type       modeldomain.ModelType   `json:"type"`
	Enable     *bool                   `json:"enable,omitempty"`
	Config     modeldomain.ModelConfig `json:"config"`
}

type AddResponse struct {
	ID      string `json:"id"`
	ModelID string `json:"model_id"`
}

type GetRequest struct {
	ID string `json:"id"`
}

type GetResponse struct {
	ID      string `json:"id"`
	ModelID string `json:"model_id"`
	modeldomain.Model
}

// UpdateRequest is the payload for updating an existing model. Enable is a
// pointer so callers can omit it to preserve the current enable state while
// still rewriting the other fields.
type UpdateRequest struct {
	ModelID    string                  `json:"model_id"`
	Name       string                  `json:"name,omitempty"`
	ProviderID string                  `json:"provider_id"`
	Type       modeldomain.ModelType   `json:"type"`
	Enable     *bool                   `json:"enable,omitempty"`
	Config     modeldomain.ModelConfig `json:"config"`
}

// toModel builds a Model from an AddRequest using the given enable value.
func (r AddRequest) toModel(enable bool) modeldomain.Model {
	return modeldomain.Model{
		ModelID:    r.ModelID,
		Name:       r.Name,
		ProviderID: r.ProviderID,
		Type:       r.Type,
		Enable:     enable,
		Config:     r.Config,
	}
}

// toModel builds a Model from an UpdateRequest using the given enable value.
func (r UpdateRequest) toModel(enable bool) modeldomain.Model {
	return modeldomain.Model{
		ModelID:    r.ModelID,
		Name:       r.Name,
		ProviderID: r.ProviderID,
		Type:       r.Type,
		Enable:     enable,
		Config:     r.Config,
	}
}

type ListRequest struct {
	Type modeldomain.ModelType `json:"type,omitempty"`
}

type DeleteRequest struct {
	ID      string `json:"id,omitempty"`
	ModelID string `json:"model_id,omitempty"`
}

type DeleteResponse struct {
	Message string `json:"message"`
}

type CountResponse struct {
	Count int64 `json:"count"`
}

// TestStatus represents the outcome of probing a model.
type TestStatus string

const (
	TestStatusOK                TestStatus = "ok"
	TestStatusAuthError         TestStatus = "auth_error"
	TestStatusModelNotSupported TestStatus = "model_not_supported"
	TestStatusError             TestStatus = "error"
)

// TestResponse is returned by POST /models/:id/test.
type TestResponse struct {
	Status    TestStatus `json:"status"`
	Reachable bool       `json:"reachable"`
	LatencyMs int64      `json:"latency_ms,omitempty"`
	Message   string     `json:"message,omitempty"`
}
