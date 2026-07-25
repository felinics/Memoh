package port

import (
	"context"
	"encoding/json"
	"time"
)

type ProviderRecord struct {
	ID        string
	Name      string
	Provider  string
	Config    json.RawMessage
	IsDefault bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

type ProviderCreate struct {
	Name      string
	Provider  string
	Config    json.RawMessage
	IsDefault bool
}

type ProviderUpdate struct {
	ID     string
	Name   string
	Config json.RawMessage
}

type ProviderStore interface {
	CreateProvider(context.Context, ProviderCreate) (ProviderRecord, error)
	FindProvider(context.Context, string) (ProviderRecord, error)
	ListProviders(context.Context) ([]ProviderRecord, error)
	UpdateProvider(context.Context, ProviderUpdate) (ProviderRecord, error)
	DeleteProvider(context.Context, string) error
	FindDefaultProvider(context.Context) (ProviderRecord, error)
}
