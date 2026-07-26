package gateway

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/channel/identity"
	"github.com/memohai/memoh/domains/channel/route"
)

func TestPersistenceErrorMapping(t *testing.T) {
	t.Run("config", func(t *testing.T) {
		if !errors.Is(mapConfigError(pgx.ErrNoRows), gateway.ErrChannelConfigNotFound) {
			t.Fatal("expected config not-found mapping")
		}
	})
	t.Run("identity", func(t *testing.T) {
		if !errors.Is(mapIdentityError(pgx.ErrNoRows), identity.ErrChannelIdentityNotFound) {
			t.Fatal("expected identity not-found mapping")
		}
	})
	t.Run("route", func(t *testing.T) {
		if !errors.Is(mapRouteError(pgx.ErrNoRows), route.ErrNotFound) {
			t.Fatal("expected route not-found mapping")
		}
	})
}

type activeThreadCoordinatorFake struct {
	sessionID string
}

func (*activeThreadCoordinatorFake) WithLockedRouteSessions(context.Context, string, func(pgx.Tx) error) error {
	return nil
}

func (f *activeThreadCoordinatorFake) WithLockedSession(_ context.Context, sessionID string, _ func(pgx.Tx) error) error {
	f.sessionID = sessionID
	return nil
}

func TestSetActiveThreadCoordinatesSessionLock(t *testing.T) {
	coordinator := &activeThreadCoordinatorFake{}
	store := &Store{routeSessions: coordinator}

	err := store.SetActiveThread(
		t.Context(),
		"00000000-0000-0000-0000-000000000001",
		"00000000-0000-0000-0000-000000000002",
	)
	if err != nil {
		t.Fatalf("SetActiveThread() error = %v", err)
	}
	if coordinator.sessionID != "00000000-0000-0000-0000-000000000002" {
		t.Fatalf("session id = %q", coordinator.sessionID)
	}
}
