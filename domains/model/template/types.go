package template

import (
	"errors"
	"time"

	templateport "github.com/memohai/memoh/domains/model/internal/port/template"
)

var (
	ErrTemplateNotFound     = templateport.ErrTemplateNotFound
	ErrTransactionsRequired = templateport.ErrTransactionsRequired
	ErrDomainInvalid        = errors.New("provider template domain invalid")
	ErrDomainMismatch       = errors.New("provider template domain mismatch")
)

type Domain string

const (
	DomainLLM           Domain = "llm"
	DomainSpeech        Domain = "speech"
	DomainTranscription Domain = "transcription"
	DomainVideo         Domain = "video"
)

func IsValidDomain(domain Domain) bool {
	switch domain {
	case DomainLLM, DomainSpeech, DomainTranscription, DomainVideo:
		return true
	default:
		return false
	}
}

// Definition is the canonical provider-template value produced from YAML and
// synchronized into the template catalog.
type Definition struct {
	Key           string
	Domain        Domain
	Name          string
	Description   string
	Icon          string
	Driver        string
	ConfigSchema  map[string]any
	DefaultConfig map[string]any
	Metadata      map[string]any
	Source        string
	SortOrder     int
	Models        []ModelDefinition
}

type ModelDefinition struct {
	ModelID   string
	Name      string
	Type      string
	Config    map[string]any
	Metadata  map[string]any
	SortOrder int
}

type ModelResponse struct {
	ID        string         `json:"id"`
	ModelID   string         `json:"model_id"`
	Name      string         `json:"name"`
	Type      string         `json:"type"`
	Config    map[string]any `json:"config,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	SortOrder int            `json:"sort_order"`
}

type GetResponse struct {
	ID            string          `json:"id"`
	Key           string          `json:"key"`
	Domain        string          `json:"domain"`
	Name          string          `json:"name"`
	Description   string          `json:"description,omitempty"`
	Icon          string          `json:"icon,omitempty"`
	Driver        string          `json:"driver"`
	ConfigSchema  map[string]any  `json:"config_schema,omitempty"`
	DefaultConfig map[string]any  `json:"default_config,omitempty"`
	Metadata      map[string]any  `json:"metadata,omitempty"`
	Source        string          `json:"source,omitempty"`
	SortOrder     int             `json:"sort_order"`
	Configured    bool            `json:"configured"`
	Models        []ModelResponse `json:"models,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type CreateInstanceRequest struct {
	TemplateID string         `json:"template_id" validate:"required"`
	Name       string         `json:"name,omitempty"`
	Config     map[string]any `json:"config,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

// CatalogTemplate is the persistence-neutral catalog row exposed to consumers.
type CatalogTemplate struct {
	ID            string
	Key           string
	Domain        string
	Name          string
	Description   string
	Icon          string
	Driver        string
	ConfigSchema  []byte
	DefaultConfig []byte
	Metadata      []byte
	Source        string
	SortOrder     int
	Configured    bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type CatalogModel struct {
	ID        string
	ModelID   string
	Name      string
	Type      string
	Config    []byte
	Metadata  []byte
	SortOrder int
}

func publicCatalogTemplate(row templateport.CatalogTemplate) CatalogTemplate {
	return CatalogTemplate{
		ID:            row.ID,
		Key:           row.Key,
		Domain:        row.Domain,
		Name:          row.Name,
		Description:   row.Description,
		Icon:          row.Icon,
		Driver:        row.Driver,
		ConfigSchema:  append([]byte(nil), row.ConfigSchema...),
		DefaultConfig: append([]byte(nil), row.DefaultConfig...),
		Metadata:      append([]byte(nil), row.Metadata...),
		Source:        row.Source,
		SortOrder:     row.SortOrder,
		Configured:    row.Configured,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}
}

func publicCatalogModel(row templateport.CatalogModel) CatalogModel {
	return CatalogModel{
		ID:        row.ID,
		ModelID:   row.ModelID,
		Name:      row.Name,
		Type:      row.Type,
		Config:    append([]byte(nil), row.Config...),
		Metadata:  append([]byte(nil), row.Metadata...),
		SortOrder: row.SortOrder,
	}
}
