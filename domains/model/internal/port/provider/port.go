package provider

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrProviderNotFound   = errors.New("provider not found")
	ErrProviderNameTaken  = errors.New("provider name already exists")
	ErrOAuthTokenNotFound = errors.New("provider oauth token not found")
)

// ProviderRecord is the persistence-neutral provider state consumed by Service.
type ProviderRecord struct {
	ID                 string
	ProviderTemplateID string
	Name               string
	ClientType         string
	Icon               string
	Enable             bool
	Config             json.RawMessage
	Metadata           json.RawMessage
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type CreateProviderCommand struct {
	Name       string
	ClientType string
	Icon       string
	Enable     bool
	Config     json.RawMessage
	Metadata   json.RawMessage
}

type CreateProviderFromTemplateCommand struct {
	ProviderTemplateID string
	Name               string
	ClientType         string
	Icon               string
	Enable             bool
	Config             json.RawMessage
	Metadata           json.RawMessage
}

type UpdateProviderCommand struct {
	ID         string
	Name       string
	ClientType string
	Icon       string
	Enable     bool
	Config     json.RawMessage
	Metadata   json.RawMessage
}

// ProviderStore owns Provider CRUD. Generated PostgreSQL types stay behind its
// owner adapter.
type ProviderStore interface {
	CreateProvider(context.Context, CreateProviderCommand) (ProviderRecord, error)
	CreateProviderFromTemplate(context.Context, CreateProviderFromTemplateCommand) (ProviderRecord, error)
	GetProvider(context.Context, string) (ProviderRecord, error)
	GetProviderByName(context.Context, string) (ProviderRecord, error)
	ListProviders(context.Context) ([]ProviderRecord, error)
	UpdateProvider(context.Context, UpdateProviderCommand) (ProviderRecord, error)
	DeleteProvider(context.Context, string) error
}

// OAuthTokenRecord is the shared credential state owned by a Provider.
type OAuthTokenRecord struct {
	ProviderID       string
	AccessToken      string //nolint:gosec // Runtime credential material persisted encrypted at rest.
	RefreshToken     string //nolint:gosec // Runtime credential material persisted encrypted at rest.
	ExpiresAt        time.Time
	Scope            string
	TokenType        string
	State            string
	PKCECodeVerifier string
	Metadata         map[string]any
}

type OAuthStateUpdate struct {
	ProviderID       string
	State            string
	PKCECodeVerifier string
	Metadata         map[string]any
}

// OAuthStore owns Provider-scoped OAuth state and tokens independently from
// Provider CRUD.
type OAuthStore interface {
	GetOAuthToken(context.Context, string) (OAuthTokenRecord, error)
	GetOAuthTokenByState(context.Context, string) (OAuthTokenRecord, error)
	UpdateOAuthState(context.Context, OAuthStateUpdate) error
	SaveOAuthToken(context.Context, OAuthTokenRecord) error
	DeleteOAuthToken(context.Context, string) error
}
