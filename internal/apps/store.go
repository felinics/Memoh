// App apps installs Supermarket Apps into bot workspaces. A
// Package bundles Skills, workspace dependency references, and Connect-It
// connector references; the service materializes the Skills itself and
// delegates dependencies and connectors to their own services, keeping one
// shared copy of each per bot.
package apps

import (
	"context"
	"errors"
	"time"
)

// Status is the lifecycle state of an App installation record.
type Status string

// Installation statuses.
const (
	// StatusInstalled means every component the App needs is in place.
	StatusInstalled Status = "installed"
	// StatusPartial means the Skills are in place but a dependency failed to
	// install or a required connector is not authorized yet.
	StatusPartial Status = "partial"
	// StatusInstalling, StatusUpdating and StatusRemoving are transient
	// operation states.
	StatusInstalling Status = "installing"
	StatusUpdating   Status = "updating"
	StatusRemoving   Status = "removing"
	// StatusFailed means the Skills could not be materialized.
	StatusFailed Status = "failed"
)

// Reason records why an App was installed.
type Reason string

// Installation reasons.
const (
	// ReasonUser means the user installed the App explicitly.
	ReasonUser Reason = "user"
	// ReasonRequired means another App pulled it in as a dependency
	// carrier and nobody asked for it directly.
	ReasonRequired Reason = "required"
)

// Installation is one row of bot_app_installations.
type Installation struct {
	ID                string
	BotID             string
	WorkspaceTargetID string
	RegistryID        string
	AppID             string
	Revision          string
	Version           string
	Status            Status
	Reason            Reason
	AvailableRevision string
	AvailableVersion  string
	LastCheckedAt     *time.Time
	LastError         string
	// Release is the immutable release document the installation
	// materialized; it lets the App view work without the Supermarket.
	Release     []byte
	InstalledAt time.Time
	UpdatedAt   time.Time
}

// UpsertInstallation creates or replaces the identity portion of a record.
type UpsertInstallation struct {
	BotID             string
	WorkspaceTargetID string
	RegistryID        string
	AppID             string
	Revision          string
	Version           string
	Status            Status
	Reason            Reason
	Release           []byte
}

// DependencyRef links an installation to a workspace dependency ID.
type DependencyRef struct {
	InstallationID string
	DependencyID   string
}

// TargetDependencyRef is a DependencyRef joined with its App identity.
type TargetDependencyRef struct {
	DependencyRef
	RegistryID string
	AppID      string
}

// ConnectorRef links an installation to a Connect-It connector type and, once
// authorized, to the bot-level connection.
type ConnectorRef struct {
	InstallationID string
	ConnectorType  string
	ConnectionID   string
	Required       bool
}

// BotConnectorRef is a ConnectorRef joined with its App identity.
type BotConnectorRef struct {
	ConnectorRef
	RegistryID        string
	AppID             string
	WorkspaceTargetID string
}

// ErrNotInstalled is returned by lookups for unknown installations.
var ErrNotInstalled = errors.New("app is not installed")

// Store persists App installations and their references. Rows are team
// scoped by row level security on the connection.
type Store interface {
	Get(ctx context.Context, botID, workspaceTargetID, registryID, appID string) (Installation, error)
	GetByID(ctx context.Context, botID, installationID string) (Installation, error)
	ListForBot(ctx context.Context, botID string) ([]Installation, error)
	ListForTarget(ctx context.Context, botID, workspaceTargetID string) ([]Installation, error)
	Upsert(ctx context.Context, in UpsertInstallation) (Installation, error)
	SetStatus(ctx context.Context, botID, installationID string, status Status, lastError string) (Installation, error)
	SetRelease(ctx context.Context, botID, installationID, revision, version string, release []byte) (Installation, error)
	SetCheck(ctx context.Context, botID, installationID, availableRevision, availableVersion string, checkedAt time.Time) (Installation, error)
	Delete(ctx context.Context, botID, installationID string) (Installation, error)

	ListDependencyRefs(ctx context.Context, installationID string) ([]DependencyRef, error)
	ListTargetDependencyRefs(ctx context.Context, botID, workspaceTargetID string) ([]TargetDependencyRef, error)
	AddDependencyRef(ctx context.Context, installationID, dependencyID string) error
	RemoveDependencyRef(ctx context.Context, installationID, dependencyID string) error

	ListConnectorRefs(ctx context.Context, installationID string) ([]ConnectorRef, error)
	ListBotConnectorRefs(ctx context.Context, botID string) ([]BotConnectorRef, error)
	UpsertConnectorRef(ctx context.Context, ref ConnectorRef) error
	SetConnectorRefConnection(ctx context.Context, installationID, connectorType, connectionID string) error
	ClearConnectorRefConnection(ctx context.Context, connectionID string) error
	RemoveConnectorRef(ctx context.Context, installationID, connectorType string) error
}
