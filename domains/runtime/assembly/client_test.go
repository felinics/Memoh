package assembly

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

type clientMembershipStub struct{}

func (clientMembershipStub) HasActiveTeamMembership(context.Context, string) (bool, error) {
	return true, nil
}

func TestNewClientRequiresPool(t *testing.T) {
	if _, _, err := NewClient(ClientDeps{
		Log: slog.Default(), Membership: clientMembershipStub{},
	}); err == nil {
		t.Fatal("expected missing pool error")
	}
}

func TestNewClientRequiresMembershipReader(t *testing.T) {
	if _, _, err := NewClient(ClientDeps{
		Log: slog.Default(), Pool: &pgxpool.Pool{},
	}); err == nil {
		t.Fatal("expected missing membership reader error")
	}
}
