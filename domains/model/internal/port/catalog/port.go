package catalog

import (
	"context"
	"encoding/json"
	"errors"

	modeldomain "github.com/memohai/memoh/domains/model"
)

var (
	ErrModelNotFound        = errors.New("model not found")
	ErrModelIDAlreadyExists = errors.New("model_id already exists")
	ErrModelIDAmbiguous     = errors.New("model_id is ambiguous across providers")
)

// Record is the persistence-neutral representation of a model row.
type Record struct {
	ID         string
	ModelID    string
	Name       string
	ProviderID string
	Type       modeldomain.ModelType
	Enable     bool
	Config     json.RawMessage
}

type CreateInput struct {
	ModelID    string
	Name       string
	ProviderID string
	Type       modeldomain.ModelType
	Enable     bool
	Config     json.RawMessage
}

type UpdateInput struct {
	ID         string
	ModelID    string
	Name       string
	ProviderID string
	Type       modeldomain.ModelType
	Enable     bool
	Config     json.RawMessage
}

type VariantRecord struct {
	ID        string
	ModelID   string
	VariantID string
	Weight    int32
	Metadata  json.RawMessage
}

type CreateVariantInput struct {
	ModelID   string
	VariantID string
	Weight    int32
	Metadata  json.RawMessage
}

// CatalogModelInput describes a registry/catalog model upsert.
type CatalogModelInput struct {
	ModelID    string
	Name       string
	ProviderID string
	Type       modeldomain.ModelType
	Config     json.RawMessage
}

// Store is the narrow persistence port consumed by the catalog service.
type Store interface {
	Create(context.Context, CreateInput) (Record, error)
	GetByID(context.Context, string) (Record, error)
	GetByModelID(context.Context, string) (Record, error)
	GetByProviderAndModelID(context.Context, string, string) (Record, error)
	List(context.Context) ([]Record, error)
	ListByType(context.Context, modeldomain.ModelType) ([]Record, error)
	ListByProviderID(context.Context, string) ([]Record, error)
	ListByProviderIDAndType(context.Context, string, modeldomain.ModelType) ([]Record, error)
	ListByProviderClientType(context.Context, modeldomain.ClientType) ([]Record, error)
	ListEnabled(context.Context) ([]Record, error)
	ListEnabledByType(context.Context, modeldomain.ModelType) ([]Record, error)
	ListEnabledByProviderClientType(context.Context, modeldomain.ClientType) ([]Record, error)
	Update(context.Context, UpdateInput) (Record, error)
	Delete(context.Context, string) error
	Count(context.Context) (int64, error)
	CountByType(context.Context, modeldomain.ModelType) (int64, error)
}
