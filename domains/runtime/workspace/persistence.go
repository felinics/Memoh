package workspace

import (
	"context"
	"time"

	"github.com/memohai/memoh/domains/api/setting"
	"github.com/memohai/memoh/domains/runtime/container"
)

// WorkspacePreferences is Runtime's typed projection of API-owned bot
// metadata. Runtime never reads or rewrites the complete bot profile.
type WorkspacePreferences struct {
	Image                  string
	GPU                    WorkspaceGPUConfig
	HasGPU                 bool
	SkillDiscoveryRoots    []string
	HasSkillDiscoveryRoots bool
}

// WorkspacePreferenceReader and WorkspacePreferenceWriter are the narrow
// ports consumed by Runtime. The API owner is responsible for preserving all
// unrelated bot metadata while applying writes.
type WorkspacePreferenceReader interface {
	LookupWorkspacePreferences(context.Context, string) (WorkspacePreferences, bool, error)
}

type WorkspacePreferenceWriter interface {
	SetWorkspaceImagePreference(context.Context, string, string) error
	ClearWorkspaceImagePreference(context.Context, string) error
	SetWorkspaceGPUPreference(context.Context, string, WorkspaceGPUConfig) error
	ClearWorkspaceGPUPreference(context.Context, string) error
}

// BotExistenceReader is used only to validate a Runtime-owned container write.
// It does not expose the API-owned bot row.
type BotExistenceReader interface {
	RequireBot(context.Context, string) error
}

// BotOwnerReader exposes only the API-owned identity needed to validate
// remote runtime ownership.
type BotOwnerReader interface {
	BotOwnerUserID(context.Context, string) (string, error)
}

type BotProfileStore interface {
	WorkspacePreferenceReader
	WorkspacePreferenceWriter
	BotExistenceReader
}

type BotRuntimeSettings struct {
	ToolApprovalConfig setting.ToolApprovalConfig
	DisplayEnabled     bool
}

// BotRuntimeSettingsReader exposes Runtime's typed, read-only projection of
// API-owned bot setting.
type BotRuntimeSettingsReader interface {
	FindBotRuntimeSettings(context.Context, string) (BotRuntimeSettings, error)
}

type ContainerRecord struct {
	BotID            string
	ContainerID      string
	Image            string
	Status           string
	Namespace        string
	ContainerPath    string
	WorkspaceBackend string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type UpsertContainerCommand struct {
	BotID            string
	ContainerID      string
	Image            string
	Status           string
	Namespace        string
	ContainerPath    string
	WorkspaceBackend string
	AutoStart        bool
}

// ContainerStore owns Workspace container lifecycle persistence.
type ContainerStore interface {
	FindContainer(context.Context, string) (ContainerRecord, error)
	ListAutoStartContainers(context.Context) ([]ContainerRecord, error)
	UpsertContainer(context.Context, UpsertContainerCommand) error
	DeleteContainer(context.Context, string) error
	MarkContainerStarted(context.Context, string) error
	MarkContainerStopped(context.Context, string) error
	MarkContainerStatus(context.Context, string, string) error
}

// ResourceLimitStore owns desired workspace resource limits.
type ResourceLimitStore interface {
	FindResourceLimits(context.Context, string) (container.ResourceLimits, error)
	SaveResourceLimits(context.Context, string, container.ResourceLimits) error
}

type SnapshotRecord struct {
	RuntimeSnapshotName       string
	DisplayName               string
	ParentRuntimeSnapshotName string
	Snapshotter               string
	Source                    string
	CreatedAt                 time.Time
	Version                   *int
}

type VersionRecord struct {
	ID                  string
	Version             int
	RuntimeSnapshotName string
	DisplayName         string
	CreatedAt           time.Time
}

type RecordSnapshotVersionCommand struct {
	ContainerID               string
	RuntimeSnapshotName       string
	DisplayName               string
	ParentRuntimeSnapshotName string
	Snapshotter               string
	Source                    string
}

type RecordedSnapshotVersion struct {
	ID        string
	Version   int
	CreatedAt time.Time
}

// VersionStore owns Workspace snapshot/version metadata.
// RecordSnapshotVersion must always be atomic.
type VersionStore interface {
	ListSnapshots(context.Context, string) ([]SnapshotRecord, error)
	ListVersions(context.Context, string) ([]VersionRecord, error)
	FindVersionSnapshotName(context.Context, string, int) (string, error)
	RecordSnapshotVersion(context.Context, RecordSnapshotVersionCommand) (RecordedSnapshotVersion, error)
	InsertLifecycleEvent(context.Context, string, string, []byte) error
}

// Persistence groups independently replaceable Workspace consumer ports. A
// concrete adapter may implement every field, but callers are not required to
// provide a giant Store interface.
type Persistence struct {
	Profiles       BotProfileStore
	Settings       BotRuntimeSettingsReader
	Containers     ContainerStore
	ResourceLimits ResourceLimitStore
	Versions       VersionStore
}

// RemoteMountRecord is the durable remote-workspace binding consumed by the
// Workspace use-case. PostgreSQL and generated SQLC types stay in its adapter.
type RemoteMountRecord struct {
	ID             string
	BotID          string
	RuntimeID      string
	IsPrimary      bool
	ToolApproval   []byte
	RuntimeName    string
	RuntimeUserID  string
	BotOwnerUserID string
	RuntimeRevoked bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// RemoteMountStore is the consumer-owned persistence port for the remote
// workspace mount aggregate.
type RemoteMountStore interface {
	CreateOrUpdateMount(context.Context, string, string, string) (RemoteMountRecord, error)
	ListMounts(context.Context, string) ([]RemoteMountRecord, error)
	GetMount(context.Context, string, string) (RemoteMountRecord, error)
	GetPrimaryMount(context.Context, string) (RemoteMountRecord, error)
	SetPrimary(context.Context, string, string) error
	UpdateToolApproval(context.Context, string, string, []byte) error
	DeleteMount(context.Context, string, string) error
	PrimaryMountTransactionRunner
}

// PrimaryMountTransaction performs the two-write remote-primary replacement
// inside one owner transaction.
type PrimaryMountTransaction interface {
	SetPrimary(context.Context, string, string) error
}

type PrimaryMountTransactionRunner interface {
	RunPrimaryMountTransaction(context.Context, func(PrimaryMountTransaction) error) error
}
