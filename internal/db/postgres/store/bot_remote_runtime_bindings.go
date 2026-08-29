package postgresstore

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

func (s *Store) CreateOrUpdateMount(ctx context.Context, botID, runtimeID string) (dbstore.BotRemoteRuntimeBindingRecord, error) {
	parsedBotID, err := db.ParseUUID(botID)
	if err != nil {
		return dbstore.BotRemoteRuntimeBindingRecord{}, err
	}
	parsedRuntimeID, err := db.ParseUUID(runtimeID)
	if err != nil {
		return dbstore.BotRemoteRuntimeBindingRecord{}, err
	}
	targetID, err := s.queries.CreateOrUpdateBotRemoteRuntimeMount(ctx, dbsqlc.CreateOrUpdateBotRemoteRuntimeMountParams{
		BotID: parsedBotID, RuntimeID: parsedRuntimeID,
	})
	if err != nil {
		return dbstore.BotRemoteRuntimeBindingRecord{}, mapQueryErr(err)
	}
	return s.GetMount(ctx, botID, targetID.String())
}

func (s *Store) ListMounts(ctx context.Context, botID string) ([]dbstore.BotRemoteRuntimeBindingRecord, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListBotRemoteRuntimeMounts(ctx, id)
	if err != nil {
		return nil, mapQueryErr(err)
	}
	records := make([]dbstore.BotRemoteRuntimeBindingRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, remoteMountRecord(
			row.ID, row.BotID, row.RuntimeID, row.IsPrimary, row.ToolApprovalConfig,
			row.RuntimeName, row.RuntimeUserID, row.RuntimeUnavailable, row.BotOwnerUserID,
			row.CreatedAt, row.UpdatedAt,
		))
	}
	return records, nil
}

func (s *Store) ListGrantsByRuntimeOwner(ctx context.Context, ownerUserID string) ([]dbstore.BotRemoteRuntimeBindingRecord, error) {
	id, err := db.ParseUUID(ownerUserID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListBotRemoteRuntimeGrantsByRuntimeOwner(ctx, id)
	if err != nil {
		return nil, mapQueryErr(err)
	}
	records := make([]dbstore.BotRemoteRuntimeBindingRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, dbstore.BotRemoteRuntimeBindingRecord{
			ID:        row.ID.String(),
			BotID:     row.BotID.String(),
			RuntimeID: row.RuntimeID.String(),
			IsPrimary: row.IsPrimary,
		})
	}
	return records, nil
}

func (s *Store) GetMount(ctx context.Context, botID, targetID string) (dbstore.BotRemoteRuntimeBindingRecord, error) {
	botUUID, targetUUID, err := parseRemoteMountIDs(botID, targetID)
	if err != nil {
		return dbstore.BotRemoteRuntimeBindingRecord{}, err
	}
	row, err := s.queries.GetBotRemoteRuntimeMount(ctx, dbsqlc.GetBotRemoteRuntimeMountParams{BotID: botUUID, TargetID: targetUUID})
	if err != nil {
		return dbstore.BotRemoteRuntimeBindingRecord{}, mapQueryErr(err)
	}
	return remoteMountRecord(
		row.ID, row.BotID, row.RuntimeID, row.IsPrimary, row.ToolApprovalConfig,
		row.RuntimeName, row.RuntimeUserID, row.RuntimeUnavailable, row.BotOwnerUserID,
		row.CreatedAt, row.UpdatedAt,
	), nil
}

func (s *Store) GetPrimaryMount(ctx context.Context, botID string) (dbstore.BotRemoteRuntimeBindingRecord, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return dbstore.BotRemoteRuntimeBindingRecord{}, err
	}
	row, err := s.queries.GetPrimaryBotRemoteRuntimeMount(ctx, id)
	if err != nil {
		return dbstore.BotRemoteRuntimeBindingRecord{}, mapQueryErr(err)
	}
	return remoteMountRecord(
		row.ID, row.BotID, row.RuntimeID, row.IsPrimary, row.ToolApprovalConfig,
		row.RuntimeName, row.RuntimeUserID, row.RuntimeUnavailable, row.BotOwnerUserID,
		row.CreatedAt, row.UpdatedAt,
	), nil
}

func (s *Store) SetPrimary(ctx context.Context, botID, targetID string) error {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(targetID) == "" || strings.EqualFold(strings.TrimSpace(targetID), "native") {
		return mapQueryErr(s.queries.ClearBotRemoteRuntimePrimary(ctx, botUUID))
	}
	targetUUID, err := db.ParseUUID(targetID)
	if err != nil {
		return err
	}
	setPrimary := func(queries *dbsqlc.Queries) error {
		if err := queries.ClearBotRemoteRuntimePrimary(ctx, botUUID); err != nil {
			return mapQueryErr(err)
		}
		rows, err := queries.SetBotRemoteRuntimePrimary(ctx, dbsqlc.SetBotRemoteRuntimePrimaryParams{
			BotID: botUUID, TargetID: targetUUID,
		})
		if err != nil {
			return mapQueryErr(err)
		}
		if rows == 0 {
			return db.ErrNotFound
		}
		return nil
	}
	if s.pool == nil {
		// NewWithQueries is used only by focused store tests. Production stores
		// always carry a pool and use the transaction below.
		if _, err := s.GetMount(ctx, botID, targetID); err != nil {
			return err
		}
		return setPrimary(s.queries)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := setPrimary(s.queries.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) UpdateToolApproval(ctx context.Context, botID, targetID string, config dbstore.JSON) error {
	botUUID, targetUUID, err := parseRemoteMountIDs(botID, targetID)
	if err != nil {
		return err
	}
	_, err = s.queries.UpdateBotRemoteRuntimeMountToolApproval(ctx, dbsqlc.UpdateBotRemoteRuntimeMountToolApprovalParams{
		BotID: botUUID, TargetID: targetUUID, ToolApprovalConfig: config,
	})
	return mapQueryErr(err)
}

func (s *Store) DeleteMount(ctx context.Context, botID, targetID string) error {
	botUUID, targetUUID, err := parseRemoteMountIDs(botID, targetID)
	if err != nil {
		return err
	}
	_, err = s.queries.DeleteBotRemoteRuntimeMount(ctx, dbsqlc.DeleteBotRemoteRuntimeMountParams{
		BotID: botUUID, TargetID: targetUUID,
	})
	return mapDeleteRemoteMountErr(err)
}

func mapDeleteRemoteMountErr(err error) error {
	var pgErr *pgconn.PgError
	// PostgreSQL 18 reports an ON DELETE RESTRICT failure as
	// restrict_violation (23001); older releases used
	// foreign_key_violation (23503). Match both so self-hosted installs on an
	// older PostgreSQL still get the mapped 409 instead of a generic 500.
	if errors.As(err, &pgErr) &&
		(pgErr.Code == "23001" || pgErr.Code == "23503") &&
		pgErr.ConstraintName == "bot_workdirs_remote_binding_fkey" {
		return db.ErrWorkspaceTargetInUse
	}
	return mapQueryErr(err)
}

func parseRemoteMountIDs(botID, targetID string) (pgtype.UUID, pgtype.UUID, error) {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	targetUUID, err := db.ParseUUID(targetID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	return botUUID, targetUUID, nil
}

func remoteMountRecord(
	id, botID, runtimeID pgtype.UUID,
	isPrimary bool,
	toolApproval []byte,
	runtimeName string,
	runtimeUserID pgtype.UUID,
	runtimeUnavailable pgtype.Bool,
	botOwnerUserID pgtype.UUID,
	createdAt, updatedAt pgtype.Timestamptz,
) dbstore.BotRemoteRuntimeBindingRecord {
	return dbstore.BotRemoteRuntimeBindingRecord{
		ID:             id.String(),
		BotID:          botID.String(),
		RuntimeID:      runtimeID.String(),
		IsPrimary:      isPrimary,
		ToolApproval:   append(dbstore.JSON(nil), toolApproval...),
		RuntimeName:    runtimeName,
		RuntimeUserID:  runtimeUserID.String(),
		BotOwnerUserID: botOwnerUserID.String(),
		RuntimeRevoked: runtimeUnavailable.Bool,
		CreatedAt:      db.TimeFromPg(createdAt),
		UpdatedAt:      db.TimeFromPg(updatedAt),
	}
}
