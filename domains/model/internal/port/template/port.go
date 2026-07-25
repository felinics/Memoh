package template

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrTemplateNotFound     = errors.New("provider template not found")
	ErrTransactionsRequired = errors.New("provider template persistence requires transactions")
)

// CatalogStore is the narrow read port consumed by template catalog use cases.
type CatalogStore interface {
	ListTemplates(context.Context, string) ([]CatalogTemplate, error)
	FindTemplate(context.Context, string) (CatalogTemplate, error)
	ListTemplateModels(context.Context, string) ([]CatalogModel, error)
}

type CatalogTemplate struct {
	ID            string
	Key           string
	Domain        string
	Name          string
	Description   string
	Icon          string
	Driver        string
	ConfigSchema  json.RawMessage
	DefaultConfig json.RawMessage
	Metadata      json.RawMessage
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
	Config    json.RawMessage
	Metadata  json.RawMessage
	SortOrder int
}

// SyncStore owns the atomic boundary for one template catalog sync.
// Implementations must invoke the callback with a transaction-scoped store.
type SyncStore interface {
	RunSyncTransaction(context.Context, func(Transaction) error) error
}

// Transaction is the narrow persistence port used while synchronizing the
// checked-in provider-template catalog.
type Transaction interface {
	AcquireSyncLock(context.Context) error
	ListTemplates(context.Context) ([]TemplateRecord, error)
	UpsertTemplate(context.Context, UpsertTemplateCommand) (TemplateRecord, error)
	DeactivateTemplate(context.Context, string) error
	ListModels(context.Context, string) ([]ModelRecord, error)
	UpsertModel(context.Context, UpsertModelCommand) error
	DeactivateModel(context.Context, string) error
}

type TemplateRecord struct {
	ID          string
	Domain      string
	Key         string
	ContentHash string
	Active      bool
}

type ModelRecord struct {
	ID      string
	ModelID string
	Type    string
}

type UpsertTemplateCommand struct {
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
	ContentHash   string
	SortOrder     int
}

type UpsertModelCommand struct {
	TemplateID string
	ModelID    string
	Name       string
	Type       string
	Config     []byte
	Metadata   []byte
	SortOrder  int
}

// ProviderCatalog is the Provider-owned data required by YAML provider sync.
type ProviderCatalog interface {
	ListProviders(context.Context) ([]ProviderRecord, error)
	UpsertProvider(context.Context, ProviderSeed) (ProviderRecord, error)
	UpdateProvider(context.Context, ProviderUpdate) (ProviderRecord, error)
}

// ModelCatalog is the Model-owned data required by YAML provider sync.
type ModelCatalog interface {
	ListModelIDs(context.Context, string) ([]string, error)
	UpsertModel(context.Context, ModelSeed) error
}

type ProviderRecord struct {
	ID         string
	Name       string
	ClientType string
	Icon       string
	Enable     bool
	Config     json.RawMessage
	Metadata   json.RawMessage
}

type ProviderSeed struct {
	Name       string
	ClientType string
	Icon       string
	Config     json.RawMessage
}

type ProviderUpdate struct {
	ID         string
	Name       string
	ClientType string
	Icon       string
	Enable     bool
	Config     json.RawMessage
	Metadata   json.RawMessage
}

type ModelSeed struct {
	ProviderID string
	ModelID    string
	Name       string
	Type       string
	Config     json.RawMessage
}
