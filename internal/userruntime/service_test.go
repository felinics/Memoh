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

	"github.com/memohai/memoh/internal/db"
	dbstore "github.com/memohai/memoh/internal/db/store"
	"github.com/memohai/memoh/internal/workspace/bridge"
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
		APIToken: input.APIToken, CreatedAt: time.Now().UTC(),
	}
	s.revoked = false
	return s.runtime, nil
}

func (s *serviceTestStore) GetUserRuntimeByAPIToken(_ context.Context, token string) (dbstore.UserRuntimeRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked || s.runtime.ID == "" || s.runtime.APIToken != token {
		return dbstore.UserRuntimeRecord{}, db.ErrNotFound
	}
	return s.runtime, nil
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
