package client

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

	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
	"github.com/memohai/memoh/domains/runtime/internal/client/secret"
)

const serviceTestRuntimeID = "11111111-1111-4111-8111-111111111111"

type serviceTestStore struct {
	mu      sync.Mutex
	runtime CredentialRecord
	revoked bool
}

type serviceTestMemberships struct {
	active map[string]bool
	calls  int
}

func (m *serviceTestMemberships) HasActiveTeamMembership(_ context.Context, userID string) (bool, error) {
	m.calls++
	return m.active[userID], nil
}

func (s *serviceTestStore) CreateCredential(_ context.Context, input CreateCredentialInput) (CredentialRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runtime = CredentialRecord{
		ID: serviceTestRuntimeID, UserID: input.UserID, Name: input.Name,
		APIToken: input.APIToken, CreatedAt: time.Now().UTC(),
	}
	s.revoked = false
	return s.runtime, nil
}

func (s *serviceTestStore) FindCredentialByAPIToken(_ context.Context, token string) (CredentialRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked || s.runtime.ID == "" || s.runtime.APIToken != token {
		return CredentialRecord{}, ErrRuntimeNotFound
	}
	return s.runtime, nil
}

func (s *serviceTestStore) ListCredentials(_ context.Context, userID string) ([]CredentialRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked || s.runtime.ID == "" || s.runtime.UserID != userID {
		return []CredentialRecord{}, nil
	}
	return []CredentialRecord{s.runtime}, nil
}

func (s *serviceTestStore) RevokeCredential(_ context.Context, runtimeID, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked || s.runtime.ID != runtimeID || s.runtime.UserID != userID {
		return ErrRuntimeNotFound
	}
	s.revoked = true
	return nil
}

func TestCreateRuntimeCapsNameLength(t *testing.T) {
	service := NewService(
		&serviceTestStore{},
		&serviceTestMemberships{active: map[string]bool{"user-1": true}},
		NewHub(nil),
	)
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

func TestFindCredentialByAPITokenRequiresActiveOwnerMembership(t *testing.T) {
	key, err := secret.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	record := CredentialRecord{
		ID: serviceTestRuntimeID, UserID: "user-1", Name: "Workstation", APIToken: key,
	}

	tests := []struct {
		name        string
		stored      CredentialRecord
		memberships map[string]bool
		wantErr     error
		wantCalls   int
	}{
		{name: "active", stored: record, memberships: map[string]bool{"user-1": true}, wantCalls: 1},
		{name: "inactive", stored: record, memberships: map[string]bool{"user-1": false}, wantErr: ErrRuntimeNotFound, wantCalls: 1},
		{name: "missing", stored: record, memberships: map[string]bool{}, wantErr: ErrRuntimeNotFound, wantCalls: 1},
		{name: "token not found", memberships: map[string]bool{"user-1": true}, wantErr: ErrRuntimeNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			memberships := &serviceTestMemberships{active: tt.memberships}
			service := NewService(&serviceTestStore{runtime: tt.stored}, memberships, NewHub(nil))

			got, err := service.FindCredentialByAPIToken(t.Context(), key)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("FindCredentialByAPIToken() error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && got != record {
				t.Fatalf("FindCredentialByAPIToken() = %#v, want %#v", got, record)
			}
			if memberships.calls != tt.wantCalls {
				t.Fatalf("membership calls = %d, want %d", memberships.calls, tt.wantCalls)
			}
		})
	}
}

func TestServiceRegistrationConnectionAndRevoke(t *testing.T) {
	store := &serviceTestStore{}
	hub := NewHub(nil)
	service := NewService(
		store,
		&serviceTestMemberships{active: map[string]bool{"user-1": true}},
		hub,
	)
	created, err := service.CreateRuntime(context.Background(), "user-1", CreateRuntimeRequest{Name: "Workstation"})
	if err != nil {
		t.Fatalf("CreateRuntime() error = %v", err)
	}
	if err := secret.ValidateKeyFormat(created.Key); err != nil {
		t.Fatalf("created key is invalid: %v", err)
	}
	if created.ID != serviceTestRuntimeID || created.Online {
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

	if err := service.RevokeRuntime(context.Background(), "user-1", created.ID); err != nil {
		t.Fatalf("RevokeRuntime() error = %v", err)
	}
	if !closed.Load() {
		t.Fatal("revoke did not close the active reverse-RPC connection")
	}
	if _, online := service.Connection(created.ID); online {
		t.Fatal("revoked Runtime remained online")
	}
	if _, err := service.AuthenticateKey(context.Background(), created.Key); !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("AuthenticateKey() after revoke error = %v", err)
	}

	rejected := newServiceTestClient(t)
	defer rejected.Close() //nolint:errcheck // test cleanup
	err = service.ActivateConnection(context.Background(), created.Key, created.ID, info, &Connection{
		ConnectionID: "connection-2", Client: rejected,
	}, func() error { return nil })
	if !errors.Is(err, ErrRuntimeNotFound) {
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
