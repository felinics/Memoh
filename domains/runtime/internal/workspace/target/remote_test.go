package target

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"slices"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	"github.com/memohai/memoh/domains/api/setting"
	"github.com/memohai/memoh/domains/runtime/bridge/bridgepb"
	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
	userruntime "github.com/memohai/memoh/domains/runtime/client"
	ctr "github.com/memohai/memoh/domains/runtime/container"
	workspaceimpl "github.com/memohai/memoh/domains/runtime/internal/workspace"
	runtimeworkspace "github.com/memohai/memoh/domains/runtime/workspace"
	"github.com/memohai/memoh/internal/config"
)

const (
	remoteTestBotID      = "11111111-1111-4111-8111-111111111111"
	remoteTestBotID2     = "11111111-1111-4111-8111-111111111112"
	remoteTestRuntimeID  = "22222222-2222-4222-8222-222222222222"
	remoteTestRuntimeID2 = "22222222-2222-4222-8222-222222222223"
	remoteTestTargetID   = "44444444-4444-4444-8444-444444444444"
	remoteTestTargetID2  = "44444444-4444-4444-8444-444444444445"
	remoteTestOwnerID    = "33333333-3333-4333-8333-333333333333"
)

type fakeRemoteBindingStore struct {
	records   []runtimeworkspace.RemoteMountRecord
	createErr error
}

func (s *fakeRemoteBindingStore) CreateOrUpdateMount(_ context.Context, botID, runtimeID, ownerUserID string) (runtimeworkspace.RemoteMountRecord, error) {
	if s.createErr != nil {
		return runtimeworkspace.RemoteMountRecord{}, s.createErr
	}
	for i := range s.records {
		if s.records[i].BotID == botID && s.records[i].RuntimeID == runtimeID {
			return s.records[i], nil
		}
	}
	targetID := remoteTestTargetID
	if len(s.records) > 0 {
		targetID = remoteTestTargetID2
	}
	raw, _ := json.Marshal(runtimeworkspace.DefaultRemoteToolApprovalConfig())
	record := runtimeworkspace.RemoteMountRecord{
		ID: targetID, BotID: botID, RuntimeID: runtimeID,
		RuntimeName:   "Office Mac",
		RuntimeUserID: ownerUserID,
		ToolApproval:  raw,
	}
	s.records = append(s.records, record)
	return record, nil
}

func (s *fakeRemoteBindingStore) ListMounts(_ context.Context, botID string) ([]runtimeworkspace.RemoteMountRecord, error) {
	var records []runtimeworkspace.RemoteMountRecord
	for _, record := range s.records {
		if record.BotID == botID {
			records = append(records, record)
		}
	}
	return records, nil
}

func (s *fakeRemoteBindingStore) GetMount(_ context.Context, botID, targetID string) (runtimeworkspace.RemoteMountRecord, error) {
	for _, record := range s.records {
		if record.BotID == botID && record.ID == targetID {
			return record, nil
		}
	}
	return runtimeworkspace.RemoteMountRecord{}, runtimeworkspace.ErrRecordNotFound
}

func (s *fakeRemoteBindingStore) GetPrimaryMount(_ context.Context, botID string) (runtimeworkspace.RemoteMountRecord, error) {
	for _, record := range s.records {
		if record.BotID == botID && record.IsPrimary {
			return record, nil
		}
	}
	return runtimeworkspace.RemoteMountRecord{}, runtimeworkspace.ErrRecordNotFound
}

func (s *fakeRemoteBindingStore) SetPrimary(ctx context.Context, botID, targetID string) error {
	if targetID != runtimeworkspace.WorkspaceTargetNative {
		if _, err := s.GetMount(ctx, botID, targetID); err != nil {
			return err
		}
	}
	for i := range s.records {
		if s.records[i].BotID == botID {
			s.records[i].IsPrimary = targetID != runtimeworkspace.WorkspaceTargetNative && s.records[i].ID == targetID
		}
	}
	return nil
}

func (s *fakeRemoteBindingStore) RunPrimaryMountTransaction(ctx context.Context, fn func(runtimeworkspace.PrimaryMountTransaction) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if fn == nil {
		return errors.New("primary mount transaction callback is required")
	}

	snapshot := slices.Clone(s.records)
	if err := fn(s); err != nil {
		s.records = snapshot
		return err
	}
	if err := ctx.Err(); err != nil {
		s.records = snapshot
		return err
	}
	return nil
}

func (s *fakeRemoteBindingStore) UpdateToolApproval(_ context.Context, botID, targetID string, config []byte) error {
	for i := range s.records {
		if s.records[i].BotID == botID && s.records[i].ID == targetID {
			s.records[i].ToolApproval = append([]byte(nil), config...)
			return nil
		}
	}
	return runtimeworkspace.ErrRecordNotFound
}

func (s *fakeRemoteBindingStore) DeleteMount(_ context.Context, botID, targetID string) error {
	for i := range s.records {
		if s.records[i].BotID == botID && s.records[i].ID == targetID {
			s.records = append(s.records[:i], s.records[i+1:]...)
			return nil
		}
	}
	return runtimeworkspace.ErrRecordNotFound
}

type fakeRuntimeConnections map[string]*userruntime.Connection

func (f fakeRuntimeConnections) Connection(runtimeID string) (*userruntime.Connection, bool) {
	connection, ok := f[runtimeID]
	return connection, ok
}

type fakeBotOwners map[string]string

func (f fakeBotOwners) BotOwnerUserID(_ context.Context, botID string) (string, error) {
	ownerUserID, found := f[botID]
	if !found {
		return "", errors.New("bot not found")
	}
	return ownerUserID, nil
}

func defaultBotOwners() fakeBotOwners {
	return fakeBotOwners{
		remoteTestBotID:  remoteTestOwnerID,
		remoteTestBotID2: remoteTestOwnerID,
	}
}

type remoteScopeCaptureServer struct {
	bridgepb.UnimplementedContainerServiceServer
	metadata chan metadata.MD
}

func (s *remoteScopeCaptureServer) Stat(ctx context.Context, _ *bridgepb.StatRequest) (*bridgepb.StatResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	s.metadata <- md
	return &bridgepb.StatResponse{Entry: &bridgepb.FileEntry{IsDir: true}}, nil
}

func TestRemoteWorkspaceMountsAreIndependentAndPrimaryIsUnique(t *testing.T) {
	store := &fakeRemoteBindingStore{}
	service := &RemoteWorkspaceService{store: store, runtimes: fakeRuntimeConnections{}, owners: defaultBotOwners()}

	first, err := service.Mount(context.Background(), remoteTestBotID, remoteTestRuntimeID)
	if err != nil {
		t.Fatalf("Mount first: %v", err)
	}
	if first.TargetID != remoteTestTargetID {
		t.Fatalf("first target = %#v", first)
	}
	second, err := service.Mount(context.Background(), remoteTestBotID, remoteTestRuntimeID2)
	if err != nil {
		t.Fatalf("Mount second: %v", err)
	}
	if second.TargetID == first.TargetID {
		t.Fatal("mounts share a target ID")
	}

	updated, err := service.Mount(context.Background(), remoteTestBotID, remoteTestRuntimeID)
	if err != nil {
		t.Fatalf("update first: %v", err)
	}
	if updated.TargetID != first.TargetID || len(store.records) != 2 {
		t.Fatalf("upsert created duplicate: target=%q records=%d", updated.TargetID, len(store.records))
	}

	if err := service.SetPrimary(context.Background(), remoteTestBotID, first.TargetID); err != nil {
		t.Fatalf("SetPrimary first: %v", err)
	}
	if err := service.SetPrimary(context.Background(), remoteTestBotID, second.TargetID); err != nil {
		t.Fatalf("SetPrimary second: %v", err)
	}
	if store.records[0].IsPrimary || !store.records[1].IsPrimary {
		t.Fatalf("primary flags = %v, %v", store.records[0].IsPrimary, store.records[1].IsPrimary)
	}
	if err := service.SetPrimary(context.Background(), remoteTestBotID, runtimeworkspace.WorkspaceTargetNative); err != nil {
		t.Fatalf("SetPrimary native: %v", err)
	}
	if store.records[0].IsPrimary || store.records[1].IsPrimary {
		t.Fatal("remote primary remains after selecting native")
	}
}

func TestRemoteWorkspaceDefaultApprovalDoesNotInheritNativeBypasses(t *testing.T) {
	config := runtimeworkspace.DefaultRemoteToolApprovalConfig()
	if config.Read.Mode != setting.ToolApprovalAllow || config.Write.Mode != setting.ToolApprovalAsk || config.Exec.Mode != setting.ToolApprovalAsk {
		t.Fatalf("modes = %#v", config)
	}
	if len(config.Read.BypassGlobs) != 0 || len(config.Write.BypassGlobs) != 0 || len(config.Exec.BypassCommands) != 0 {
		t.Fatalf("remote default inherited bypasses: %#v", config)
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal remote default: %v", err)
	}
	roundTrip := runtimeworkspace.ToolApprovalConfigFromRaw(raw)
	if len(roundTrip.Read.BypassGlobs) != 0 || len(roundTrip.Write.BypassGlobs) != 0 || len(roundTrip.Exec.BypassCommands) != 0 {
		t.Fatalf("remote default inherited bypasses after persistence: %#v", roundTrip)
	}
}

func TestRemoteWorkspaceApprovalIsIsolatedPerBotBinding(t *testing.T) {
	firstConfig := runtimeworkspace.DefaultRemoteToolApprovalConfig()
	secondConfig := runtimeworkspace.DefaultRemoteToolApprovalConfig()
	firstRaw, _ := json.Marshal(firstConfig)
	secondRaw, _ := json.Marshal(secondConfig)
	store := &fakeRemoteBindingStore{records: []runtimeworkspace.RemoteMountRecord{
		{ID: remoteTestTargetID, BotID: remoteTestBotID, RuntimeID: remoteTestRuntimeID, ToolApproval: firstRaw},
		{ID: remoteTestTargetID2, BotID: remoteTestBotID2, RuntimeID: remoteTestRuntimeID, ToolApproval: secondRaw},
	}}
	service := &RemoteWorkspaceService{store: store, owners: defaultBotOwners()}

	firstConfig.Enabled = false
	firstConfig.Write.BypassGlobs = []string{"shared/**"}
	if err := service.UpdateToolApprovalConfig(context.Background(), remoteTestBotID, remoteTestTargetID, firstConfig); err != nil {
		t.Fatalf("UpdateToolApprovalConfig: %v", err)
	}
	other, err := service.GetMount(context.Background(), remoteTestBotID2, remoteTestTargetID2)
	if err != nil {
		t.Fatalf("GetMount second Bot: %v", err)
	}
	if !other.ToolApprovalConfig.Enabled || slices.Contains(other.ToolApprovalConfig.Write.BypassGlobs, "shared/**") {
		t.Fatalf("second Bot policy was changed: %#v", other.ToolApprovalConfig)
	}
}

func TestOwnerMismatchIsRedactedButTargetCanBeDeleted(t *testing.T) {
	const newOwnerID = "55555555-5555-4555-8555-555555555555"
	store := &fakeRemoteBindingStore{records: []runtimeworkspace.RemoteMountRecord{{
		ID: remoteTestTargetID, BotID: remoteTestBotID, RuntimeID: remoteTestRuntimeID,
		RuntimeName:   "Previous owner's Mac",
		RuntimeUserID: remoteTestOwnerID,
	}}}
	service := &RemoteWorkspaceService{
		store: store, runtimes: fakeRuntimeConnections{},
		owners: fakeBotOwners{remoteTestBotID: newOwnerID},
	}
	target, err := service.GetMount(context.Background(), remoteTestBotID, remoteTestTargetID)
	if err != nil {
		t.Fatalf("GetMount: %v", err)
	}
	if target.TargetID != remoteTestTargetID || target.Status != runtimeworkspace.WorkspaceTargetStatusOwnerMismatch {
		t.Fatalf("target = %#v", target)
	}
	if target.Name != "" {
		t.Fatalf("previous owner data leaked: %#v", target)
	}
	if err := service.DeleteMount(context.Background(), remoteTestBotID, remoteTestTargetID); err != nil {
		t.Fatalf("DeleteMount: %v", err)
	}
}

func TestRemoteWorkspaceListGetAndPrimaryExposeRevokedRuntime(t *testing.T) {
	store := &fakeRemoteBindingStore{records: []runtimeworkspace.RemoteMountRecord{{
		ID: remoteTestTargetID, BotID: remoteTestBotID, RuntimeID: remoteTestRuntimeID,
		RuntimeName: "Revoked Mac", RuntimeUserID: remoteTestOwnerID,
		RuntimeRevoked: true, IsPrimary: true,
	}}}
	service := &RemoteWorkspaceService{store: store, runtimes: fakeRuntimeConnections{}, owners: defaultBotOwners()}

	listed, err := service.ListMounts(t.Context(), remoteTestBotID)
	if err != nil || len(listed) != 1 || listed[0].Status != runtimeworkspace.WorkspaceTargetStatusRevoked {
		t.Fatalf("ListMounts() = %#v, %v", listed, err)
	}
	got, err := service.GetMount(t.Context(), remoteTestBotID, remoteTestTargetID)
	if err != nil || got.Status != runtimeworkspace.WorkspaceTargetStatusRevoked {
		t.Fatalf("GetMount() = %#v, %v", got, err)
	}
	primary, err := service.GetPrimaryMount(t.Context(), remoteTestBotID)
	if err != nil || primary.Status != runtimeworkspace.WorkspaceTargetStatusRevoked || !primary.Primary {
		t.Fatalf("GetPrimaryMount() = %#v, %v", primary, err)
	}
}

func TestRemoteWorkspaceClientUsesHostFilesystemCapability(t *testing.T) {
	rootClient, captured := newRemoteScopeTestClient(t)
	store := &fakeRemoteBindingStore{records: []runtimeworkspace.RemoteMountRecord{{
		ID: remoteTestTargetID, BotID: remoteTestBotID, RuntimeID: remoteTestRuntimeID,
		IsPrimary:     true,
		RuntimeUserID: remoteTestOwnerID, BotOwnerUserID: remoteTestOwnerID,
	}}}
	service := &RemoteWorkspaceService{
		store:  store,
		owners: defaultBotOwners(),
		runtimes: fakeRuntimeConnections{remoteTestRuntimeID: {
			RuntimeID: remoteTestRuntimeID,
			Client:    rootClient,
			Info: userruntime.RuntimeInfo{
				WorkspaceBase: "/Users/alice",
				OS:            "darwin",
				Capabilities:  []string{userruntime.CapabilityFS, userruntime.CapabilityExec, userruntime.CapabilityHostFS},
			},
		}},
	}
	target, err := service.ResolveMount(context.Background(), remoteTestBotID, remoteTestTargetID)
	if err != nil {
		t.Fatalf("ResolveMount: %v", err)
	}
	if target.Info.Backend != bridge.WorkspaceBackendRemote || target.Info.DefaultWorkDir != "/Users/alice" {
		t.Fatalf("workspace info = %#v", target.Info)
	}
	if _, err := target.Client.Stat(context.Background(), "/Users/alice"); err != nil {
		t.Fatalf("Stat: %v", err)
	}
	md := <-captured
	if got := md.Get("x-memoh-workspace-id"); len(got) != 0 {
		t.Fatalf("obsolete workspace id metadata = %v", got)
	}
	if got := md.Get("x-memoh-workspace-path-bin"); len(got) != 0 {
		t.Fatalf("obsolete workspace path metadata = %v", got)
	}
}

func TestRemoteWorkspaceRejectsLegacyScopeCapability(t *testing.T) {
	rootClient, _ := newRemoteScopeTestClient(t)
	store := &fakeRemoteBindingStore{records: []runtimeworkspace.RemoteMountRecord{{
		ID: remoteTestTargetID, BotID: remoteTestBotID, RuntimeID: remoteTestRuntimeID,
		RuntimeUserID: remoteTestOwnerID, BotOwnerUserID: remoteTestOwnerID,
	}}}
	service := &RemoteWorkspaceService{
		store:  store,
		owners: defaultBotOwners(),
		runtimes: fakeRuntimeConnections{remoteTestRuntimeID: {
			RuntimeID: remoteTestRuntimeID,
			Client:    rootClient,
			Info: userruntime.RuntimeInfo{
				WorkspaceBase: "/Users/alice",
				OS:            "darwin",
				Capabilities:  []string{userruntime.CapabilityFS, userruntime.CapabilityExec, "workspace_scope"},
			},
		}},
	}
	if _, err := service.ResolveMount(context.Background(), remoteTestBotID, remoteTestTargetID); !errors.Is(err, runtimeworkspace.ErrRemoteRuntimeClientUpdateNeeded) {
		t.Fatalf("ResolveMount error = %v, want client update required", err)
	}
}

func newRemoteScopeTestClient(t *testing.T) (*bridge.Client, <-chan metadata.MD) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	captured := make(chan metadata.MD, 1)
	server := grpc.NewServer()
	bridgepb.RegisterContainerServiceServer(server, &remoteScopeCaptureServer{metadata: captured})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///remote-scope-test",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return bridge.NewClientFromConn(conn), captured
}

func TestRemotePrimaryOfflineNeverFallsBackToNative(t *testing.T) {
	store := &fakeRemoteBindingStore{records: []runtimeworkspace.RemoteMountRecord{{
		ID: remoteTestTargetID, BotID: remoteTestBotID, RuntimeID: remoteTestRuntimeID,
		IsPrimary:     true,
		RuntimeUserID: remoteTestOwnerID, BotOwnerUserID: remoteTestOwnerID,
	}}}
	manager := workspaceimpl.NewManager(slog.Default(), nil, nil, config.WorkspaceConfig{}, "", nil)
	manager.SetRemoteWorkspaceService(&RemoteWorkspaceService{store: store, runtimes: fakeRuntimeConnections{}, owners: defaultBotOwners()})
	if _, err := manager.MCPClient(context.Background(), remoteTestBotID); !errors.Is(err, runtimeworkspace.ErrRemoteRuntimeOffline) {
		t.Fatalf("MCPClient error = %v, want offline", err)
	}
	if _, err := manager.ResolveWorkspaceTarget(context.Background(), remoteTestBotID, remoteTestTargetID); !errors.Is(err, runtimeworkspace.ErrRemoteRuntimeOffline) {
		t.Fatalf("explicit target error = %v, want offline", err)
	}
}

func TestRemotePrimaryDoesNotHideNativeContainerStatus(t *testing.T) {
	nativeInfo := ctr.ContainerInfo{
		ID:     "workspace-" + remoteTestBotID,
		Image:  "debian:bookworm",
		Labels: map[string]string{runtimeworkspace.BotLabelKey: remoteTestBotID, runtimeworkspace.WorkspaceLabelKey: runtimeworkspace.WorkspaceLabelValue},
	}
	native := &legacyRouteTestService{created: true, container: nativeInfo, byLabel: []ctr.ContainerInfo{nativeInfo}}
	store := &fakeRemoteBindingStore{records: []runtimeworkspace.RemoteMountRecord{{
		ID: remoteTestTargetID, BotID: remoteTestBotID, RuntimeID: remoteTestRuntimeID,
		IsPrimary:     true,
		RuntimeUserID: remoteTestOwnerID, BotOwnerUserID: remoteTestOwnerID,
	}}}
	manager := workspaceimpl.NewManager(slog.Default(), native, nil, config.WorkspaceConfig{}, "", nil)
	manager.SetRemoteWorkspaceService(&RemoteWorkspaceService{store: store, runtimes: fakeRuntimeConnections{}, owners: defaultBotOwners()})

	status, err := manager.GetContainerInfo(context.Background(), remoteTestBotID)
	if err != nil {
		t.Fatalf("GetContainerInfo: %v", err)
	}
	if status.ContainerID != nativeInfo.ID || status.WorkspaceBackend == bridge.WorkspaceBackendRemote {
		t.Fatalf("native status was hidden by remote primary: %#v", status)
	}
}

// legacyRouteTestService is a minimal container runtime stub for Manager
// integration coverage in this package.
type legacyRouteTestService struct {
	container ctr.ContainerInfo
	created   bool
	byLabel   []ctr.ContainerInfo
}

func (*legacyRouteTestService) PullImage(context.Context, string, *ctr.PullImageOptions) (ctr.ImageInfo, error) {
	return ctr.ImageInfo{}, nil
}

func (*legacyRouteTestService) GetImage(context.Context, string) (ctr.ImageInfo, error) {
	return ctr.ImageInfo{}, nil
}
func (*legacyRouteTestService) ListImages(context.Context) ([]ctr.ImageInfo, error) { return nil, nil }
func (*legacyRouteTestService) DeleteImage(context.Context, string, *ctr.DeleteImageOptions) error {
	return nil
}

func (*legacyRouteTestService) ResolveRemoteDigest(context.Context, string) (string, error) {
	return "", nil
}

func (s *legacyRouteTestService) CreateContainer(_ context.Context, req ctr.CreateContainerRequest) (ctr.ContainerInfo, error) {
	s.created = true
	s.container = ctr.ContainerInfo{ID: req.ID, Image: req.ImageRef, Labels: req.Labels}
	return s.container, nil
}

func (s *legacyRouteTestService) GetContainer(context.Context, string) (ctr.ContainerInfo, error) {
	if !s.created {
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
	s.created = false
	return nil
}

func (s *legacyRouteTestService) ListContainersByLabel(context.Context, string, string) ([]ctr.ContainerInfo, error) {
	return s.byLabel, nil
}

func (*legacyRouteTestService) StartContainer(context.Context, string, *ctr.StartTaskOptions) error {
	return nil
}

func (*legacyRouteTestService) StopContainer(context.Context, string, *ctr.StopTaskOptions) error {
	return nil
}

func (*legacyRouteTestService) DeleteTask(context.Context, string, *ctr.DeleteTaskOptions) error {
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

func (*legacyRouteTestService) SetupNetwork(context.Context, ctr.NetworkRequest) (ctr.NetworkResult, error) {
	return ctr.NetworkResult{IP: "10.0.0.2"}, nil
}
func (*legacyRouteTestService) RemoveNetwork(context.Context, ctr.NetworkRequest) error { return nil }
func (*legacyRouteTestService) CheckNetwork(context.Context, ctr.NetworkRequest) error  { return nil }
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
