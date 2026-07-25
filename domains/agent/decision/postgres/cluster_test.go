package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/domains/agent/chat/runtimefence"
	"github.com/memohai/memoh/domains/agent/decision/approval"
	"github.com/memohai/memoh/domains/agent/decision/input"
)

func TestNewRequiresDeps(t *testing.T) {
	if _, err := New(nil, nil, nil); err == nil {
		t.Fatal("New(nil, nil, nil) succeeded")
	}
	lock := BotSessionWriteLockerFromTx(func(pgx.Tx) BotSessionWriteLocker { return stubLocker{} })
	identities := stubIdentities{}
	if _, err := New(nil, lock, identities); err == nil {
		t.Fatal("New(nil, lock, identities) succeeded")
	}
}

func TestNoRowsMapping(t *testing.T) {
	if !errors.Is(mapApprovalError(pgx.ErrNoRows), approval.ErrNotFound) {
		t.Fatal("approval no-row error was not mapped")
	}
	if !errors.Is(mapInputError(pgx.ErrNoRows), input.ErrNotFound) {
		t.Fatal("input no-row error was not mapped")
	}
	if !errors.Is(mapFenceError(pgx.ErrNoRows), runtimefence.ErrRecordNotFound) {
		t.Fatal("fence no-row error was not mapped")
	}
}

func TestChannelIdentityExistsUsesReader(t *testing.T) {
	exists, err := channelIdentityExists(context.Background(), stubIdentities{ids: map[string]bool{"11111111-1111-1111-1111-111111111111": true}}, "11111111-1111-1111-1111-111111111111")
	if err != nil || !exists {
		t.Fatalf("exists = %v, %v", exists, err)
	}
	exists, err = channelIdentityExists(context.Background(), stubIdentities{}, "11111111-1111-1111-1111-111111111111")
	if err != nil || exists {
		t.Fatalf("missing identity = %v, %v", exists, err)
	}
}

type stubLocker struct{}

func (stubLocker) LockBotForSessionWrite(context.Context, pgtype.UUID) (pgtype.UUID, error) {
	return pgtype.UUID{}, nil
}

type stubIdentities struct {
	ids map[string]bool
}

func (s stubIdentities) GetByID(_ context.Context, id string) (ChannelIdentity, error) {
	if s.ids[id] {
		return ChannelIdentity{ID: id}, nil
	}
	return ChannelIdentity{}, errors.New("not found")
}
