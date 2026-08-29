package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/config"
	ctr "github.com/felinics/memoh/internal/container"
	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/hooks"
	"github.com/felinics/memoh/internal/identity"
	netctl "github.com/felinics/memoh/internal/network"
	"github.com/felinics/memoh/internal/settings"
	skillset "github.com/felinics/memoh/internal/skills"
	"github.com/felinics/memoh/internal/workspace/bridge"
	workspacetemplates "github.com/felinics/memoh/templates"
)

const (
	BotLabelKey                 = "memoh.bot_id"
	WorkspaceLabelKey           = "memoh.workspace"
	WorkspaceLabelValue         = "v3"
	WorkspaceCDIDevicesLabelKey = "memoh.workspace.cdi_devices"
	ContainerPrefix             = "workspace-"
	LegacyContainerPrefix       = "mcp-"
	DisplayRFBSocketName        = "display.rfb.sock"
	ACPToolsProxyHTTPURL        = bridge.ACPToolsProxyHTTPURL

	legacyGRPCPort           = 9090
	bridgeReadyTimeout       = 45 * time.Second
	bridgeReadyRPCTimeout    = 3 * time.Second
	bridgeReadyRetryInterval = 500 * time.Millisecond
)

// ErrContainerNotFound is returned when no container exists for a bot.
var ErrContainerNotFound = errors.New("workspace not found for bot")

// ContainerStatus combines DB records with live containerd state.
type ContainerStatus struct {
	ContainerID      string    `json:"container_id"`
	WorkspaceBackend string    `json:"workspace_backend"`
	RuntimeBackend   string    `json:"runtime_backend,omitempty"`
	Image            string    `json:"image"`
	Status           string    `json:"status"`
	Namespace        string    `json:"namespace"`
	ContainerPath    string    `json:"container_path"`
	CDIDevices       []string  `json:"cdi_devices,omitempty"`
	TaskRunning      bool      `json:"task_running"`
	HasPreservedData bool      `json:"has_preserved_data"`
	Legacy           bool      `json:"legacy"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type ContainerMetricsStatus struct {
	Exists      bool `json:"exists"`
	TaskRunning bool `json:"task_running"`
}

type ContainerStorageMetrics struct {
	Path      string `json:"path"`
	UsedBytes uint64 `json:"used_bytes"`
}

type ContainerMetricsResult struct {
	Supported         bool
	UnsupportedReason string
	Status            ContainerMetricsStatus
	SampledAt         time.Time
	CPU               *ctr.CPUMetrics
	Memory            *ctr.MemoryMetrics
	Storage           *ContainerStorageMetrics
}

type Manager struct {
	service           runtimeService
	networkController netctl.Controller
	cfg               config.WorkspaceConfig
	namespace         string
	db                *pgxpool.Pool
	queries           dbstore.Queries
	hookService       *hooks.Service
	logger            *slog.Logger
	containerLockMu   sync.Mutex
	containerLocks    map[string]*sync.Mutex
	grpcPool          *bridge.Pool
	bridgeTLS         *BridgeTLSRuntimeOptions
	remote            *RemoteWorkspaceService
	templateBootstrap *TemplateBootstrapper
	setupDiagnostics  WorkspaceSetupDiagnostics
	legacyMu          sync.RWMutex
	legacyIPs         map[string]string // botID → IP for pre-bridge containers
}

func NewManager(log *slog.Logger, service runtimeService, networkController netctl.Controller, cfg config.WorkspaceConfig, namespace string, conn *pgxpool.Pool, queryOverride ...dbstore.Queries) *Manager {
	if namespace == "" {
		namespace = config.DefaultNamespace
	}
	var queries dbstore.Queries
	if len(queryOverride) > 0 {
		queries = queryOverride[0]
	} else if conn != nil {
		queries = postgresstore.NewQueries(dbsqlc.New(conn))
	}
	m := &Manager{
		service:           service,
		networkController: networkController,
		cfg:               cfg,
		namespace:         namespace,
		db:                conn,
		queries:           queries,
		logger:            log.With(slog.String("component", "workspace")),
		containerLocks:    make(map[string]*sync.Mutex),
		legacyIPs:         make(map[string]string),
		templateBootstrap: NewTemplateBootstrapper(workspacetemplates.WorkspaceFS()),
	}
	m.grpcPool = bridge.NewPool(m.dialTarget)
	return m
}

func (m *Manager) SetHookService(h *hooks.Service) {
	m.hookService = h
}

func (m *Manager) SetRemoteWorkspaceService(service *RemoteWorkspaceService) {
	m.remote = service
}

// WorkspaceSetupDiagnostics records sanitized setup failures for Bot health
// checks without coupling the workspace package to the bots package.
type WorkspaceSetupDiagnostics interface {
	RecordContainerSetupFailure(ctx context.Context, botID, phase string, setupErr error) error
	ClearContainerSetupFailure(ctx context.Context, botID string) error
}

func (m *Manager) SetSetupDiagnostics(diagnostics WorkspaceSetupDiagnostics) {
	m.setupDiagnostics = diagnostics
}

// SetBridgeTLS enables strict mTLS on TCP bridge dials and injects bridge-side
// TLS material into new workspace containers. UDS bridge targets keep using the
// local filesystem trust model.
func (m *Manager) SetBridgeTLS(opts *BridgeTLSRuntimeOptions) {
	if opts == nil {
		m.bridgeTLS = nil
		m.grpcPool.SetTLSOptions(nil)
		return
	}
	m.bridgeTLS = opts
	m.grpcPool.SetTLSOptions(opts.Client)
}

// resolveContainerID resolves the actual workspace container ID for a bot.
// This is the SINGLE point of container ID resolution for all lookup operations.
// It delegates to ContainerID (DB → label → scan) and falls back to the
// new-style prefix if no container exists yet.
func (m *Manager) resolveContainerID(ctx context.Context, botID string) string {
	id, err := m.ContainerID(ctx, botID)
	if err != nil {
		return ContainerPrefix + botID
	}
	return id
}

func (m *Manager) lockContainer(containerID string) func() {
	m.containerLockMu.Lock()
	lock, ok := m.containerLocks[containerID]
	if !ok {
		lock = &sync.Mutex{}
		m.containerLocks[containerID] = lock
	}
	m.containerLockMu.Unlock()

	lock.Lock()
	return lock.Unlock
}

// socketDir returns the host-side directory that is bind-mounted into the
// container at /run/memoh, holding the UDS socket file.
func (m *Manager) socketDir(botID string) string {
	return filepath.Join(m.dataRoot(), "run", botID)
}

// socketPath returns the path to the UDS socket file for a bot's container.
func (m *Manager) socketPath(botID string) string {
	return filepath.Join(m.socketDir(botID), "bridge.sock")
}

// DisplaySocketPath returns the host-side path to the workspace display RFB
// Unix socket. The directory is mounted into the container at /run/memoh.
func (m *Manager) DisplaySocketPath(botID string) string {
	return filepath.Join(m.socketDir(botID), DisplayRFBSocketName)
}

// dialTarget returns the gRPC dial target for a bot. Legacy containers
// (pre-bridge) are reached via TCP; bridge containers use UDS.
func (m *Manager) dialTarget(botID string) string {
	if targeter, ok := m.service.(interface{ BridgeTarget(string) string }); ok {
		if target := strings.TrimSpace(targeter.BridgeTarget(botID)); target != "" {
			return target
		}
	}
	m.legacyMu.RLock()
	ip, legacy := m.legacyIPs[botID]
	m.legacyMu.RUnlock()
	if legacy {
		return fmt.Sprintf("passthrough:///%s:%d", ip, legacyGRPCPort)
	}
	return "unix://" + m.socketPath(botID)
}

// SetLegacyIP records the IP address of a legacy (pre-bridge) container
// so the gRPC pool can reach it via TCP.
func (m *Manager) SetLegacyIP(botID, ip string) {
	m.legacyMu.Lock()
	m.legacyIPs[botID] = ip
	m.legacyMu.Unlock()
}

// ClearLegacyIP removes a cached legacy IP (e.g. when the container is deleted).
func (m *Manager) ClearLegacyIP(botID string) {
	m.legacyMu.Lock()
	delete(m.legacyIPs, botID)
	m.legacyMu.Unlock()
}

// clearLegacyRoute evicts any stale TCP fallback state for a bot so future
// gRPC dials use the bridge container's Unix socket.
func (m *Manager) clearLegacyRoute(botID string) {
	m.ClearLegacyIP(botID)
	m.grpcPool.Remove(botID)
}

func (m *Manager) nativeMCPClient(ctx context.Context, botID string) (*bridge.Client, error) {
	if provider, ok := m.service.(bridge.Provider); ok {
		client, err := provider.MCPClient(ctx, botID)
		if err == nil {
			return client, nil
		}
		if !errors.Is(err, ctr.ErrNotSupported) && !ctr.IsNotFound(err) {
			return nil, err
		}
	}
	return m.grpcPool.Get(ctx, botID)
}

func (m *Manager) NativeMCPClient(ctx context.Context, botID string) (*bridge.Client, error) {
	return m.nativeMCPClient(ctx, botID)
}

// MCPClient implements bridge.Provider and resolves the request-scoped target
// override before falling back to the Bot's persisted Primary target.
func (m *Manager) MCPClient(ctx context.Context, botID string) (*bridge.Client, error) {
	if targetID := WorkspaceTargetFromContext(ctx); targetID != "" {
		if targetID == WorkspaceTargetNative {
			return m.nativeMCPClient(ctx, botID)
		}
		target, err := m.ResolveWorkspaceTarget(ctx, botID, targetID)
		return target.Client, err
	}
	if m.remote != nil {
		if target, primary, err := m.remote.ResolvePrimary(ctx, botID); err != nil || primary {
			return target.Client, err
		}
	}
	return m.nativeMCPClient(ctx, botID)
}

// CurrentWorkspaceTargetID resolves only the request or persisted target ID.
// It intentionally avoids connecting to a runtime or loading target settings.
func (m *Manager) CurrentWorkspaceTargetID(ctx context.Context, botID string) (string, error) {
	if targetID := WorkspaceTargetFromContext(ctx); targetID != "" {
		return targetID, nil
	}
	if m.remote == nil {
		return WorkspaceTargetNative, nil
	}
	record, err := m.remote.getPrimaryRecord(ctx, botID)
	if errors.Is(err, ErrRemoteWorkspaceNotBound) {
		return WorkspaceTargetNative, nil
	}
	if err != nil {
		return "", err
	}
	return record.ID, nil
}

func (m *Manager) ResolveWorkspaceTarget(ctx context.Context, botID, targetID string) (ResolvedWorkspaceTarget, error) {
	targetID = strings.TrimSpace(targetID)
	if targetID == "" {
		targetID = WorkspaceTargetFromContext(ctx)
	}
	if targetID == "" && m.remote != nil {
		if target, primary, err := m.remote.ResolvePrimary(ctx, botID); err != nil || primary {
			return target, err
		}
	}
	if targetID == "" || targetID == WorkspaceTargetNative {
		primary := targetID == ""
		if targetID == WorkspaceTargetNative && m.remote != nil {
			if _, err := m.remote.GetPrimaryMount(ctx, botID); err == nil {
				primary = false
			} else if !errors.Is(err, ErrRemoteWorkspaceNotBound) {
				return ResolvedWorkspaceTarget{}, err
			} else {
				primary = true
			}
		}
		client, err := m.nativeMCPClient(ctx, botID)
		if err != nil {
			return ResolvedWorkspaceTarget{}, err
		}
		info, err := m.nativeWorkspaceInfo(ctx, botID)
		if err != nil {
			return ResolvedWorkspaceTarget{}, err
		}
		approval, err := m.nativeToolApprovalConfig(ctx, botID)
		if err != nil {
			return ResolvedWorkspaceTarget{}, err
		}
		return ResolvedWorkspaceTarget{
			TargetID: info.TargetID,
			Kind:     info.TargetKind,
			Name:     info.TargetName,
			Primary:  primary,
			Client:   client,
			Info:     info,
			Approval: approval,
		}, nil
	}
	if _, ok := canonicalWorkspaceUUID(targetID); !ok || m.remote == nil {
		return ResolvedWorkspaceTarget{}, ErrWorkspaceTargetNotFound
	}
	return m.remote.ResolveMount(ctx, botID, targetID)
}

func (m *Manager) WaitForWorkspaceReady(ctx context.Context, botID string) error {
	deadline := time.Now().Add(bridgeReadyTimeout)
	var lastErr error
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, bridgeReadyRPCTimeout)
		client, err := m.nativeMCPClient(attemptCtx, botID)
		if err == nil {
			_, err = client.Stat(attemptCtx, "/")
		}
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		m.grpcPool.Remove(botID)
		if time.Now().After(deadline) {
			return fmt.Errorf("workspace bridge not ready for bot %s after %s: %w", botID, bridgeReadyTimeout, lastErr)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for workspace bridge: %w", ctx.Err())
		case <-time.After(bridgeReadyRetryInterval):
		}
	}
}

// InitializeNativeWorkspace applies server-owned bootstrap content after the
// native workspace bridge is reachable. Remote Runtime targets intentionally do
// not pass through this method.
func (m *Manager) InitializeNativeWorkspace(ctx context.Context, botID string) error {
	client, err := m.nativeMCPClient(ctx, botID)
	if err != nil {
		return fmt.Errorf("%w: resolve native workspace filesystem: %w", ErrWorkspaceTemplateBootstrapFailed, err)
	}
	if err := validateWorkspaceContract(ctx, client); err != nil {
		return err
	}
	if m.templateBootstrap == nil {
		return fmt.Errorf("%w: template bootstrapper is not configured", ErrWorkspaceTemplateBootstrapFailed)
	}
	if err := m.templateBootstrap.Bootstrap(
		ctx,
		bridgeWorkspaceFileSystem{client: client},
		defaultWorkspaceBootstrapRoot,
	); err != nil {
		return fmt.Errorf("%w: %w", ErrWorkspaceTemplateBootstrapFailed, err)
	}
	return nil
}

func (m *Manager) WorkspaceInfo(ctx context.Context, botID string) (bridge.WorkspaceInfo, error) {
	if targetID := WorkspaceTargetFromContext(ctx); targetID != "" {
		target, err := m.ResolveWorkspaceTarget(ctx, botID, targetID)
		return target.Info, err
	}
	if m.remote != nil {
		if target, primary, err := m.remote.ResolvePrimary(ctx, botID); err != nil || primary {
			return target.Info, err
		}
	}
	return m.nativeWorkspaceInfo(ctx, botID)
}

func (m *Manager) nativeWorkspaceInfo(ctx context.Context, botID string) (bridge.WorkspaceInfo, error) {
	if provider, ok := m.service.(bridge.WorkspaceInfoProvider); ok {
		info, err := provider.WorkspaceInfo(ctx, botID)
		if err == nil {
			return withNativeWorkspaceTarget(withACPToolsEndpoint(info)), nil
		}
		if !errors.Is(err, ctr.ErrNotSupported) && !ctr.IsNotFound(err) {
			return bridge.WorkspaceInfo{}, err
		}
	}
	info := bridge.WorkspaceInfo{
		Backend:        bridge.WorkspaceBackendContainer,
		DefaultWorkDir: config.DefaultDataMount,
	}
	return withNativeWorkspaceTarget(withACPToolsEndpoint(info)), nil
}

func (m *Manager) nativeToolApprovalConfig(ctx context.Context, botID string) (settings.ToolApprovalConfig, error) {
	config := settings.DefaultToolApprovalConfig()
	if m.queries == nil {
		return config, nil
	}
	id, err := db.ParseUUID(botID)
	if err != nil {
		return settings.ToolApprovalConfig{}, err
	}
	row, err := m.queries.GetSettingsByBotID(ctx, id)
	if err != nil {
		return settings.ToolApprovalConfig{}, err
	}
	if len(row.ToolApprovalConfig) > 0 {
		if err := json.Unmarshal(row.ToolApprovalConfig, &config); err != nil {
			return settings.ToolApprovalConfig{}, err
		}
	}
	return settings.NormalizeToolApprovalConfig(config), nil
}

func (m *Manager) ListWorkspaceTargets(ctx context.Context, botID string) ([]WorkspaceTarget, error) {
	approval, err := m.nativeToolApprovalConfig(ctx, botID)
	if err != nil {
		return nil, err
	}
	native := WorkspaceTarget{
		TargetID:           WorkspaceTargetNative,
		Kind:               WorkspaceTargetNative,
		Name:               "Server Workspace",
		Primary:            true,
		Status:             WorkspaceTargetStatusOffline,
		ToolApproval:       WorkspaceToolApprovalModes(approval),
		ToolApprovalConfig: approval,
	}
	if status, statusErr := m.GetContainerInfo(ctx, botID); statusErr == nil {
		native.Online = status.TaskRunning
		if native.Online {
			native.Status = WorkspaceTargetStatusOnline
		} else if strings.TrimSpace(status.Status) != "" {
			native.Status = status.Status
		}
	} else if !errors.Is(statusErr, ErrContainerNotFound) && !ctr.IsNotFound(statusErr) {
		return nil, statusErr
	}
	targets := []WorkspaceTarget{native}
	if m.remote == nil {
		return targets, nil
	}
	remoteTargets, err := m.remote.ListMounts(ctx, botID)
	if err != nil {
		return nil, err
	}
	for _, target := range remoteTargets {
		if target.Primary {
			targets[0].Primary = false
		}
		targets = append(targets, target)
	}
	return targets, nil
}

func withACPToolsEndpoint(info bridge.WorkspaceInfo) bridge.WorkspaceInfo {
	if strings.TrimSpace(info.Backend) != bridge.WorkspaceBackendContainer {
		return info
	}
	if strings.TrimSpace(info.ACPToolsHTTPURL) != "" {
		return info
	}
	info.ACPToolsHTTPURL = ACPToolsProxyHTTPURL
	return info
}

func withNativeWorkspaceTarget(info bridge.WorkspaceInfo) bridge.WorkspaceInfo {
	info.TargetID = WorkspaceTargetNative
	info.TargetKind = WorkspaceTargetNative
	info.TargetName = "Server Workspace"
	return info
}

func (m *Manager) Init(ctx context.Context) error {
	image := m.imageRef()
	result, err := m.PrepareImageForCreate(ctx, image, &ctr.PullImageOptions{
		Unpack:        true,
		StorageDriver: m.cfg.Snapshotter,
	})
	if err != nil {
		m.logger.Warn("base image preparation failed", slog.String("image", image), slog.Any("error", err))
		return err
	}
	if result.Mode == ImagePrepareDelegated {
		m.logger.Info("base image pull delegated to container backend", slog.String("image", image))
	}
	return nil
}

// EnsureBot creates the workspace container for a bot if it does not exist.
// Bot data lives in the container's writable layer (snapshot), not bind mounts.
// Only the bridge binary is injected as a read-only file mount; the workspace
// image owns its toolkit and runtime scripts.
// If imageOverride is non-empty, it is used instead of the configured default.
func (m *Manager) EnsureBot(ctx context.Context, botID, imageOverride string) error {
	image := m.imageRef()
	if imageOverride != "" {
		image = config.NormalizeImageRef(imageOverride)
	}
	gpu, err := m.resolveWorkspaceGPU(ctx, botID)
	if err != nil {
		return err
	}
	return m.ensureBotWithImage(ctx, botID, image, gpu)
}

func workspaceCDIDevicesLabelValue(devices []string) string {
	devices = normalizeWorkspaceGPUDevices(devices)
	return strings.Join(devices, ",")
}

func workspaceCDIDevicesFromLabels(labels map[string]string) []string {
	if len(labels) == 0 {
		return nil
	}
	value := strings.TrimSpace(labels[WorkspaceCDIDevicesLabelKey])
	if value == "" {
		return nil
	}
	return normalizeWorkspaceGPUDevices(strings.Split(value, ","))
}

func (m *Manager) buildWorkspaceContainerSpec(ctx context.Context, botID string, gpu WorkspaceGPUConfig) (ctr.ContainerSpec, error) {
	resolvPath, err := ctr.ResolveConfSource(m.dataRoot())
	if err != nil {
		return ctr.ContainerSpec{}, err
	}

	bridgePath := m.cfg.BridgeBinaryPath()
	sockDir := m.socketDir(botID)
	if err := os.MkdirAll(sockDir, 0o750); err != nil {
		return ctr.ContainerSpec{}, fmt.Errorf("create socket dir: %w", err)
	}

	mounts := []ctr.MountSpec{
		{
			Destination: "/etc/resolv.conf",
			Type:        "bind",
			Source:      resolvPath,
			Options:     []string{"rbind", "ro"},
		},
		{
			Destination: WorkspaceBridgePath,
			Type:        "bind",
			Source:      bridgePath,
			Options:     []string{"bind", "ro"},
		},
		{
			Destination: "/run/memoh",
			Type:        "bind",
			Source:      sockDir,
			Options:     []string{"rbind", "rw"},
		},
	}
	tzMounts, tzEnv := ctr.TimezoneSpec()
	mounts = append(mounts, tzMounts...)
	if m.bridgeTLS != nil {
		bridgeDir := strings.TrimSpace(m.bridgeTLS.BridgeMaterialDir)
		if bridgeDir == "" || strings.TrimSpace(m.bridgeTLS.ExpectedClientURI) == "" {
			return ctr.ContainerSpec{}, fmt.Errorf("%w: bridge TLS strict mode requires bridge material dir and expected client URI", ctr.ErrInvalidArgument)
		}
		mounts = append(mounts, ctr.MountSpec{
			Destination: bridgeMTLSMountPath,
			Type:        "bind",
			Source:      bridgeDir,
			Options:     []string{"rbind", "ro"},
		})
	}

	skillRoots, err := m.ResolveWorkspaceSkillDiscoveryRoots(ctx, botID)
	if err != nil {
		return ctr.ContainerSpec{}, err
	}
	skillEnv := skillset.ContainerEnv(skillRoots)
	env := make([]string, 0, len(tzEnv)+1+len(skillEnv))
	env = append(env, tzEnv...)
	env = append(env, "BRIDGE_SOCKET_PATH=/run/memoh/bridge.sock")
	env = m.appendBridgeTLSEnv(env)
	if m.botDisplayEnabled(ctx, botID) {
		env = append(env,
			"MEMOH_DISPLAY_ENABLED=true",
			"MEMOH_DISPLAY_RFB_TCP_ADDR=127.0.0.1:5999",
			"DISPLAY=:99",
		)
	}
	env = append(env, skillEnv...)

	return ctr.ContainerSpec{
		Cmd:        []string{WorkspaceInitPath, "-g", "--", WorkspaceBridgePath},
		Mounts:     mounts,
		Env:        env,
		CDIDevices: normalizeWorkspaceGPUDevices(gpu.Devices),
	}, nil
}

func (m *Manager) appendBridgeTLSEnv(env []string) []string {
	if m.bridgeTLS == nil {
		return env
	}
	return append(env,
		"BRIDGE_TLS_MODE="+config.BridgeTLSModeStrict,
		"BRIDGE_TLS_CERT_FILE="+bridgeMTLSMountPath+"/"+bridgeServerCertFile,
		"BRIDGE_TLS_KEY_FILE="+bridgeMTLSMountPath+"/"+bridgeServerKeyFile,
		"BRIDGE_TLS_CLIENT_CA_FILE="+bridgeMTLSMountPath+"/"+serverClientCACertFile,
		"BRIDGE_TLS_EXPECTED_CLIENT_URI="+m.bridgeTLS.ExpectedClientURI,
	)
}

func (m *Manager) botDisplayEnabled(ctx context.Context, botID string) bool {
	if m.queries == nil {
		return false
	}
	id, err := db.ParseUUID(botID)
	if err != nil {
		return false
	}
	row, err := m.queries.GetSettingsByBotID(ctx, id)
	if err != nil {
		return false
	}
	return row.DisplayEnabled
}

func (m *Manager) BotDisplayEnabled(ctx context.Context, botID string) bool {
	return m.botDisplayEnabled(ctx, botID)
}

func (m *Manager) DisplayDialContext(ctx context.Context, botID, network, address string) (net.Conn, error) {
	client, err := m.nativeMCPClient(ctx, botID)
	if err != nil {
		return nil, err
	}
	return client.DialContext(ctx, network, address)
}

func (m *Manager) ensureBotWithImage(ctx context.Context, botID, image string, gpu WorkspaceGPUConfig) error {
	if err := validateBotID(botID); err != nil {
		return err
	}
	containerID := m.resolveContainerID(ctx, botID)
	if _, err := m.service.GetContainer(ctx, containerID); err == nil {
		return nil
	} else if !ctr.IsNotFound(err) {
		return err
	}
	spec, err := m.buildWorkspaceContainerSpec(ctx, botID, gpu)
	if err != nil {
		return err
	}
	limits, err := m.resourceLimitsForCreate(ctx, botID)
	if err != nil {
		return err
	}

	labels := map[string]string{
		BotLabelKey:       botID,
		WorkspaceLabelKey: WorkspaceLabelValue,
	}
	for k, v := range resourceLimitLabels(limits) {
		labels[k] = v
	}
	if value := workspaceCDIDevicesLabelValue(gpu.Devices); value != "" {
		labels[WorkspaceCDIDevicesLabelKey] = value
	}

	_, err = m.service.CreateContainer(ctx, ctr.CreateContainerRequest{
		ID:              containerID,
		ImageRef:        image,
		ImagePullPolicy: m.cfg.EffectiveImagePullPolicy(),
		StorageRef:      ctr.StorageRef{Driver: m.cfg.Snapshotter, Kind: "active"},
		ResourceLimits:  limits,
		Labels:          labels,
		Spec:            spec,
	})
	if err == nil {
		return nil
	}

	if !ctr.IsAlreadyExists(err) {
		return err
	}

	return nil
}

// ListBots returns the bot IDs that have workspace containers.
func (m *Manager) ListBots(ctx context.Context) ([]string, error) {
	containers, err := m.service.ListContainers(ctx)
	if err != nil {
		return nil, err
	}

	botIDs := make([]string, 0, len(containers))
	for _, info := range containers {
		if botID, ok := BotIDFromContainerInfo(info); ok {
			botIDs = append(botIDs, botID)
		}
	}
	return botIDs, nil
}

func (m *Manager) Start(ctx context.Context, botID string) error {
	image, err := m.resolveWorkspaceImage(ctx, botID)
	if err != nil {
		return err
	}
	gpu, err := m.resolveWorkspaceGPU(ctx, botID)
	if err != nil {
		return err
	}
	return m.startWithResolvedConfig(ctx, botID, image, gpu)
}

// StartWithImage creates and starts the MCP container for a bot.
// If imageOverride is non-empty, it is used as the base image instead of the
// configured default. The override only applies when creating a new container.
func (m *Manager) StartWithImage(ctx context.Context, botID, imageOverride string) error {
	image := strings.TrimSpace(imageOverride)
	if image == "" {
		return m.Start(ctx, botID)
	}
	gpu, err := m.resolveWorkspaceGPU(ctx, botID)
	if err != nil {
		return err
	}
	return m.startWithResolvedConfig(ctx, botID, config.NormalizeImageRef(image), gpu)
}

// StartWithResolvedImage creates and starts the workspace container for a bot
// using an explicit image reference.
func (m *Manager) StartWithResolvedImage(ctx context.Context, botID, image string) error {
	image = strings.TrimSpace(image)
	if image == "" {
		return errors.New("image is required")
	}
	gpu, err := m.resolveWorkspaceGPU(ctx, botID)
	if err != nil {
		return err
	}
	return m.startWithResolvedConfig(ctx, botID, image, gpu)
}

func (m *Manager) StartWithResolvedConfig(ctx context.Context, botID, image string, gpu WorkspaceGPUConfig) error {
	image = strings.TrimSpace(image)
	if image == "" {
		return errors.New("image is required")
	}
	return m.startWithResolvedConfig(ctx, botID, image, gpu)
}

func (m *Manager) startWithResolvedConfig(ctx context.Context, botID, image string, gpu WorkspaceGPUConfig) error {
	containerID := m.resolveContainerID(ctx, botID)
	unlock := m.lockContainer(containerID)
	defer unlock()

	// Before creating a new container, check for an orphaned snapshot
	// (container deleted but snapshot with /data survived). Export /data
	// to a backup so it can be restored after EnsureBot creates a fresh
	// container. This covers dev image rebuilds, containerd metadata loss,
	// and manual container deletion.
	if _, err := m.service.GetContainer(ctx, containerID); ctr.IsNotFound(err) {
		m.recoverOrphanedSnapshot(ctx, botID)
	}

	if err := m.ensureBotWithImage(ctx, botID, image, gpu); err != nil {
		return err
	}

	// Restore preserved data (from orphaned snapshot recovery or a previous
	// CleanupBotContainer with preserveData) into the fresh snapshot before
	// starting the task when the backend exposes snapshot mounts. Backends
	// without mount support restore through the bridge after the task starts.
	restoreAfterStart := false
	if m.HasPreservedData(botID) {
		info, err := m.service.GetContainer(ctx, containerID)
		if err != nil {
			return fmt.Errorf("load workspace for preserved data restore: %w", err)
		}
		if err := m.restorePreservedIntoSnapshot(ctx, botID, info); err != nil {
			if errors.Is(err, errMountNotSupported) {
				restoreAfterStart = true
			} else {
				return fmt.Errorf("restore preserved data: %w", err)
			}
		}
	}

	// Start the task and restore the container network so workspace processes
	// regain outbound connectivity. Server communication still uses UDS.
	if err := m.startTaskAndEnsureNetwork(ctx, botID, containerID); err != nil {
		if stopErr := m.service.StopContainer(ctx, containerID, &ctr.StopTaskOptions{Force: true}); stopErr != nil {
			m.logger.Warn("cleanup: stop task failed", slog.String("container_id", containerID), slog.Any("error", stopErr))
		}
		return err
	}
	if restoreAfterStart {
		if err := m.restorePreservedDataViaGRPC(ctx, botID); err != nil {
			return fmt.Errorf("restore preserved data through bridge: %w", err)
		}
	}
	if !m.IsLegacyContainer(ctx, containerID) {
		m.clearLegacyRoute(botID)
	}
	if err := m.runWorkspaceHook(context.WithoutCancel(ctx), botID, hooks.EventWorkspaceStart, map[string]any{
		"backend": bridge.WorkspaceBackendContainer,
		"image":   image,
	}); err != nil {
		m.logWorkspaceHookError(hooks.EventWorkspaceStart, botID, err)
	}
	return nil
}

func (m *Manager) Stop(ctx context.Context, botID string, timeout time.Duration) error {
	if err := validateBotID(botID); err != nil {
		return err
	}
	if err := m.runWorkspaceHook(ctx, botID, hooks.EventWorkspaceStop, map[string]any{
		"timeout_seconds": int(timeout.Seconds()),
	}); err != nil {
		m.logWorkspaceHookError(hooks.EventWorkspaceStop, botID, err)
	}
	return m.service.StopContainer(ctx, m.resolveContainerID(ctx, botID), &ctr.StopTaskOptions{
		Timeout: timeout,
		Force:   true,
	})
}

func (m *Manager) Delete(ctx context.Context, botID string, preserveData bool) error {
	if err := validateBotID(botID); err != nil {
		return err
	}

	containerID := m.resolveContainerID(ctx, botID)

	if preserveData {
		if err := m.preserveDataBeforeDelete(ctx, botID); err != nil {
			return fmt.Errorf("preserve data: %w", err)
		}
	}

	m.clearLegacyRoute(botID)

	if err := m.removeContainerNetwork(ctx, botID, containerID); err != nil {
		m.logger.Warn("delete: remove network failed",
			slog.String("container_id", containerID), slog.Any("error", err))
	}
	if err := m.service.DeleteTask(ctx, containerID, &ctr.DeleteTaskOptions{Force: true}); err != nil {
		m.logger.Warn("delete: delete task failed",
			slog.String("container_id", containerID), slog.Any("error", err))
	}
	return m.service.DeleteContainer(ctx, containerID, &ctr.DeleteContainerOptions{
		CleanupSnapshot: true,
	})
}

func (m *Manager) dataRoot() string {
	return m.cfg.DataRootPath()
}

func (m *Manager) imageRef() string {
	return m.cfg.ImageRef()
}

// IsLegacyContainer returns true if the container was created before the
// bridge process architecture (uses the legacy "mcp-" prefix).
// Legacy containers are functional but unreachable from the server (they
// use TCP gRPC instead of UDS). Users should delete and recreate them.
func (*Manager) IsLegacyContainer(_ context.Context, containerID string) bool {
	return strings.HasPrefix(containerID, LegacyContainerPrefix)
}

func validateBotID(botID string) error {
	return identity.ValidateChannelIdentityID(botID)
}
