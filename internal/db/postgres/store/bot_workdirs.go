package postgresstore

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

func (s *Store) CreateWorkdir(ctx context.Context, input dbstore.CreateBotWorkdirInput) (dbstore.BotWorkdirRecord, error) {
	botID, err := db.ParseUUID(input.BotID)
	if err != nil {
		return dbstore.BotWorkdirRecord{}, err
	}
	row, err := s.queries.CreateBotWorkdir(ctx, dbsqlc.CreateBotWorkdirParams{
		BotID:           botID,
		Name:            input.Name,
		TargetKind:      input.TargetKind,
		RemoteBindingID: db.ParseUUIDOrEmpty(input.RemoteBindingID),
		Path:            input.Path,
		CreatedByUserID: db.ParseUUIDOrEmpty(input.CreatedByUserID),
	})
	if err != nil {
		return dbstore.BotWorkdirRecord{}, mapQueryErr(err)
	}
	return botWorkdirRecord(row), nil
}

func (s *Store) ListWorkdirs(ctx context.Context, botID string, includeArchived bool) ([]dbstore.BotWorkdirRecord, error) {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListBotWorkdirs(ctx, dbsqlc.ListBotWorkdirsParams{
		BotID:           botUUID,
		IncludeArchived: includeArchived,
	})
	if err != nil {
		return nil, err
	}
	items := make([]dbstore.BotWorkdirRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, botWorkdirRecord(row))
	}
	return items, nil
}

func (s *Store) GetWorkdir(ctx context.Context, botID, workdirID string) (dbstore.BotWorkdirRecord, error) {
	botUUID, workdirUUID, err := parseBotWorkdirIDs(botID, workdirID)
	if err != nil {
		return dbstore.BotWorkdirRecord{}, err
	}
	row, err := s.queries.GetBotWorkdir(ctx, dbsqlc.GetBotWorkdirParams{
		BotID:     botUUID,
		WorkdirID: workdirUUID,
	})
	if err != nil {
		return dbstore.BotWorkdirRecord{}, mapQueryErr(err)
	}
	return botWorkdirRecord(row), nil
}

func (s *Store) RenameWorkdir(ctx context.Context, botID, workdirID, name string) (dbstore.BotWorkdirRecord, error) {
	botUUID, workdirUUID, err := parseBotWorkdirIDs(botID, workdirID)
	if err != nil {
		return dbstore.BotWorkdirRecord{}, err
	}
	row, err := s.queries.RenameBotWorkdir(ctx, dbsqlc.RenameBotWorkdirParams{
		BotID:     botUUID,
		WorkdirID: workdirUUID,
		Name:      name,
	})
	if err != nil {
		return dbstore.BotWorkdirRecord{}, mapQueryErr(err)
	}
	return botWorkdirRecord(row), nil
}

func (s *Store) ArchiveWorkdir(ctx context.Context, botID, workdirID string) error {
	botUUID, workdirUUID, err := parseBotWorkdirIDs(botID, workdirID)
	if err != nil {
		return err
	}
	_, err = s.queries.ArchiveBotWorkdir(ctx, dbsqlc.ArchiveBotWorkdirParams{
		BotID:     botUUID,
		WorkdirID: workdirUUID,
	})
	return mapQueryErr(err)
}

func parseBotWorkdirIDs(botID, workdirID string) (botUUID, workdirUUID pgtype.UUID, err error) {
	botUUID, err = db.ParseUUID(botID)
	if err != nil {
		return botUUID, workdirUUID, err
	}
	workdirUUID, err = db.ParseUUID(workdirID)
	return botUUID, workdirUUID, err
}

func botWorkdirRecord(row dbsqlc.BotWorkdir) dbstore.BotWorkdirRecord {
	record := dbstore.BotWorkdirRecord{
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
