package client

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	runtimeclient "github.com/memohai/memoh/domains/runtime/client"
	runtimesqlc "github.com/memohai/memoh/domains/runtime/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

const (
	testRuntimeID = "11111111-1111-4111-8111-111111111111"
	testUserID    = "22222222-2222-4222-8222-222222222222"
)

type credentialQueriesStub struct {
	create func(context.Context, runtimesqlc.CreateUserRuntimeParams) (runtimesqlc.RuntimeUserRuntime, error)
	find   func(context.Context, string) (runtimesqlc.RuntimeUserRuntime, error)
	list   func(context.Context, pgtype.UUID) ([]runtimesqlc.RuntimeUserRuntime, error)
	revoke func(context.Context, runtimesqlc.RevokeUserRuntimeParams) (runtimesqlc.RuntimeUserRuntime, error)
}

func (s credentialQueriesStub) CreateUserRuntime(ctx context.Context, arg runtimesqlc.CreateUserRuntimeParams) (runtimesqlc.RuntimeUserRuntime, error) {
	return s.create(ctx, arg)
}

func (s credentialQueriesStub) GetUserRuntimeByAPIToken(ctx context.Context, token string) (runtimesqlc.RuntimeUserRuntime, error) {
	return s.find(ctx, token)
}

func (s credentialQueriesStub) ListUserRuntimes(ctx context.Context, id pgtype.UUID) ([]runtimesqlc.RuntimeUserRuntime, error) {
	return s.list(ctx, id)
}

func (s credentialQueriesStub) RevokeUserRuntime(ctx context.Context, arg runtimesqlc.RevokeUserRuntimeParams) (runtimesqlc.RuntimeUserRuntime, error) {
	return s.revoke(ctx, arg)
}

func TestCredentialStoreCreateMapsInputAndRecord(t *testing.T) {
	createdAt := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	queries := credentialQueriesStub{
		create: func(_ context.Context, arg runtimesqlc.CreateUserRuntimeParams) (runtimesqlc.RuntimeUserRuntime, error) {
			if arg.UserID.String() != testUserID || arg.Name != "Workstation" || arg.ApiToken != "mrk_token" {
				t.Fatalf("CreateUserRuntime params = %#v", arg)
			}
			return credentialSQLCRecord(t, createdAt), nil
		},
	}
	store := NewCredentialStoreWithQueries(queries)
	record, err := store.CreateCredential(context.Background(), runtimeclient.CreateCredentialInput{
		UserID: testUserID, Name: "Workstation", APIToken: "mrk_token",
	})
	if err != nil {
		t.Fatalf("CreateCredential() error = %v", err)
	}
	if record.ID != testRuntimeID || record.UserID != testUserID || record.Name != "Workstation" || record.APIToken != "mrk_token" || !record.CreatedAt.Equal(createdAt) {
		t.Fatalf("CreateCredential() = %#v", record)
	}
}

func TestCredentialStoreListAndNotFoundMapping(t *testing.T) {
	queries := credentialQueriesStub{
		find: func(context.Context, string) (runtimesqlc.RuntimeUserRuntime, error) {
			return runtimesqlc.RuntimeUserRuntime{}, pgx.ErrNoRows
		},
		list: func(_ context.Context, id pgtype.UUID) ([]runtimesqlc.RuntimeUserRuntime, error) {
			if id.String() != testUserID {
				t.Fatalf("ListUserRuntimes user = %s", id.String())
			}
			return []runtimesqlc.RuntimeUserRuntime{credentialSQLCRecord(t, time.Time{})}, nil
		},
		revoke: func(_ context.Context, arg runtimesqlc.RevokeUserRuntimeParams) (runtimesqlc.RuntimeUserRuntime, error) {
			if arg.ID.String() != testRuntimeID || arg.UserID.String() != testUserID {
				t.Fatalf("RevokeUserRuntime params = %#v", arg)
			}
			return runtimesqlc.RuntimeUserRuntime{}, pgx.ErrNoRows
		},
	}
	store := NewCredentialStoreWithQueries(queries)
	records, err := store.ListCredentials(context.Background(), testUserID)
	if err != nil || len(records) != 1 || records[0].ID != testRuntimeID {
		t.Fatalf("ListCredentials() = %#v, %v", records, err)
	}
	if _, err := store.FindCredentialByAPIToken(context.Background(), "missing"); !errors.Is(err, runtimeclient.ErrRuntimeNotFound) {
		t.Fatalf("FindCredentialByAPIToken() error = %v, want runtimeclient.ErrRuntimeNotFound", err)
	}
	if err := store.RevokeCredential(context.Background(), testRuntimeID, testUserID); !errors.Is(err, runtimeclient.ErrRuntimeNotFound) {
		t.Fatalf("RevokeCredential() error = %v, want runtimeclient.ErrRuntimeNotFound", err)
	}
}

func TestCredentialStoreMapsDuplicateRuntimeName(t *testing.T) {
	store := NewCredentialStoreWithQueries(credentialQueriesStub{
		create: func(context.Context, runtimesqlc.CreateUserRuntimeParams) (runtimesqlc.RuntimeUserRuntime, error) {
			return runtimesqlc.RuntimeUserRuntime{}, &pgconn.PgError{Code: "23505"}
		},
	})
	_, err := store.CreateCredential(t.Context(), runtimeclient.CreateCredentialInput{
		UserID: testUserID, Name: "Workstation", APIToken: "mrk_token",
	})
	if !errors.Is(err, runtimeclient.ErrRuntimeNameTaken) {
		t.Fatalf("CreateCredential() error = %v, want runtimeclient.ErrRuntimeNameTaken", err)
	}
}

func credentialSQLCRecord(t *testing.T, createdAt time.Time) runtimesqlc.RuntimeUserRuntime {
	t.Helper()
	runtimeID, err := db.ParseUUID(testRuntimeID)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := db.ParseUUID(testUserID)
	if err != nil {
		t.Fatal(err)
	}
	return runtimesqlc.RuntimeUserRuntime{
		ID: runtimeID, UserID: userID, Name: "Workstation", ApiToken: "mrk_token",
		CreatedAt: pgtype.Timestamptz{Time: createdAt, Valid: !createdAt.IsZero()},
	}
}
