package workspacedeps

import (
	"context"
	"errors"
	"time"
)

// Status is the lifecycle state of a dependency installation record. The
// database records intent, never fact: "installed"
// only means the last discovery confirmed the copy, and "missing" means the
// user wanted it but the workspace no longer has it.
type Status string

const (
	StatusInstalled  Status = "installed"
	StatusInstalling Status = "installing"
	StatusUpdating   Status = "updating"
	StatusRemoving   Status = "removing"
	StatusMissing    Status = "missing"
	StatusFailed     Status = "failed"
)

// InProgress reports whether the status is a transient operation state that
// a stale reaper may have to recover.
func (s Status) InProgress() bool {
	switch s {
	case StatusInstalling, StatusUpdating, StatusRemoving:
		return true
	}
	return false
}

// Installation source values record who currently provides the copy the
// record describes; discovery corrects them.
const (
	InstallationSourceImage   = "image"
	InstallationSourceManaged = "managed"
)

// Installation is one row of bot_dependency_installations.
type Installation struct {
	OperationID        string
	SourceURL          string
	RegistryID         string
	DefinitionRevision string
	ID                 string
	BotID              string
	WorkspaceTargetID  string
	DependencyID       string
	Source             string
	Status             Status
	InstalledVersion   string
	LatestVersion      string
	LastCheckedAt      *time.Time
	LastError          string
	ManifestDigest     string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// InstallationKey identifies a record; the triple is unique per team.
type InstallationKey struct {
	BotID             string
	WorkspaceTargetID string
	DependencyID      string
}

// UpsertInstallation creates or replaces the intent portion of a record.
type UpsertInstallation struct {
	SourceURL          string
	RegistryID         string
	DefinitionRevision string
	InstallationKey
	Source           string
	Status           Status
	InstalledVersion string
	ManifestDigest   string
}

// ObservedUpdate carries the fact-derived columns discovery and update
// checks may correct. A nil field leaves that column untouched.
type ObservedUpdate struct {
	SourceURL          *string
	RegistryID         *string
	DefinitionRevision *string
	Source             *string
	InstalledVersion   *string
	LatestVersion      *string
	LastCheckedAt      *time.Time
	LastError          *string
	ManifestDigest     *string
}

// ErrInstallationNotFound is returned by Store lookups for unknown keys.
// Conditional writes that miss a row or find a claimed operation return ErrBusy.
var ErrInstallationNotFound = errors.New("workspace dependency installation not found")

// Store persists installation records. Implementations are team scoped by
// row level security through the caller's context; a background worker
// must run inside a team-bound context the same way request handlers do.
type Store interface {
	// ClaimOperation admits a new operation only when no in-progress intent
	// exists. Conflicting admission returns ErrBusy without changing the row.
	ClaimOperation(ctx context.Context, in UpsertInstallation, operationID string) (Installation, error)
	// FinishOperation changes or deletes (terminal=nil) only the row still
	// owned by operationID. A superseded operation returns ErrBusy.
	FinishOperation(ctx context.Context, key InstallationKey, operationID string, terminal *Installation) (Installation, error)
	Get(ctx context.Context, key InstallationKey) (Installation, error)
	ListForTarget(ctx context.Context, botID, workspaceTargetID string) ([]Installation, error)
	ListForBot(ctx context.Context, botID string) ([]Installation, error)
	// ListByStatus returns every record in the given status across bots.
	ListByStatus(ctx context.Context, status Status) ([]Installation, error)
	// ListStaleOperations returns in-progress records not updated for at
	// least olderThan, so the reaper can mark them failed.
	ListStaleOperations(ctx context.Context, olderThan time.Duration) ([]Installation, error)
	Upsert(ctx context.Context, in UpsertInstallation) (Installation, error)
	SetStatus(ctx context.Context, key InstallationKey, status Status, lastError string) (Installation, error)
	UpdateObserved(ctx context.Context, key InstallationKey, upd ObservedUpdate) (Installation, error)
	Delete(ctx context.Context, key InstallationKey) error
}
