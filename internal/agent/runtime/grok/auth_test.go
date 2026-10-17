package grok

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agentcredential"
)

// fakeLoginStore holds the one credential an Agent currently has, with the
// credential service's version-guarded update.
type fakeLoginStore struct {
	current    agentcredential.ResolvedCredential
	resolveErr error
	writes     int
}

func (s *fakeLoginStore) ResolveForBotAgent(context.Context, string, string) (agentcredential.ResolvedCredential, error) {
	return s.current, s.resolveErr
}

func (s *fakeLoginStore) UpdateSecretCAS(_ context.Context, id string, version int64, secret map[string]string, _ map[string]any, _ *time.Time) (agentcredential.PublicCredential, error) {
	if id != s.current.ID || version != s.current.CredentialVersion {
		return agentcredential.PublicCredential{}, agentcredential.ErrNotFound
	}
	s.writes++
	s.current.CredentialVersion++
	s.current.Secret = secret
	return s.current.PublicCredential, nil
}

func storedLogin(id string, version int64, authJSON string) agentcredential.ResolvedCredential {
	return agentcredential.ResolvedCredential{
		PublicCredential: agentcredential.PublicCredential{ID: id, CredentialVersion: version},
		Secret:           map[string]string{"auth_json": authJSON},
	}
}

func newLoginSync(store *fakeLoginStore, file *string) *loginSync {
	return &loginSync{
		store: store, logger: slog.Default(), stored: store.current,
		read: func(context.Context) (string, error) { return *file, nil },
	}
}

func TestLoginSyncStoresWhatGrokRefreshed(t *testing.T) {
	store := &fakeLoginStore{current: storedLogin("cred-1", 1, `{"a":"L1"}`)}
	file := `{"a":"L1"}`
	sync := newLoginSync(store, &file)
	ctx := context.Background()

	sync.sync(ctx)
	if store.writes != 0 {
		t.Fatal("an unchanged login must not be rewritten")
	}
	file = `{"a":"L1-refreshed"}`
	sync.sync(ctx)
	sync.sync(ctx)
	if store.writes != 1 || store.current.Secret["auth_json"] != file || store.current.CredentialVersion != 2 {
		t.Fatalf("store after a refresh = %+v with %d writes, want the refreshed login stored once", store.current, store.writes)
	}

	// A displaced process sharing the home stored the next refresh first.
	file = `{"a":"L1-refreshed-again"}`
	store.current = storedLogin("cred-1", 3, file)
	sync.sync(ctx)
	if store.writes != 1 {
		t.Fatal("a refresh the sibling already stored must not be written again")
	}
	file = `{"a":"L1-refreshed-third"}`
	sync.sync(ctx)
	if store.writes != 2 || store.current.Secret["auth_json"] != file {
		t.Fatalf("store = %+v with %d writes, want this process to keep following the credential", store.current, store.writes)
	}
}

func TestLoginSyncStopsOnlyForAReplacedCredentialOrAnEndedProcess(t *testing.T) {
	ctx := context.Background()

	// The user signed in again while this process ran: its file is the older
	// login.
	store := &fakeLoginStore{current: storedLogin("cred-1", 1, `{"a":"L1"}`)}
	file := `{"a":"L1-refreshed"}`
	sync := newLoginSync(store, &file)
	store.current = storedLogin("cred-2", 1, `{"a":"L2"}`)
	sync.sync(ctx)
	if store.writes != 0 || store.current.Secret["auth_json"] != `{"a":"L2"}` {
		t.Fatalf("store = %+v with %d writes, want the new sign-in untouched", store.current, store.writes)
	}

	store = &fakeLoginStore{current: storedLogin("cred-1", 2, `{"a":"L1"}`)}
	file = `{"a":"L1-refreshed"}`
	sync = newLoginSync(store, &file)
	sync.stored.CredentialVersion = 1

	// A lookup that failed says nothing about the credential.
	store.resolveErr = errors.New("database unavailable")
	sync.sync(ctx)
	store.resolveErr = nil
	sync.sync(ctx)
	if store.writes != 1 {
		t.Fatalf("writes = %d, want the refresh stored once the credential could be read again", store.writes)
	}

	// The process ended, and the file now holds what its successor was given.
	sync.finish()
	file = `{"a":"L2"}`
	sync.sync(ctx)
	if store.writes != 1 || store.current.Secret["auth_json"] != `{"a":"L1-refreshed"}` {
		t.Fatalf("store = %+v with %d writes, want nothing stored after the process ended", store.current, store.writes)
	}
}

func TestLoginSyncNeverStoresAnOlderRefreshOverASiblings(t *testing.T) {
	store := &fakeLoginStore{current: storedLogin("cred-1", 1, `{"a":"L1"}`)}
	file := `{"a":"L1-refreshed"}`
	sync := newLoginSync(store, &file)
	// Right after this process reads the file, Grok refreshes again and a
	// sibling stores that later login.
	sync.read = func(context.Context) (string, error) {
		read := file
		if store.current.CredentialVersion == 1 {
			file = `{"a":"L1-refreshed-again"}`
			store.current = storedLogin("cred-1", 2, file)
		}
		return read, nil
	}

	sync.sync(context.Background())
	if store.writes != 0 || store.current.Secret["auth_json"] != file {
		t.Fatalf("store = %+v with %d writes, want the sibling's later refresh kept", store.current, store.writes)
	}
}
