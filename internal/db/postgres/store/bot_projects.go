package postgresstore

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/internal/db"
	dbsqlc "github.com/memohai/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/memohai/memoh/internal/db/store"
)

func (s *Store) CreateProject(ctx context.Context, input dbstore.CreateBotProjectInput) (dbstore.BotProjectRecord, error) {
	botID, err := db.ParseUUID(input.BotID)
	if err != nil {
		return dbstore.BotProjectRecord{}, err
	}
	row, err := s.queries.CreateBotProject(ctx, dbsqlc.CreateBotProjectParams{
		BotID:           botID,
		Name:            input.Name,
		TargetKind:      input.TargetKind,
		RemoteBindingID: db.ParseUUIDOrEmpty(input.RemoteBindingID),
		Path:            input.Path,
		CreatedByUserID: db.ParseUUIDOrEmpty(input.CreatedByUserID),
	})
	if err != nil {
		return dbstore.BotProjectRecord{}, mapQueryErr(err)
	}
	return botProjectRecord(row), nil
}

func (s *Store) ListProjects(ctx context.Context, botID string, includeArchived bool) ([]dbstore.BotProjectRecord, error) {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListBotProjects(ctx, dbsqlc.ListBotProjectsParams{
		BotID:           botUUID,
		IncludeArchived: includeArchived,
	})
	if err != nil {
		return nil, err
	}
	items := make([]dbstore.BotProjectRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, botProjectRecord(row))
	}
	return items, nil
}

func (s *Store) GetProject(ctx context.Context, botID, projectID string) (dbstore.BotProjectRecord, error) {
	botUUID, projectUUID, err := parseBotProjectIDs(botID, projectID)
	if err != nil {
		return dbstore.BotProjectRecord{}, err
	}
	row, err := s.queries.GetBotProject(ctx, dbsqlc.GetBotProjectParams{
		BotID:     botUUID,
		ProjectID: projectUUID,
	})
	if err != nil {
		return dbstore.BotProjectRecord{}, mapQueryErr(err)
	}
	return botProjectRecord(row), nil
}

func (s *Store) RenameProject(ctx context.Context, botID, projectID, name string) (dbstore.BotProjectRecord, error) {
	botUUID, projectUUID, err := parseBotProjectIDs(botID, projectID)
	if err != nil {
		return dbstore.BotProjectRecord{}, err
	}
	row, err := s.queries.RenameBotProject(ctx, dbsqlc.RenameBotProjectParams{
		BotID:     botUUID,
		ProjectID: projectUUID,
		Name:      name,
	})
	if err != nil {
		return dbstore.BotProjectRecord{}, mapQueryErr(err)
	}
	return botProjectRecord(row), nil
}

func (s *Store) ArchiveProject(ctx context.Context, botID, projectID string) error {
	botUUID, projectUUID, err := parseBotProjectIDs(botID, projectID)
	if err != nil {
		return err
	}
	_, err = s.queries.ArchiveBotProject(ctx, dbsqlc.ArchiveBotProjectParams{
		BotID:     botUUID,
		ProjectID: projectUUID,
	})
	return mapQueryErr(err)
}

func parseBotProjectIDs(botID, projectID string) (botUUID, projectUUID pgtype.UUID, err error) {
	botUUID, err = db.ParseUUID(botID)
	if err != nil {
		return botUUID, projectUUID, err
	}
	projectUUID, err = db.ParseUUID(projectID)
	return botUUID, projectUUID, err
}

func botProjectRecord(row dbsqlc.BotProject) dbstore.BotProjectRecord {
	record := dbstore.BotProjectRecord{
		ID:         row.ID.String(),
		BotID:      row.BotID.String(),
		Name:       row.Name,
		TargetKind: row.TargetKind,
		Path:       row.Path,
		ArchivedAt: db.TimeFromPg(row.ArchivedAt),
		CreatedAt:  db.TimeFromPg(row.CreatedAt),
		UpdatedAt:  db.TimeFromPg(row.UpdatedAt),
	}
	if row.RemoteBindingID.Valid {
		record.RemoteBindingID = row.RemoteBindingID.String()
	}
	if row.CreatedByUserID.Valid {
		record.CreatedByUserID = row.CreatedByUserID.String()
	}
	return record
}
