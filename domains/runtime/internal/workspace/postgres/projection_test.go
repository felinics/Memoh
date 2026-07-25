package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbsqlc "github.com/memohai/memoh/domains/runtime/internal/postgres/sqlc"
)

type botProjectionQueriesStub struct {
	row dbsqlc.RuntimeContainer
	err error
}

func (s botProjectionQueriesStub) GetContainerByBotID(context.Context, pgtype.UUID) (dbsqlc.RuntimeContainer, error) {
	return s.row, s.err
}

func TestBotProjectionStoreFind(t *testing.T) {
	store := NewBotProjectionStore(botProjectionQueriesStub{row: dbsqlc.RuntimeContainer{
		ContainerID: "runtime-1",
		Namespace:   "bots",
		Image:       "memoh:latest",
		Status:      "running",
	}})

	got, found, err := store.Find(t.Context(), "11111111-1111-4111-8111-111111111111")
	if err != nil || !found {
		t.Fatalf("Find() = %#v, %v, %v", got, found, err)
	}
	if got.ContainerID != "runtime-1" || got.Namespace != "bots" || got.Image != "memoh:latest" || got.Status != "running" {
		t.Fatalf("Find() = %#v", got)
	}
}

func TestBotProjectionStoreFindMissing(t *testing.T) {
	store := NewBotProjectionStore(botProjectionQueriesStub{err: pgx.ErrNoRows})
	_, found, err := store.Find(t.Context(), "11111111-1111-4111-8111-111111111111")
	if err != nil || found {
		t.Fatalf("Find() found=%v err=%v", found, err)
	}
}

func TestBotProjectionStoreFindPropagatesError(t *testing.T) {
	want := errors.New("query failed")
	store := NewBotProjectionStore(botProjectionQueriesStub{err: want})
	_, _, err := store.Find(t.Context(), "11111111-1111-4111-8111-111111111111")
	if !errors.Is(err, want) {
		t.Fatalf("Find() error = %v, want %v", err, want)
	}
}
