package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/domains/runtime/internal/postgres/sqlc"
	"github.com/memohai/memoh/domains/runtime/network"
)

const networkBotID = "11111111-1111-4111-8111-111111111111"

type queriesStub struct {
	container func(context.Context, pgtype.UUID) (sqlc.RuntimeContainer, error)
}

func (s queriesStub) GetContainerByBotID(ctx context.Context, id pgtype.UUID) (sqlc.RuntimeContainer, error) {
	return s.container(ctx, id)
}

func TestStoreMapsWorkspaceContainer(t *testing.T) {
	t.Parallel()

	queries := queriesStub{
		container: func(_ context.Context, id pgtype.UUID) (sqlc.RuntimeContainer, error) {
			if id.String() != networkBotID {
				t.Fatalf("GetContainerByBotID id = %s", id.String())
			}
			return sqlc.RuntimeContainer{ContainerID: "workspace-1"}, nil
		},
	}
	store := NewStoreWithQueries(queries)

	container, err := store.GetWorkspaceContainer(t.Context(), networkBotID)
	if err != nil {
		t.Fatalf("GetWorkspaceContainer() error = %v", err)
	}
	if container.ContainerID != "workspace-1" {
		t.Fatalf("GetWorkspaceContainer() = %#v", container)
	}
}

func TestStoreMapsMissingWorkspaceAndRejectsInvalidBotID(t *testing.T) {
	t.Parallel()

	store := NewStoreWithQueries(queriesStub{
		container: func(context.Context, pgtype.UUID) (sqlc.RuntimeContainer, error) {
			return sqlc.RuntimeContainer{}, pgx.ErrNoRows
		},
	})
	if _, err := store.GetWorkspaceContainer(t.Context(), networkBotID); !errors.Is(err, network.ErrWorkspaceContainerMissing) {
		t.Fatalf("GetWorkspaceContainer() error = %v, want ErrWorkspaceContainerMissing", err)
	}
	if _, err := store.GetWorkspaceContainer(t.Context(), "not-a-uuid"); err == nil {
		t.Fatal("GetWorkspaceContainer() error = nil, want invalid UUID")
	}
}
