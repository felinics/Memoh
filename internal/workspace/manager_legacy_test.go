package workspace

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/memohai/memoh/internal/config"
	ctr "github.com/memohai/memoh/internal/container"
	netctl "github.com/memohai/memoh/internal/network"
	"github.com/memohai/memoh/internal/workspace/bridge"
)

type legacyRouteTestService struct {
	container  ctr.ContainerInfo
	created    bool
	byLabel    []ctr.ContainerInfo
	createdReq ctr.CreateContainerRequest

	createCalls int
	startCalls  int
	deleteCalls int
	removeNet   int
	deleteTask  int
	setupNet    int

	getContainerBeforeCreateErr error
	getImageErr                 error
	getImageErrs                map[string]error
	pullErr                     error
	pullErrs                    map[string]error
	pullCalls                   int
	pullRefs                    []string
	getImageCalls               int
	getImageRefs                []string
	setupNetworkResults         []ctr.NetworkResult
	setupNetworkErrs            []error
}

type workspaceInfoProviderTestService struct {
	legacyRouteTestService
	info bridge.WorkspaceInfo
}

func (s *workspaceInfoProviderTestService) WorkspaceInfo(context.Context, string) (bridge.WorkspaceInfo, error) {
	return s.info, nil
}

func (s *legacyRouteTestService) PullImage(_ context.Context, ref string, _ *ctr.PullImageOptions) (ctr.ImageInfo, error) {
	s.pullCalls++
	s.pullRefs = append(s.pullRefs, ref)
	if s.pullErrs != nil {
		if err, ok := s.pullErrs[ref]; ok {
			return ctr.ImageInfo{}, err
		}
	}
	if s.pullErr != nil {
		return ctr.ImageInfo{}, s.pullErr
	}
	return ctr.ImageInfo{Name: ref, ID: ref, Tags: []string{ref}}, nil
}

func (s *legacyRouteTestService) GetImage(_ context.Context, ref string) (ctr.ImageInfo, error) {
	s.getImageCalls++
	s.getImageRefs = append(s.getImageRefs, ref)
	if s.getImageErrs != nil {
		if err, ok := s.getImageErrs[ref]; ok {
			return ctr.ImageInfo{}, err
		}
	}
	if s.getImageErr != nil {
		return ctr.ImageInfo{}, s.getImageErr
	}
	return ctr.ImageInfo{Name: ref, ID: ref, Tags: []string{ref}}, nil
}

func (*legacyRouteTestService) ListImages(context.Context) ([]ctr.ImageInfo, error) {
	return nil, nil
}

func (*legacyRouteTestService) DeleteImage(context.Context, string, *ctr.DeleteImageOptions) error {
	return nil
}

func (*legacyRouteTestService) ResolveRemoteDigest(context.Context, string) (string, error) {
	return "", nil
}

func (s *legacyRouteTestService) CreateContainer(_ context.Context, req ctr.CreateContainerRequest) (ctr.ContainerInfo, error) {
	s.createCalls++
	s.created = true
	s.createdReq = req
	s.container = ctr.ContainerInfo{
		ID:         req.ID,
		Image:      req.ImageRef,
		Labels:     req.Labels,
		StorageRef: ctr.StorageRef{Driver: req.StorageRef.Driver, Key: req.ID, Kind: "active"},
	}
	return s.container, nil
}

func (s *legacyRouteTestService) GetContainer(context.Context, string) (ctr.ContainerInfo, error) {
	if !s.created {
		if s.getContainerBeforeCreateErr != nil {
			return ctr.ContainerInfo{}, s.getContainerBeforeCreateErr
		}
		return ctr.ContainerInfo{}, ctr.ErrNotFound
	}
	return s.container, nil
}

func (s *legacyRouteTestService) ListContainers(context.Context) ([]ctr.ContainerInfo, error) {
	if !s.created {
		return nil, nil
	}
	return []ctr.ContainerInfo{s.container}, nil
}

func (s *legacyRouteTestService) DeleteContainer(context.Context, string, *ctr.DeleteContainerOptions) error {
	s.deleteCalls++
	s.created = false
	return nil
}

func (s *legacyRouteTestService) ListContainersByLabel(context.Context, string, string) ([]ctr.ContainerInfo, error) {
	return s.byLabel, nil
}

func (s *legacyRouteTestService) StartContainer(context.Context, string, *ctr.StartTaskOptions) error {
	s.startCalls++
	return nil
}

func (*legacyRouteTestService) StopContainer(context.Context, string, *ctr.StopTaskOptions) error {
	return nil
}

func (s *legacyRouteTestService) DeleteTask(context.Context, string, *ctr.DeleteTaskOptions) error {
	s.deleteTask++
	return nil
}

func (*legacyRouteTestService) GetTaskInfo(context.Context, string) (ctr.TaskInfo, error) {
	return ctr.TaskInfo{}, ctr.ErrNotFound
}

func (*legacyRouteTestService) GetContainerMetrics(context.Context, string) (ctr.ContainerMetrics, error) {
	return ctr.ContainerMetrics{}, ctr.ErrNotSupported
}

func (*legacyRouteTestService) ListTasks(context.Context, *ctr.ListTasksOptions) ([]ctr.TaskInfo, error) {
	return nil, nil
}

func (s *legacyRouteTestService) SetupNetwork(context.Context, ctr.NetworkRequest) (ctr.NetworkResult, error) {
	idx := s.setupNet
	s.setupNet++
	if idx < len(s.setupNetworkErrs) && s.setupNetworkErrs[idx] != nil {
		return ctr.NetworkResult{}, s.setupNetworkErrs[idx]
	}
	if idx < len(s.setupNetworkResults) {
		return s.setupNetworkResults[idx], nil
	}
	return ctr.NetworkResult{IP: "10.0.0.2"}, nil
}

func (s *legacyRouteTestService) RemoveNetwork(context.Context, ctr.NetworkRequest) error {
	s.removeNet++
	return nil
}

func (*legacyRouteTestService) CheckNetwork(context.Context, ctr.NetworkRequest) error {
	return nil
}

func (*legacyRouteTestService) CommitSnapshot(context.Context, ctr.CommitSnapshotRequest) error {
	return nil
}

func (*legacyRouteTestService) ListSnapshots(context.Context, ctr.ListSnapshotsRequest) ([]ctr.SnapshotInfo, error) {
	return nil, nil
}

func (*legacyRouteTestService) PrepareSnapshot(context.Context, ctr.PrepareSnapshotRequest) error {
	return nil
}

func (*legacyRouteTestService) RestoreContainer(context.Context, ctr.CreateContainerRequest) (ctr.ContainerInfo, error) {
	return ctr.ContainerInfo{}, nil
}

func (*legacyRouteTestService) SnapshotMounts(context.Context, string, string) ([]ctr.MountInfo, error) {
	return nil, ctr.ErrNotSupported
}

func (*legacyRouteTestService) SnapshotUsage(context.Context, string, string) (ctr.SnapshotUsage, error) {
	return ctr.SnapshotUsage{}, ctr.ErrNotSupported
}

func newLegacyRouteTestManager(t *testing.T, svc runtimeService, cfg config.WorkspaceConfig) *Manager {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	m := &Manager{
		service:           svc,
		networkController: netctl.NewController(netctl.NewContainerRuntimeFromBackend("containerd", svc), nil, nil),
		cfg:               cfg,
		namespace:         config.DefaultNamespace,
		containerLocks:    make(map[string]*sync.Mutex),
		legacyIPs:         make(map[string]string),
		logger:            logger,
	}
	m.grpcPool = bridge.NewPool(m.dialTarget)
	return m
}

func TestBuildWorkspaceContainerSpecInjectsBridgeTLSMaterial(t *testing.T) {
	dataRoot := t.TempDir()
	bridgePath := filepath.Join(t.TempDir(), "bridge")
	bridgeDir := t.TempDir()
	expectedClientURI := config.ServerClientSPIFFE("instance-1")
	m := newLegacyRouteTestManager(t, &legacyRouteTestService{}, config.WorkspaceConfig{
		DataRoot:   dataRoot,
		BridgePath: bridgePath,
	})
	m.SetBridgeTLS(&BridgeTLSRuntimeOptions{
		Client:            &bridge.TLSOptions{},
		BridgeMaterialDir: bridgeDir,
		ExpectedClientURI: expectedClientURI,
	})

	spec, err := m.buildWorkspaceContainerSpec(context.Background(), "00000000-0000-0000-0000-000000000001", WorkspaceGPUConfig{})
	if err != nil {
		t.Fatalf("build spec: %v", err)
	}
	if got, want := strings.Join(spec.Cmd, " "), WorkspaceInitPath+" -g -- "+WorkspaceBridgePath; got != want {
		t.Fatalf("workspace command = %q, want %q", got, want)
	}

	var foundMount, foundBridgeMount bool
	for _, mount := range spec.Mounts {
		if mount.Destination == WorkspaceBridgePath {
			foundBridgeMount = true
			if mount.Source != bridgePath {
				t.Fatalf("bridge mount source = %q, want %q", mount.Source, bridgePath)
			}
			if strings.Join(mount.Options, ",") != "bind,ro" {
				t.Fatalf("bridge mount options = %v", mount.Options)
			}
		}
		if mount.Destination == bridgeMTLSMountPath {
			foundMount = true
			if mount.Source != bridgeDir {
				t.Fatalf("bridge TLS mount source = %q, want %q", mount.Source, bridgeDir)
			}
			if strings.Join(mount.Options, ",") != "rbind,ro" {
				t.Fatalf("bridge TLS mount options = %v", mount.Options)
			}
		}
	}
	if !foundMount {
		t.Fatalf("missing bridge TLS mount at %s", bridgeMTLSMountPath)
	}
	if !foundBridgeMount {
		t.Fatal("missing read-only bridge binary mount")
	}

	env := envMap(spec.Env)
	wantEnv := map[string]string{
		"BRIDGE_SOCKET_PATH":             "/run/memoh/bridge.sock",
		"BRIDGE_TLS_MODE":                config.BridgeTLSModeStrict,
		"BRIDGE_TLS_CERT_FILE":           bridgeMTLSMountPath + "/" + bridgeServerCertFile,
		"BRIDGE_TLS_KEY_FILE":            bridgeMTLSMountPath + "/" + bridgeServerKeyFile,
		"BRIDGE_TLS_CLIENT_CA_FILE":      bridgeMTLSMountPath + "/" + serverClientCACertFile,
		"BRIDGE_TLS_EXPECTED_CLIENT_URI": expectedClientURI,
	}
	for key, want := range wantEnv {
		if got := env[key]; got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

func envMap(items []string) map[string]string {
	out := make(map[string]string, len(items))
	for _, item := range items {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			out[key] = value
		}
	}
	return out
}

func TestStartWithImageClearsLegacyRouteForBridgeContainer(t *testing.T) {
	dataRoot := t.TempDir()
	runtimeDir := filepath.Join(dataRoot, "runtime")
	if err := os.MkdirAll(runtimeDir, 0o750); err != nil {
		t.Fatalf("mkdir runtime dir: %v", err)
	}

	svc := &legacyRouteTestService{}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{
		DataRoot:     dataRoot,
		RuntimeDir:   runtimeDir,
		Snapshotter:  "overlayfs",
		CNIBinaryDir: "/opt/cni/bin",
		CNIConfigDir: "/etc/cni/net.d",
	})

	botID := "00000000-0000-0000-0000-000000000001"
	m.SetLegacyIP(botID, "10.0.0.9")

	if got := m.dialTarget(botID); got != "passthrough:///10.0.0.9:9090" {
		t.Fatalf("expected legacy dial target before start, got %q", got)
	}

	if err := m.StartWithImage(context.Background(), botID, ""); err != nil {
		t.Fatalf("StartWithImage failed: %v", err)
	}

	if got := m.dialTarget(botID); got != "unix://"+filepath.Join(dataRoot, "run", botID, "bridge.sock") {
		t.Fatalf("expected unix dial target after bridge start, got %q", got)
	}
	if svc.createCalls != 1 || svc.startCalls != 1 {
		t.Fatalf("expected create/start once, got create=%d start=%d", svc.createCalls, svc.startCalls)
	}
}

func TestStartWithImageDoesNotRecreateExistingContainer(t *testing.T) {
	dataRoot := t.TempDir()
	runtimeDir := filepath.Join(dataRoot, "runtime")
	if err := os.MkdirAll(runtimeDir, 0o750); err != nil {
		t.Fatalf("mkdir runtime dir: %v", err)
	}

	botID := "00000000-0000-0000-0000-000000000001"
	svc := &legacyRouteTestService{
		created: true,
		container: ctr.ContainerInfo{
			ID:    ContainerPrefix + botID,
			Image: "docker.io/memohai/workspace:debian",
		},
	}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{
		DataRoot:     dataRoot,
		RuntimeDir:   runtimeDir,
		Snapshotter:  "overlayfs",
		CNIBinaryDir: "/opt/cni/bin",
		CNIConfigDir: "/etc/cni/net.d",
	})

	if err := m.StartWithImage(context.Background(), botID, ""); err != nil {
		t.Fatalf("StartWithImage failed: %v", err)
	}
	if svc.createCalls != 0 {
		t.Fatalf("existing container was recreated %d times", svc.createCalls)
	}
	if svc.startCalls != 1 {
		t.Fatalf("existing container start calls = %d, want 1", svc.startCalls)
	}
}

func TestWorkspaceInfoAddsACPToolsEndpointForProviderContainer(t *testing.T) {
	svc := &workspaceInfoProviderTestService{
		info: bridge.WorkspaceInfo{
			Backend:        bridge.WorkspaceBackendContainer,
			DefaultWorkDir: "/data",
		},
	}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{DataRoot: t.TempDir()})

	info, err := m.WorkspaceInfo(context.Background(), "bot-1")
	if err != nil {
		t.Fatal(err)
	}
	if info.ACPToolsHTTPURL != ACPToolsProxyHTTPURL {
		t.Fatalf("ACPToolsHTTPURL = %q", info.ACPToolsHTTPURL)
	}
}

func TestDeleteClearsLegacyRoute(t *testing.T) {
	svc := &legacyRouteTestService{created: true, container: ctr.ContainerInfo{ID: "workspace-00000000-0000-0000-0000-000000000001"}}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{
		DataRoot:     t.TempDir(),
		Snapshotter:  "overlayfs",
		CNIBinaryDir: "/opt/cni/bin",
		CNIConfigDir: "/etc/cni/net.d",
	})

	botID := "00000000-0000-0000-0000-000000000001"
	m.SetLegacyIP(botID, "10.0.0.9")

	if err := m.Delete(context.Background(), botID, false); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	if got := m.dialTarget(botID); got == "passthrough:///10.0.0.9:9090" {
		t.Fatalf("expected legacy TCP target to be cleared, got %q", got)
	}
	if svc.removeNet != 1 || svc.deleteTask != 1 || svc.deleteCalls != 1 {
		t.Fatalf("expected delete cleanup once, got removeNet=%d deleteTask=%d delete=%d", svc.removeNet, svc.deleteTask, svc.deleteCalls)
	}
}

func TestEnsureContainerNetworkAndGetIPRejectsEmptyIP(t *testing.T) {
	svc := &legacyRouteTestService{
		setupNetworkResults: []ctr.NetworkResult{{IP: ""}, {IP: "10.0.0.3"}},
	}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{
		CNIBinaryDir: "/opt/cni/bin",
		CNIConfigDir: "/etc/cni/net.d",
	})

	ip, err := m.ensureContainerNetworkAndGetIP(context.Background(), "", "workspace-bot")
	if err != nil {
		t.Fatalf("ensureContainerNetworkAndGetIP failed: %v", err)
	}
	if ip != "10.0.0.3" {
		t.Fatalf("expected retry IP, got %q", ip)
	}
	if svc.setupNet != 2 {
		t.Fatalf("expected two network setup attempts, got %d", svc.setupNet)
	}
}

func TestContainerIDPrefersCurrentLabelSearch(t *testing.T) {
	t.Parallel()

	botID := "00000000-0000-0000-0000-000000000001"
	svc := &legacyRouteTestService{
		byLabel: []ctr.ContainerInfo{{
			ID:        "workspace-from-label",
			Labels:    map[string]string{BotLabelKey: botID},
			UpdatedAt: time.Now(),
		}},
	}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{})

	containerID, err := m.ContainerID(context.Background(), botID)
	if err != nil {
		t.Fatalf("ContainerID failed: %v", err)
	}
	if containerID != "workspace-from-label" {
		t.Fatalf("expected label-resolved container ID, got %q", containerID)
	}
}

func TestContainerIDFallsBackToNameInference(t *testing.T) {
	t.Parallel()

	botID := "00000000-0000-0000-0000-000000000001"
	svc := &legacyRouteTestService{
		created: true,
		container: ctr.ContainerInfo{
			ID:        ContainerPrefix + botID,
			UpdatedAt: time.Now(),
		},
	}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{})

	containerID, err := m.ContainerID(context.Background(), botID)
	if err != nil {
		t.Fatalf("ContainerID failed: %v", err)
	}
	if containerID != ContainerPrefix+botID {
		t.Fatalf("expected inferred container ID, got %q", containerID)
	}
}
