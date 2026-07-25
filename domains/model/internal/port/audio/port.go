package audio

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	modeldomain "github.com/memohai/memoh/domains/model"
)

var (
	ErrProviderNotFound = errors.New("audio provider not found")
	ErrModelNotFound    = errors.New("audio model not found")
)

// ProviderRecord is the persistence-neutral provider state consumed by Audio.
type ProviderRecord struct {
	ID         string
	Name       string
	ClientType string
	Icon       string
	Enable     bool
	Config     json.RawMessage
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ModelRecord is the persistence-neutral speech or transcription model state.
type ModelRecord struct {
	ID           string
	ModelID      string
	Name         string
	ProviderID   string
	ProviderType string
	Type         modeldomain.ModelType
	Enable       bool
	Config       json.RawMessage
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// UpdateModelInput contains the complete model state preserved by an Audio
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

// CatalogModelInput describes a model synchronized from the Audio registry.
type CatalogModelInput struct {
	ModelID    string
	Name       string
	ProviderID string
	Type       modeldomain.ModelType
	Config     json.RawMessage
}

// Store is the narrow persistence port consumed by the Audio service.
type Store interface {
	ListSpeechProviders(context.Context) ([]ProviderRecord, error)
	ListTranscriptionProviders(context.Context) ([]ProviderRecord, error)
	GetProvider(context.Context, string) (ProviderRecord, error)
	ListSpeechModels(context.Context) ([]ModelRecord, error)
	ListTranscriptionModels(context.Context) ([]ModelRecord, error)
	ListSpeechModelsByProvider(context.Context, string) ([]ModelRecord, error)
	ListTranscriptionModelsByProvider(context.Context, string) ([]ModelRecord, error)
	GetSpeechModel(context.Context, string) (ModelRecord, error)
	GetTranscriptionModel(context.Context, string) (ModelRecord, error)
	UpdateAudioModel(context.Context, UpdateModelInput) (ModelRecord, error)
}

// CatalogStore is the narrower persistence port used during registry sync.
type CatalogStore interface {
	GetProviderByClientType(context.Context, modeldomain.ClientType) (ProviderRecord, error)
	UpsertAudioCatalogModel(context.Context, CatalogModelInput) (ModelRecord, error)
	DeleteAudioCatalogModel(context.Context, string, string, modeldomain.ModelType) error
}
