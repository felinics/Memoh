package postgresstore

import (
	"context"

	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
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

func (s *Store) ActivateUserRuntime(ctx context.Context, runtimeID, apiToken string) (dbstore.UserRuntimeRecord, error) {
	id, err := db.ParseUUID(runtimeID)
	if err != nil {
		return dbstore.UserRuntimeRecord{}, err
	}
	row, err := s.queries.ActivateUserRuntime(ctx, dbsqlc.ActivateUserRuntimeParams{ID: id, ApiToken: apiToken})
	if err != nil {
		return dbstore.UserRuntimeRecord{}, mapQueryErr(err)
	}
	return userRuntimeRecord(row), nil
}

func (s *Store) ExpirePendingUserRuntimes(ctx context.Context, userID string) error {
	id, err := db.ParseUUID(userID)
	if err != nil {
		return err
	}
	return mapQueryErr(s.queries.ExpirePendingUserRuntimes(ctx, id))
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
	revoke := func(queries *dbsqlc.Queries) error {
		if _, err := queries.RevokeUserRuntime(ctx, dbsqlc.RevokeUserRuntimeParams{ID: id, UserID: ownerID}); err != nil {
			return mapQueryErr(err)
		}
		// Cascade: dead mounts of the revoked runtime must not linger as
		// ghost rows on the bot page / grants view / composer selector.
		return mapQueryErr(queries.DeleteBotRemoteRuntimeMountsByRuntime(ctx, id))
	}
	if s.pool == nil {
		// NewWithQueries is used only by focused store tests. Production stores
		// always carry a pool and use the transaction below.
		return revoke(s.queries)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := revoke(s.queries.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) BackfillUserRuntimeName(ctx context.Context, runtimeID, userID, name, defaultName string) (bool, error) {
	id, err := db.ParseUUID(runtimeID)
	if err != nil {
		return false, err
	}
	ownerID, err := db.ParseUUID(userID)
	if err != nil {
		return false, err
	}
	rows, err := s.queries.BackfillUserRuntimeName(ctx, dbsqlc.BackfillUserRuntimeNameParams{
		ID: id, UserID: ownerID, Name: name, DefaultName: defaultName,
	})
	if err != nil {
		return false, mapQueryErr(err)
	}
	return rows > 0, nil
}

func userRuntimeRecord(row dbsqlc.UserRuntime) dbstore.UserRuntimeRecord {
	return dbstore.UserRuntimeRecord{
		ID: row.ID.String(), TeamID: row.TeamID.String(), UserID: row.UserID.String(), Name: row.Name,
		APIToken: row.ApiToken, ActivatedAt: db.TimeFromPg(row.ActivatedAt),
		PendingExpiresAt: db.TimeFromPg(row.PendingExpiresAt), CreatedAt: db.TimeFromPg(row.CreatedAt),
	}
}
