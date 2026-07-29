package postgresstore

import (
	"context"

	"github.com/memohai/memoh/internal/db"
	dbsqlc "github.com/memohai/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/memohai/memoh/internal/db/store"
)

func (s *Store) CreateUserRuntime(ctx context.Context, input dbstore.CreateUserRuntimeInput) (dbstore.UserRuntimeRecord, error) {
	userID, err := db.ParseUUID(input.UserID)
	if err != nil {
		return dbstore.UserRuntimeRecord{}, err
	}
	row, err := s.queries.CreateUserRuntime(ctx, dbsqlc.CreateUserRuntimeParams{
		UserID: userID, Name: input.Name, ApiToken: input.APIToken,
	})
	if err != nil {
		return dbstore.UserRuntimeRecord{}, err
	}
	return userRuntimeRecord(row), nil
}

func (s *Store) GetUserRuntimeByAPIToken(ctx context.Context, apiToken string) (dbstore.UserRuntimeRecord, error) {
	row, err := s.queries.GetUserRuntimeByAPIToken(ctx, apiToken)
	if err != nil {
		return dbstore.UserRuntimeRecord{}, mapQueryErr(err)
	}
	return userRuntimeRecord(row), nil
}

func (s *Store) ListUserRuntimes(ctx context.Context, userID string) ([]dbstore.UserRuntimeRecord, error) {
	id, err := db.ParseUUID(userID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListUserRuntimes(ctx, id)
	if err != nil {
		return nil, err
	}
	items := make([]dbstore.UserRuntimeRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, userRuntimeRecord(row))
	}
	return items, nil
}

func (s *Store) RevokeUserRuntime(ctx context.Context, runtimeID, userID string) error {
	id, err := db.ParseUUID(runtimeID)
	if err != nil {
		return err
	}
	ownerID, err := db.ParseUUID(userID)
	if err != nil {
		return err
	}
	_, err = s.queries.RevokeUserRuntime(ctx, dbsqlc.RevokeUserRuntimeParams{ID: id, UserID: ownerID})
	return mapQueryErr(err)
}

func userRuntimeRecord(row dbsqlc.UserRuntime) dbstore.UserRuntimeRecord {
	return dbstore.UserRuntimeRecord{
		ID: row.ID.String(), TeamID: row.TeamID.String(), UserID: row.UserID.String(), Name: row.Name,
		APIToken: row.ApiToken, CreatedAt: db.TimeFromPg(row.CreatedAt),
	}
}
