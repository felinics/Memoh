package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"slices"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/config"
	ctr "github.com/felinics/memoh/internal/container"
	"github.com/felinics/memoh/internal/db"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/settings"
	"github.com/felinics/memoh/internal/userruntime"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
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
	records   []dbstore.BotRemoteRuntimeBindingRecord
	createErr error
	deleteErr error
}

func (s *fakeRemoteBindingStore) CreateOrUpdateMount(_ context.Context, botID, runtimeID string) (dbstore.BotRemoteRuntimeBindingRecord, error) {
	if s.createErr != nil {
		return dbstore.BotRemoteRuntimeBindingRecord{}, s.createErr
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
	raw, _ := json.Marshal(DefaultRemoteToolApprovalConfig())
	record := dbstore.BotRemoteRuntimeBindingRecord{
		ID: targetID, BotID: botID, RuntimeID: runtimeID,
		RuntimeName:   "Office Mac",
		RuntimeUserID: remoteTestOwnerID, BotOwnerUserID: remoteTestOwnerID,
		ToolApproval: raw,
	}
	s.records = append(s.records, record)
	return record, nil
}

func (s *fakeRemoteBindingStore) ListMounts(_ context.Context, botID string) ([]dbstore.BotRemoteRuntimeBindingRecord, error) {
	var records []dbstore.BotRemoteRuntimeBindingRecord
	for _, record := range s.records {
		if record.BotID == botID {
			records = append(records, record)
		}
	}
	return records, nil
}

func (s *fakeRemoteBindingStore) ListGrantsByRuntimeOwner(_ context.Context, ownerUserID string) ([]dbstore.BotRemoteRuntimeBindingRecord, error) {
	var records []dbstore.BotRemoteRuntimeBindingRecord
	for _, record := range s.records {
		if record.BotOwnerUserID == ownerUserID {
			records = append(records, record)
		}
	}
	return records, nil
}

func (s *fakeRemoteBindingStore) GetMount(_ context.Context, botID, targetID string) (dbstore.BotRemoteRuntimeBindingRecord, error) {
	for _, record := range s.records {
		if record.BotID == botID && record.ID == targetID {
			return record, nil
		}
	}
	return dbstore.BotRemoteRuntimeBindingRecord{}, db.ErrNotFound
}

func (s *fakeRemoteBindingStore) GetPrimaryMount(_ context.Context, botID string) (dbstore.BotRemoteRuntimeBindingRecord, error) {
	for _, record := range s.records {
		if record.BotID == botID && record.IsPrimary {
			return record, nil
		}
	}
	return dbstore.BotRemoteRuntimeBindingRecord{}, db.ErrNotFound
}

func (s *fakeRemoteBindingStore) SetPrimary(ctx context.Context, botID, targetID string) error {
	if targetID != WorkspaceTargetNative {
		if _, err := s.GetMount(ctx, botID, targetID); err != nil {
			return err
		}
	}
	for i := range s.records {
		if s.records[i].BotID == botID {
			s.records[i].IsPrimary = targetID != WorkspaceTargetNative && s.records[i].ID == targetID
		}
	}
	return nil
}

func (s *fakeRemoteBindingStore) UpdateToolApproval(_ context.Context, botID, targetID string, config dbstore.JSON) error {
	for i := range s.records {
		if s.records[i].BotID == botID && s.records[i].ID == targetID {
			s.records[i].ToolApproval = append(dbstore.JSON(nil), config...)
			return nil
		}
	}
	return db.ErrNotFound
}

func (s *fakeRemoteBindingStore) DeleteMount(_ context.Context, botID, targetID string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	for i := range s.records {
		if s.records[i].BotID == botID && s.records[i].ID == targetID {
			s.records = append(s.records[:i], s.records[i+1:]...)
			return nil
		}
	}
	return db.ErrNotFound
}

func TestDeleteMountRemovesTarget(t *testing.T) {
	store := &fakeRemoteBindingStore{
		records: []dbstore.BotRemoteRuntimeBindingRecord{{
			ID: remoteTestTargetID, BotID: remoteTestBotID, RuntimeID: remoteTestRuntimeID,
		}},
	}
	service := &RemoteWorkspaceService{store: store}

	if err := service.DeleteMount(context.Background(), remoteTestBotID, remoteTestTargetID); err != nil {
		t.Fatalf("DeleteMount() error = %v", err)
	}
	if len(store.records) != 0 {
		t.Fatalf("target records = %d, want 0", len(store.records))
	}
}

type fakeRuntimeConnections map[string]*userruntime.Connection

func (f fakeRuntimeConnections) Connection(runtimeID string) (*userruntime.Connection, bool) {
	connection, ok := f[runtimeID]
	return connection, ok
}

type remoteScopeCaptureServer struct {
	pb.UnimplementedContainerServiceServer
	metadata chan metadata.MD
}

func (s *remoteScopeCaptureServer) Stat(ctx context.Context, _ *pb.StatRequest) (*pb.StatResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	s.metadata <- md
	return &pb.StatResponse{Entry: &pb.FileEntry{IsDir: true}}, nil
}

func TestRemoteWorkspaceMountsAreIndependentAndPrimaryIsUnique(t *testing.T) {
	store := &fakeRemoteBindingStore{}
	service := &RemoteWorkspaceService{store: store, runtimes: fakeRuntimeConnections{}}

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
	if err := service.SetPrimary(context.Background(), remoteTestBotID, WorkspaceTargetNative); err != nil {
		t.Fatalf("SetPrimary native: %v", err)
	}
	if store.records[0].IsPrimary || store.records[1].IsPrimary {
		t.Fatal("remote primary remains after selecting native")
	}
}

func TestListAccountGrantsScopesByOwnerAndValidatesID(t *testing.T) {
	store := &fakeRemoteBindingStore{records: []dbstore.BotRemoteRuntimeBindingRecord{
		{ID: remoteTestTargetID, BotID: remoteTestBotID, RuntimeID: remoteTestRuntimeID, IsPrimary: true, BotOwnerUserID: remoteTestOwnerID},
		{ID: remoteTestTargetID2, BotID: remoteTestBotID2, RuntimeID: remoteTestRuntimeID, BotOwnerUserID: "55555555-5555-4555-8555-555555555555"},
	}}
	service := &RemoteWorkspaceService{store: store, runtimes: fakeRuntimeConnections{}}

	grants, err := service.ListAccountGrants(context.Background(), remoteTestOwnerID)
	if err != nil {
		t.Fatalf("ListAccountGrants: %v", err)
	}
	if len(grants) != 1 {
		t.Fatalf("grants = %#v", grants)
	}
	if grants[0].TargetID != remoteTestTargetID || grants[0].BotID != remoteTestBotID || grants[0].RuntimeID != remoteTestRuntimeID || !grants[0].Primary {
		t.Fatalf("grant = %#v", grants[0])
	}

	if _, err := service.ListAccountGrants(context.Background(), "not-a-uuid"); !errors.Is(err, userruntime.ErrInvalidInput) {
		t.Fatalf("invalid owner id err = %v", err)
	}
}

func TestRemoteWorkspaceDefaultApprovalDoesNotInheritNativeBypasses(t *testing.T) {
	config := DefaultRemoteToolApprovalConfig()
	if config.Read.Mode != settings.ToolApprovalAllow || config.Write.Mode != settings.ToolApprovalAllow || config.Exec.Mode != settings.ToolApprovalAllow {
		t.Fatalf("modes = %#v", config)
	}
	if len(config.Read.BypassGlobs) != 0 || len(config.Write.BypassGlobs) != 0 || len(config.Exec.BypassCommands) != 0 {
		t.Fatalf("remote default inherited bypasses: %#v", config)
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal remote default: %v", err)
	}
	roundTrip := toolApprovalConfig(raw)
	if len(roundTrip.Read.BypassGlobs) != 0 || len(roundTrip.Write.BypassGlobs) != 0 || len(roundTrip.Exec.BypassCommands) != 0 {
		t.Fatalf("remote default inherited bypasses after persistence: %#v", roundTrip)
	}
}

func TestRemoteWorkspaceApprovalIsIsolatedPerBotBinding(t *testing.T) {
	firstConfig := DefaultRemoteToolApprovalConfig()
	secondConfig := DefaultRemoteToolApprovalConfig()
	firstRaw, _ := json.Marshal(firstConfig)
	secondRaw, _ := json.Marshal(secondConfig)
	store := &fakeRemoteBindingStore{records: []dbstore.BotRemoteRuntimeBindingRecord{
		{ID: remoteTestTargetID, BotID: remoteTestBotID, RuntimeID: remoteTestRuntimeID, ToolApproval: firstRaw},
		{ID: remoteTestTargetID2, BotID: remoteTestBotID2, RuntimeID: remoteTestRuntimeID, ToolApproval: secondRaw},
	}}
	service := &RemoteWorkspaceService{store: store}

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
	store := &fakeRemoteBindingStore{records: []dbstore.BotRemoteRuntimeBindingRecord{{
		ID: remoteTestTargetID, BotID: remoteTestBotID, RuntimeID: remoteTestRuntimeID,
		RuntimeName:   "Previous owner's Mac",
		RuntimeUserID: remoteTestOwnerID, BotOwnerUserID: "55555555-5555-4555-8555-555555555555",
	}}}
	service := &RemoteWorkspaceService{store: store, runtimes: fakeRuntimeConnections{}}
	target, err := service.GetMount(context.Background(), remoteTestBotID, remoteTestTargetID)
	if err != nil {
		t.Fatalf("GetMount: %v", err)
	}
	if target.TargetID != remoteTestTargetID || target.Status != WorkspaceTargetStatusOwnerMismatch {
		t.Fatalf("target = %#v", target)
	}
	if target.Name != "" {
		t.Fatalf("previous owner data leaked: %#v", target)
	}
	if err := service.DeleteMount(context.Background(), remoteTestBotID, remoteTestTargetID); err != nil {
		t.Fatalf("DeleteMount: %v", err)
	}
}

func TestRemoteWorkspaceTargetInUseCannotBeDeleted(t *testing.T) {
	store := &fakeRemoteBindingStore{
		records: []dbstore.BotRemoteRuntimeBindingRecord{{
			ID: remoteTestTargetID, BotID: remoteTestBotID, RuntimeID: remoteTestRuntimeID,
		}},
		deleteErr: db.ErrWorkspaceTargetInUse,
	}
	service := &RemoteWorkspaceService{store: store}

	err := service.DeleteMount(context.Background(), remoteTestBotID, remoteTestTargetID)
	if !errors.Is(err, ErrWorkspaceTargetInUse) {
		t.Fatalf("DeleteMount() error = %v, want ErrWorkspaceTargetInUse", err)
	}
	if len(store.records) != 1 || store.records[0].ID != remoteTestTargetID {
		t.Fatalf("referenced target was removed: %#v", store.records)
	}
}

func TestRemotePrimaryOfflineNeverFallsBackToNative(t *testing.T) {
	store := &fakeRemoteBindingStore{records: []dbstore.BotRemoteRuntimeBindingRecord{{
		ID: remoteTestTargetID, BotID: remoteTestBotID, RuntimeID: remoteTestRuntimeID,
		IsPrimary:     true,
		RuntimeUserID: remoteTestOwnerID, BotOwnerUserID: remoteTestOwnerID,
	}}}
	manager := NewManager(slog.Default(), nil, nil, config.WorkspaceConfig{}, "", nil)
	manager.SetRemoteWorkspaceService(&RemoteWorkspaceService{store: store, runtimes: fakeRuntimeConnections{}})
	if _, err := manager.MCPClient(context.Background(), remoteTestBotID); !errors.Is(err, ErrRemoteRuntimeOffline) {
		t.Fatalf("MCPClient error = %v, want offline", err)
	}
	if _, err := manager.ResolveWorkspaceTarget(context.Background(), remoteTestBotID, remoteTestTargetID); !errors.Is(err, ErrRemoteRuntimeOffline) {
		t.Fatalf("explicit target error = %v, want offline", err)
	}
}

func TestRemotePrimaryDoesNotHideNativeContainerStatus(t *testing.T) {
	nativeInfo := ctr.ContainerInfo{
		ID:     "workspace-" + remoteTestBotID,
		Image:  "debian:bookworm",
		Labels: map[string]string{BotLabelKey: remoteTestBotID, WorkspaceLabelKey: WorkspaceLabelValue},
	}
	native := &legacyRouteTestService{created: true, container: nativeInfo, byLabel: []ctr.ContainerInfo{nativeInfo}}
	store := &fakeRemoteBindingStore{records: []dbstore.BotRemoteRuntimeBindingRecord{{
		ID: remoteTestTargetID, BotID: remoteTestBotID, RuntimeID: remoteTestRuntimeID,
		IsPrimary:     true,
		RuntimeUserID: remoteTestOwnerID, BotOwnerUserID: remoteTestOwnerID,
	}}}
	manager := NewManager(slog.Default(), native, nil, config.WorkspaceConfig{}, "", nil)
	manager.SetRemoteWorkspaceService(&RemoteWorkspaceService{store: store, runtimes: fakeRuntimeConnections{}})

	status, err := manager.GetContainerInfo(context.Background(), remoteTestBotID)
	if err != nil {
		t.Fatalf("GetContainerInfo: %v", err)
	}
	if status.ContainerID != nativeInfo.ID || status.WorkspaceBackend == bridge.WorkspaceBackendRemote {
		t.Fatalf("native status was hidden by remote primary: %#v", status)
	}
}

func TestRemoteWorkspaceClientUsesHostFilesystemCapability(t *testing.T) {
	rootClient, captured := newRemoteScopeTestClient(t)
	capabilities := []string{
		userruntime.CapabilityFS,
		userruntime.CapabilityExec,
		userruntime.CapabilityHostFS,
		userruntime.CapabilityACPCodex,
		userruntime.CapabilityACPClaudeCode,
	}
	store := &fakeRemoteBindingStore{records: []dbstore.BotRemoteRuntimeBindingRecord{{
		ID: remoteTestTargetID, BotID: remoteTestBotID, RuntimeID: remoteTestRuntimeID,
		RuntimeName:   "Office Mac",
		IsPrimary:     true,
		RuntimeUserID: remoteTestOwnerID, BotOwnerUserID: remoteTestOwnerID,
	}}}
	service := &RemoteWorkspaceService{
		store: store,
		runtimes: fakeRuntimeConnections{remoteTestRuntimeID: {
			RuntimeID: remoteTestRuntimeID,
			Client:    rootClient,
			Info: userruntime.RuntimeInfo{
				WorkspaceBase: "/Users/alice",
				OS:            "darwin",
				Capabilities:  capabilities,
			},
		}},
	}
	target, err := service.ResolveMount(context.Background(), remoteTestBotID, remoteTestTargetID)
	if err != nil {
		t.Fatalf("ResolveMount: %v", err)
	}
	if target.Info.Backend != bridge.WorkspaceBackendRemote || target.Info.DefaultWorkDir != "/Users/alice" ||
		target.Info.TargetID != remoteTestTargetID || target.Info.TargetKind != WorkspaceTargetRemote || target.Info.TargetName != "Office Mac" {
		t.Fatalf("workspace info = %#v", target.Info)
	}
	if got := strings.Join(target.Info.Capabilities, ","); got != strings.Join(capabilities, ",") {
		t.Fatalf("workspace capabilities = %q, want %q", got, strings.Join(capabilities, ","))
	}
	target.Info.Capabilities[0] = "mutated"
	connection, ok := service.runtimes.Connection(remoteTestRuntimeID)
	if !ok || connection.Info.Capabilities[0] != userruntime.CapabilityFS {
		t.Fatalf("resolved WorkspaceInfo aliases runtime capabilities: %#v", connection)
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
	store := &fakeRemoteBindingStore{records: []dbstore.BotRemoteRuntimeBindingRecord{{
		ID: remoteTestTargetID, BotID: remoteTestBotID, RuntimeID: remoteTestRuntimeID,
		RuntimeUserID: remoteTestOwnerID, BotOwnerUserID: remoteTestOwnerID,
	}}}
	service := &RemoteWorkspaceService{
		store: store,
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
	if _, err := service.ResolveMount(context.Background(), remoteTestBotID, remoteTestTargetID); !errors.Is(err, ErrRemoteRuntimeClientUpdateNeeded) {
		t.Fatalf("ResolveMount error = %v, want client update required", err)
	}
}

func newRemoteScopeTestClient(t *testing.T) (*bridge.Client, <-chan metadata.MD) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	captured := make(chan metadata.MD, 1)
	server := grpc.NewServer()
	pb.RegisterContainerServiceServer(server, &remoteScopeCaptureServer{metadata: captured})
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
