package video

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	modeldomain "github.com/memohai/memoh/domains/model"
)

var (
	ErrProviderNotFound = errors.New("video provider not found")
	ErrModelNotFound    = errors.New("video model not found")
)

// ProviderRecord is the persistence-neutral provider state consumed by Video.
type ProviderRecord struct {
	ID         string
	Name       string
	ClientType modeldomain.ClientType
	Icon       string
	Enable     bool
	Config     json.RawMessage
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ModelRecord is the persistence-neutral video model state consumed by Video.
type ModelRecord struct {
	ID           string
	ModelID      string
	Name         string
	ProviderID   string
	Type         modeldomain.ModelType
	Enable       bool
	Config       json.RawMessage
	ProviderType modeldomain.ClientType
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// UpdateModelInput contains the complete model state preserved by a Video
// configuration update.
type UpdateModelInput struct {
	ID         string
	ModelID    string
	Name       string
	ProviderID string
	Type       modeldomain.ModelType
	Enable     bool
	Config     json.RawMessage
}

// CatalogModelInput describes a model synchronized from the Video registry.
type CatalogModelInput struct {
	ModelID    string
	Name       string
	ProviderID string
	Type       modeldomain.ModelType
	Config     json.RawMessage
}

// Store is the narrow persistence port consumed by the Video service.
type Store interface {
	ListProviders(context.Context) ([]ProviderRecord, error)
	GetProvider(context.Context, string) (ProviderRecord, error)
	ListModels(context.Context) ([]ModelRecord, error)
	ListModelsByProvider(context.Context, string) ([]ModelRecord, error)
	GetModel(context.Context, string) (ModelRecord, error)
	UpdateVideoModel(context.Context, UpdateModelInput) (ModelRecord, error)
}

// CatalogStore is the narrower persistence port used during registry sync.
type CatalogStore interface {
	GetProviderByClientType(context.Context, modeldomain.ClientType) (ProviderRecord, error)
	UpsertVideoCatalogModel(context.Context, CatalogModelInput) error
}
