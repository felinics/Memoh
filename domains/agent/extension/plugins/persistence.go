package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var ErrNotFound = errors.New("plugin installation not found")

// InstallationRecord is the persistence-neutral plugin installation state
// consumed by Service.
type InstallationRecord struct {
	ID          string
	BotID       string
	PluginID    string
	PluginName  string
	Version     string
	Status      string
	Enabled     bool
	Config      json.RawMessage
	Metadata    json.RawMessage
	Manifest    json.RawMessage
	InstalledAt time.Time
	UpdatedAt   time.Time
}

type CreateInstallationInput struct {
	BotID      string
	PluginID   string
	PluginName string
	Version    string
	Status     string
	Enabled    bool
	Config     json.RawMessage
	Metadata   json.RawMessage
	Manifest   json.RawMessage
}

type InstallationStatusUpdate struct {
	BotID          string
	InstallationID string
	Status         string
	Enabled        bool
}

// ResourceRecord is the durable resource link associated with a plugin
// installation.
type ResourceRecord struct {
	ID             string
	InstallationID string
	Type           string
	Key            string
	ResourceID     string
	Status         string
	Metadata       json.RawMessage
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type ResourceUpsert struct {
	InstallationID string
	Type           string
	Key            string
	ResourceID     string
	Status         string
	Metadata       json.RawMessage
}

// Store is the narrow persistence port consumed by the plugin lifecycle
// service. Generated PostgreSQL query types stay behind its adapter.
type Store interface {
	ListInstallations(context.Context, string) ([]InstallationRecord, error)
	CreateInstallation(context.Context, CreateInstallationInput) (InstallationRecord, error)
	FindInstallation(context.Context, string, string) (InstallationRecord, error)
	UpdateInstallationStatus(context.Context, InstallationStatusUpdate) (InstallationRecord, error)
	DeleteInstallation(context.Context, string, string) error
	ListResources(context.Context, string) ([]ResourceRecord, error)
	UpsertResource(context.Context, ResourceUpsert) error
	DeleteResources(context.Context, string) error
}
