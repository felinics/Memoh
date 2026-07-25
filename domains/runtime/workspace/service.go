package workspace

import (
	"context"
	"io"
	"net"
	"time"

	"github.com/memohai/memoh/domains/agent/extension/hooks"
	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
	ctr "github.com/memohai/memoh/domains/runtime/container"
)

// Service is the stable workspace manager surface consumed by HTTP handlers,
// Agent tools, bots lifecycle, and composition roots.
type Service interface {
	bridge.Provider

	SetHookService(h *hooks.Service)
	SetSetupDiagnostics(diagnostics WorkspaceSetupDiagnostics)

	BotDisplayEnabled(ctx context.Context, botID string) bool
	DisplaySocketPath(botID string) string
	DisplayDialContext(ctx context.Context, botID, network, address string) (net.Conn, error)

	NativeMCPClient(ctx context.Context, botID string) (*bridge.Client, error)
	ResolveWorkspaceTarget(ctx context.Context, botID, targetID string) (ResolvedWorkspaceTarget, error)
	ListWorkspaceTargets(ctx context.Context, botID string) ([]WorkspaceTarget, error)
	WorkspaceInfo(ctx context.Context, botID string) (bridge.WorkspaceInfo, error)

	Init(ctx context.Context) error
	EnsureBot(ctx context.Context, botID, imageOverride string) error
	EnsureRunning(ctx context.Context, botID string) error
	EnsureNativeRunning(ctx context.Context, botID string) error
	WaitForWorkspaceReady(ctx context.Context, botID string) error
	InitializeNativeWorkspace(ctx context.Context, botID string) error

	ContainerID(ctx context.Context, botID string) (string, error)
	GetContainerInfo(ctx context.Context, botID string) (*ContainerStatus, error)
	GetContainerMetrics(ctx context.Context, botID string) (*ContainerMetricsResult, error)
	RecordContainerRunning(ctx context.Context, botID, containerID, image string)

	ResolveWorkspaceImage(ctx context.Context, botID string) (string, error)
	ResolveWorkspaceGPU(ctx context.Context, botID string) (WorkspaceGPUConfig, error)
	ResolveWorkspaceSkillDiscoveryRoots(ctx context.Context, botID string) ([]string, error)
	RememberWorkspaceImage(ctx context.Context, botID, image string) error
	RememberWorkspaceGPU(ctx context.Context, botID string, gpu WorkspaceGPUConfig) error
	ClearWorkspaceImagePreference(ctx context.Context, botID string) error
	ClearWorkspaceGPUPreference(ctx context.Context, botID string) error
	PrepareImageForCreate(ctx context.Context, image string, opts *ctr.PullImageOptions) (ImagePrepareResult, error)

	Start(ctx context.Context, botID string) error
	StartWithImage(ctx context.Context, botID, imageOverride string) error
	StartWithResolvedImage(ctx context.Context, botID, image string) error
	StartWithResolvedConfig(ctx context.Context, botID, image string, gpu WorkspaceGPUConfig) error
	Stop(ctx context.Context, botID string, timeout time.Duration) error
	StopBot(ctx context.Context, botID string) error
	Delete(ctx context.Context, botID string, preserveData bool) error
	CleanupBotContainer(ctx context.Context, botID string, preserveData bool) error
	SetupBotContainer(ctx context.Context, botID string) error
	SetupBotContainerWithProgress(ctx context.Context, botID string, progress ContainerSetupProgress) error
	ReconcileContainers(ctx context.Context)

	HasPreservedData(botID string) bool
	PreserveData(ctx context.Context, botID string) error
	RestorePreservedData(ctx context.Context, botID string) error
	ExportData(ctx context.Context, botID string) (io.ReadCloser, error)
	ImportData(ctx context.Context, botID string, r io.Reader) error
	CountData(ctx context.Context, botID string) (int, error)

	CreateSnapshot(ctx context.Context, botID, snapshotName, source string) (*SnapshotCreateInfo, error)
	CreateVersion(ctx context.Context, botID string) (*VersionInfo, error)
	ListBotSnapshotData(ctx context.Context, botID string) (*BotSnapshotData, error)
	ListVersions(ctx context.Context, botID string) ([]VersionInfo, error)
	RollbackVersion(ctx context.Context, botID string, version int) error
	VersionSnapshotName(ctx context.Context, botID string, version int) (string, error)

	GetResourceLimits(ctx context.Context, botID string) (*ResourceLimitsResult, error)
	SetResourceLimits(ctx context.Context, botID string, limits ctr.ResourceLimits) (*ResourceLimitsResult, error)

	ListBots(ctx context.Context) ([]string, error)
}
