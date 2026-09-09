package userruntime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/felinics/memoh/internal/db"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	serviceTestRuntimeID = "11111111-1111-4111-8111-111111111111"
	serviceTestTeamID    = "22222222-2222-4222-8222-222222222222"
)

type serviceTestStore struct {
	mu      sync.Mutex
	runtime dbstore.UserRuntimeRecord
	revoked bool
}

func (s *serviceTestStore) CreateUserRuntime(_ context.Context, input dbstore.CreateUserRuntimeInput) (dbstore.UserRuntimeRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runtime = dbstore.UserRuntimeRecord{
		ID: serviceTestRuntimeID, TeamID: serviceTestTeamID, UserID: input.UserID, Name: input.Name,
		APIToken: input.APIToken, PendingExpiresAt: time.Now().UTC().Add(15 * time.Minute), CreatedAt: time.Now().UTC(),
	}
	s.revoked = false
	return s.runtime, nil
}

func (s *serviceTestStore) GetUserRuntimeByAPIToken(_ context.Context, token string) (dbstore.UserRuntimeRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked || s.runtime.ID == "" || s.runtime.APIToken != token ||
		(s.runtime.ActivatedAt.IsZero() && !s.runtime.PendingExpiresAt.After(time.Now())) {
		return dbstore.UserRuntimeRecord{}, db.ErrNotFound
	}
	return s.runtime, nil
}

func (s *serviceTestStore) ActivateUserRuntime(_ context.Context, runtimeID, token string) (dbstore.UserRuntimeRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked || s.runtime.ID != runtimeID || s.runtime.APIToken != token ||
		(s.runtime.ActivatedAt.IsZero() && !s.runtime.PendingExpiresAt.After(time.Now())) {
		return dbstore.UserRuntimeRecord{}, db.ErrNotFound
	}
	if s.runtime.ActivatedAt.IsZero() {
		s.runtime.ActivatedAt = time.Now().UTC()
		s.runtime.PendingExpiresAt = time.Time{}
	}
	return s.runtime, nil
}

func (s *serviceTestStore) ExpirePendingUserRuntimes(_ context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.revoked && s.runtime.UserID == userID && s.runtime.ActivatedAt.IsZero() &&
		!s.runtime.PendingExpiresAt.IsZero() && !s.runtime.PendingExpiresAt.After(time.Now()) {
		s.revoked = true
	}
	return nil
}

func (s *serviceTestStore) ListUserRuntimes(_ context.Context, userID string) ([]dbstore.UserRuntimeRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked || s.runtime.ID == "" || s.runtime.UserID != userID {
		return []dbstore.UserRuntimeRecord{}, nil
	}
	return []dbstore.UserRuntimeRecord{s.runtime}, nil
}

func (s *serviceTestStore) RevokeUserRuntime(_ context.Context, runtimeID, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked || s.runtime.ID != runtimeID || s.runtime.UserID != userID {
		return db.ErrNotFound
	}
	s.revoked = true
	return nil
}

func (s *serviceTestStore) BackfillUserRuntimeName(_ context.Context, runtimeID, userID, name, defaultName string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked || s.runtime.ID != runtimeID || s.runtime.UserID != userID {
		return false, nil
	}
	if s.runtime.Name != "" && s.runtime.Name != defaultName {
		return false, nil
	}
	s.runtime.Name = name
	return true, nil
}

func TestCreateRuntimeDefaultsNameAndHandshakeBackfills(t *testing.T) {
	store := &serviceTestStore{}
	service := NewService(store, NewHub(nil))

	created, err := service.CreateRuntime(context.Background(), "user-1", CreateRuntimeRequest{})
	if err != nil {
		t.Fatalf("CreateRuntime() error = %v", err)
	}
	wantDefault := DefaultRuntimeName(created.Key)
	if created.Name != wantDefault {
		t.Fatalf("default name = %q, want %q", created.Name, wantDefault)
	}

	// A never-connected default claims no row in the list.
	items, err := service.ListRuntimes(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("ListRuntimes() error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("unconnected default listed: %#v", items)
	}

	connection := &Connection{
		ConnectionID: "connection-1",
		Client:       newServiceTestClient(t),
		Close:        func(string) {},
	}
	info := HandshakeInfo{
		Version: 1, Hostname: "qingfengdeMacBook-Pro", OS: "darwin", Arch: "arm64",
		ClientVersion: "test", WorkspaceBase: "/workspace", Capabilities: []string{CapabilityFS},
	}
	if err := service.ActivateConnection(context.Background(), created.Key, created.ID, info, connection, func() error { return nil }); err != nil {
		t.Fatalf("ActivateConnection() error = %v", err)
	}
	store.mu.Lock()
	backfilled := store.runtime.Name
	store.mu.Unlock()
	if backfilled != "qingfengdeMacBook-Pro" {
		t.Fatalf("backfilled name = %q", backfilled)
	}

	// Activation is persisted independently of the Hub: after disconnect and a
	// service restart the machine remains visible and manageable while offline.
	service.DeactivateConnection(created.ID, connection, "test disconnect")
	service = NewService(store, NewHub(nil))
	items, err = service.ListRuntimes(context.Background(), "user-1")
	if err != nil || len(items) != 1 || items[0].Online {
		t.Fatalf("ListRuntimes() = %#v, %v", items, err)
	}
}

func TestHandshakeDoesNotOverwriteUserChosenName(t *testing.T) {
	store := &serviceTestStore{}
	service := NewService(store, NewHub(nil))

	created, err := service.CreateRuntime(context.Background(), "user-1", CreateRuntimeRequest{Name: "Workstation"})
	if err != nil {
		t.Fatalf("CreateRuntime() error = %v", err)
	}
	connection := &Connection{
		ConnectionID: "connection-1",
		Client:       newServiceTestClient(t),
		Close:        func(string) {},
	}
	info := HandshakeInfo{
		Version: 1, Hostname: "other-host", OS: "linux", Arch: "amd64",
		ClientVersion: "test", WorkspaceBase: "/workspace", Capabilities: []string{CapabilityFS},
	}
	if err := service.ActivateConnection(context.Background(), created.Key, created.ID, info, connection, func() error { return nil }); err != nil {
		t.Fatalf("ActivateConnection() error = %v", err)
	}
	store.mu.Lock()
	name := store.runtime.Name
	store.mu.Unlock()
	if name != "Workstation" {
		t.Fatalf("user-chosen name overwritten: %q", name)
	}
}

func TestPendingRuntimeExpiresWithoutBecomingAHiddenCredential(t *testing.T) {
	store := &serviceTestStore{}
	service := NewService(store, NewHub(nil))

	created, err := service.CreateRuntime(context.Background(), "user-1", CreateRuntimeRequest{Name: "Abandoned"})
	if err != nil {
		t.Fatalf("CreateRuntime() error = %v", err)
	}
	store.mu.Lock()
	store.runtime.PendingExpiresAt = time.Now().UTC().Add(-time.Second)
	store.mu.Unlock()

	if _, err := service.AuthenticateKey(context.Background(), created.Key); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("AuthenticateKey() expired pending error = %v, want not found", err)
	}
	items, err := service.ListRuntimes(context.Background(), "user-1")
	if err != nil || len(items) != 0 {
		t.Fatalf("ListRuntimes() expired pending = %#v, %v", items, err)
	}
	store.mu.Lock()
	revoked := store.revoked
	store.mu.Unlock()
	if !revoked {
		t.Fatal("expired pending Runtime was not revoked during cleanup")
	}
}

func TestFailedConnectionCommitDoesNotActivatePendingRuntime(t *testing.T) {
	store := &serviceTestStore{}
	service := NewService(store, NewHub(nil))
	created, err := service.CreateRuntime(context.Background(), "user-1", CreateRuntimeRequest{})
	if err != nil {
		t.Fatalf("CreateRuntime() error = %v", err)
	}
	connection := &Connection{
		ConnectionID: "connection-1",
		Client:       newServiceTestClient(t),
		Close:        func(string) {},
	}
	wantErr := errors.New("transport lost")
	err = service.ActivateConnection(context.Background(), created.Key, created.ID, HandshakeInfo{}, connection, func() error { return wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("ActivateConnection() error = %v, want %v", err, wantErr)
	}
	store.mu.Lock()
	activatedAt := store.runtime.ActivatedAt
	store.mu.Unlock()
	if !activatedAt.IsZero() {
		t.Fatalf("failed connection activated pending Runtime at %v", activatedAt)
	}
}

func TestPublicationFailureDoesNotHideConsumedCredential(t *testing.T) {
	store := &serviceTestStore{}
	service := NewService(store, NewHub(nil))
	created, err := service.CreateRuntime(context.Background(), "user-1", CreateRuntimeRequest{})
	if err != nil {
		t.Fatalf("CreateRuntime() error = %v", err)
	}
	client := newServiceTestClient(t)
	defer client.Close() //nolint:errcheck // test cleanup
	connection := &Connection{ConnectionID: "connection-1", Client: client, Close: func(string) {}}
	wantErr := errors.New("transport lost at publication")
	var checks int
	err = service.ActivateConnection(context.Background(), created.Key, created.ID, HandshakeInfo{}, connection, func() error {
		checks++
		if checks == 2 {
			return wantErr
		}
		return nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("ActivateConnection() error = %v, want %v", err, wantErr)
	}
	items, err := service.ListRuntimes(context.Background(), "user-1")
	if err != nil || len(items) != 1 || items[0].Online {
		t.Fatalf("ListRuntimes() consumed credential = %#v, %v", items, err)
	}
}

func TestCreateRuntimeCapsNameLength(t *testing.T) {
	service := NewService(&serviceTestStore{}, NewHub(nil))
	for name, value := range map[string]string{
		"too long":     strings.Repeat("a", maxRuntimeNameBytes+1),
		"nul byte":     "work\x00station",
		"invalid utf8": "work\xffstation",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.CreateRuntime(context.Background(), "user-1", CreateRuntimeRequest{Name: value}); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("CreateRuntime error = %v, want ErrInvalidInput", err)
			}
		})
	}
	if _, err := service.CreateRuntime(context.Background(), "user-1", CreateRuntimeRequest{Name: strings.Repeat("a", maxRuntimeNameBytes)}); err != nil {
		t.Fatalf("CreateRuntime max-length name error = %v", err)
	}
}

func TestServiceRegistrationConnectionAndRevoke(t *testing.T) {
	store := &serviceTestStore{}
	hub := NewHub(nil)
	service := NewService(store, hub)
	created, err := service.CreateRuntime(context.Background(), "user-1", CreateRuntimeRequest{Name: "Workstation"})
	if err != nil {
		t.Fatalf("CreateRuntime() error = %v", err)
	}
	if err := ValidateKeyFormat(created.Key); err != nil {
		t.Fatalf("created key is invalid: %v", err)
	}
	if created.ID != serviceTestRuntimeID || created.TeamID != serviceTestTeamID || created.Online {
		t.Fatalf("created Runtime = %#v", created)
	}
	store.mu.Lock()
	storedToken := store.runtime.APIToken
	store.mu.Unlock()
	if storedToken != created.Key {
		t.Fatal("Runtime API token was not stored directly")
	}

	client := newServiceTestClient(t)
	var closed atomic.Bool
	connection := &Connection{
		ConnectionID: "connection-1",
		Client:       client,
		Close:        func(string) { closed.Store(true) },
	}
	info := HandshakeInfo{
		Version: 1, Hostname: "workstation.local", OS: "linux", Arch: "amd64",
		ClientVersion: "test", WorkspaceBase: "/workspace", Capabilities: []string{CapabilityFS, CapabilityExec},
	}
	if err := service.ActivateConnection(context.Background(), created.Key, created.ID, info, connection, func() error { return nil }); err != nil {
		t.Fatalf("ActivateConnection() error = %v", err)
	}
	items, err := service.ListRuntimes(context.Background(), "user-1")
	if err != nil || len(items) != 1 {
		t.Fatalf("ListRuntimes() = %#v, %v", items, err)
	}
	if !items[0].Online || items[0].WorkspaceBase != "/workspace" || items[0].Hostname != "workstation.local" {
		t.Fatalf("online Runtime = %#v", items[0])
	}
	if items[0].Key != created.Key {
		t.Fatal("ListRuntimes() did not return the reusable API token")
	}
	if items[0].TeamID != serviceTestTeamID {
		t.Fatalf("ListRuntimes() team ID = %q, want %q", items[0].TeamID, serviceTestTeamID)
	}

	if err := service.RevokeRuntime(context.Background(), "user-1", created.ID); err != nil {
		t.Fatalf("RevokeRuntime() error = %v", err)
	}
	if !closed.Load() {
		t.Fatal("revoke did not close the active reverse-RPC connection")
	}
	if _, online := service.Connection(created.ID); online {
		t.Fatal("revoked Runtime remained online")
	}
	if _, err := service.AuthenticateKey(context.Background(), created.Key); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("AuthenticateKey() after revoke error = %v", err)
	}

	rejected := newServiceTestClient(t)
	defer rejected.Close() //nolint:errcheck // test cleanup
	err = service.ActivateConnection(context.Background(), created.Key, created.ID, info, &Connection{
		ConnectionID: "connection-2", Client: rejected,
	}, func() error { return nil })
	if !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("ActivateConnection() with revoked key error = %v", err)
	}
}

func newServiceTestClient(t *testing.T) *bridge.Client {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///runtime-test", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient() error = %v", err)
	}
	return bridge.NewClientFromConn(conn)
}
