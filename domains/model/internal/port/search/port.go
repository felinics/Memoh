package search

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrProviderTypeConflict = errors.New("search provider type already exists")
	ErrProviderNameTaken    = errors.New("search provider name already exists")
)

// ProviderRecord is the persistence-neutral search provider representation.
type ProviderRecord struct {
	ID        string
	Name      string
	Provider  string
	Config    json.RawMessage
	Enable    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ProviderWrite is the persistence write input for search providers.
type ProviderWrite struct {
	ID       string
	Name     string
	Provider string
	Config   json.RawMessage
	Enable   bool
}

// Store is the narrow persistence port consumed by the search service.
type Store interface {
	CreateProvider(context.Context, ProviderWrite) (ProviderRecord, error)
	FindProvider(context.Context, string) (ProviderRecord, error)
	ListProviders(context.Context, string) ([]ProviderRecord, error)
	UpdateProvider(context.Context, ProviderWrite) (ProviderRecord, error)
	DeleteProvider(context.Context, string) error
}
