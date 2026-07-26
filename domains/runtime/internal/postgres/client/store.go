// Package client implements reverse user-runtime credential persistence.
package client

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	runtimeclient "github.com/memohai/memoh/domains/runtime/client"
	runtimesqlc "github.com/memohai/memoh/domains/runtime/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type queries interface {
	CreateUserRuntime(context.Context, runtimesqlc.CreateUserRuntimeParams) (runtimesqlc.RuntimeUserRuntime, error)
	GetUserRuntimeByAPIToken(context.Context, string) (runtimesqlc.RuntimeUserRuntime, error)
	ListUserRuntimes(context.Context, pgtype.UUID) ([]runtimesqlc.RuntimeUserRuntime, error)
	RevokeUserRuntime(context.Context, runtimesqlc.RevokeUserRuntimeParams) (runtimesqlc.RuntimeUserRuntime, error)
}

// CredentialStore adapts generated Runtime statements to the public credential port.
type CredentialStore struct {
	queries queries
}

var _ runtimeclient.CredentialStore = (*CredentialStore)(nil)

// NewCredentialStore creates a postgres-backed credential store from a pool.
func NewCredentialStore(pool *pgxpool.Pool) *CredentialStore {
	if pool == nil {
		return nil
	}
	return &CredentialStore{queries: runtimesqlc.New(pool)}
}

// NewCredentialStoreWithQueries injects a query surface for tests.
func NewCredentialStoreWithQueries(queries queries) *CredentialStore {
	return &CredentialStore{queries: queries}
}

func (s *CredentialStore) CreateCredential(ctx context.Context, input runtimeclient.CreateCredentialInput) (runtimeclient.CredentialRecord, error) {
	userID, err := db.ParseUUID(input.UserID)
	if err != nil {
		return runtimeclient.CredentialRecord{}, err
	}
	row, err := s.queries.CreateUserRuntime(ctx, runtimesqlc.CreateUserRuntimeParams{
		UserID: userID, Name: input.Name, ApiToken: input.APIToken,
	})
	if err != nil {
		return runtimeclient.CredentialRecord{}, mapQueryError(err)
	}
	return credentialRecord(row), nil
}

func (s *CredentialStore) FindCredentialByAPIToken(ctx context.Context, apiToken string) (runtimeclient.CredentialRecord, error) {
	row, err := s.queries.GetUserRuntimeByAPIToken(ctx, apiToken)
	if err != nil {
		return runtimeclient.CredentialRecord{}, mapQueryError(err)
	}
	return credentialRecord(row), nil
}

func (s *CredentialStore) ListCredentials(ctx context.Context, userID string) ([]runtimeclient.CredentialRecord, error) {
	id, err := db.ParseUUID(userID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListUserRuntimes(ctx, id)
	if err != nil {
		return nil, err
	}
	items := make([]runtimeclient.CredentialRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, credentialRecord(row))
	}
	return items, nil
}

func (s *CredentialStore) RevokeCredential(ctx context.Context, runtimeID, userID string) error {
	id, err := db.ParseUUID(runtimeID)
	if err != nil {
		return err
	}
	ownerID, err := db.ParseUUID(userID)
	if err != nil {
		return err
	}
	_, err = s.queries.RevokeUserRuntime(ctx, runtimesqlc.RevokeUserRuntimeParams{ID: id, UserID: ownerID})
	return mapQueryError(err)
}

func credentialRecord(row runtimesqlc.RuntimeUserRuntime) runtimeclient.CredentialRecord {
	return runtimeclient.CredentialRecord{
		ID: row.ID.String(), UserID: row.UserID.String(), Name: row.Name,
		APIToken: row.ApiToken, CreatedAt: db.TimeFromPg(row.CreatedAt),
	}
}

func mapQueryError(err error) error {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return runtimeclient.ErrRuntimeNotFound
	case db.IsUniqueViolation(err):
		return runtimeclient.ErrRuntimeNameTaken
	}
	return err
}
