// Package postgres implements Workspace-owned PostgreSQL persistence.
package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	runtimesqlc "github.com/memohai/memoh/domains/runtime/internal/postgres/sqlc"
	runtimeworkspace "github.com/memohai/memoh/domains/runtime/workspace"
	"github.com/memohai/memoh/internal/db"
)

type mountQueries interface {
	ClearBotRemoteRuntimePrimary(context.Context, pgtype.UUID) error
	CreateOrUpdateBotRemoteRuntimeMount(context.Context, runtimesqlc.CreateOrUpdateBotRemoteRuntimeMountParams) (pgtype.UUID, error)
	DeleteBotRemoteRuntimeMount(context.Context, runtimesqlc.DeleteBotRemoteRuntimeMountParams) (pgtype.UUID, error)
	GetBotRemoteRuntimeMount(context.Context, runtimesqlc.GetBotRemoteRuntimeMountParams) (runtimesqlc.GetBotRemoteRuntimeMountRow, error)
	GetPrimaryBotRemoteRuntimeMount(context.Context, pgtype.UUID) (runtimesqlc.GetPrimaryBotRemoteRuntimeMountRow, error)
	ListBotRemoteRuntimeMounts(context.Context, pgtype.UUID) ([]runtimesqlc.ListBotRemoteRuntimeMountsRow, error)
	SetBotRemoteRuntimePrimary(context.Context, runtimesqlc.SetBotRemoteRuntimePrimaryParams) (int64, error)
	UpdateBotRemoteRuntimeMountToolApproval(context.Context, runtimesqlc.UpdateBotRemoteRuntimeMountToolApprovalParams) (pgtype.UUID, error)
}

// RemoteMountStore adapts the current generated Runtime statements to the
// Workspace remote-mount port. Primary changes require a real transaction.
type RemoteMountStore struct {
	queries      mountQueries
	transactions transactionBeginner
	bindQueries  func(pgx.Tx) mountQueries
}

type primaryTransaction struct {
	queries mountQueries
}

var _ runtimeworkspace.RemoteMountStore = (*RemoteMountStore)(nil)

func NewRemoteMountStore(pool *pgxpool.Pool) *RemoteMountStore {
	if pool == nil {
		return nil
	}
	queries := runtimesqlc.New(pool)
	return newRemoteMountStore(queries, pool, func(tx pgx.Tx) mountQueries { return queries.WithTx(tx) })
}

func newRemoteMountStore(queries mountQueries, transactions transactionBeginner, bindQueries func(pgx.Tx) mountQueries) *RemoteMountStore {
	return &RemoteMountStore{queries: queries, transactions: transactions, bindQueries: bindQueries}
}

func (s *RemoteMountStore) CreateOrUpdateMount(ctx context.Context, botID, runtimeID, ownerUserID string) (runtimeworkspace.RemoteMountRecord, error) {
	parsedBotID, err := db.ParseUUID(botID)
	if err != nil {
		return runtimeworkspace.RemoteMountRecord{}, err
	}
	parsedRuntimeID, err := db.ParseUUID(runtimeID)
	if err != nil {
		return runtimeworkspace.RemoteMountRecord{}, err
	}
	parsedOwnerUserID, err := db.ParseUUID(ownerUserID)
	if err != nil {
		return runtimeworkspace.RemoteMountRecord{}, err
	}
	targetID, err := s.queries.CreateOrUpdateBotRemoteRuntimeMount(ctx, runtimesqlc.CreateOrUpdateBotRemoteRuntimeMountParams{
		BotID: parsedBotID, RuntimeID: parsedRuntimeID, OwnerUserID: parsedOwnerUserID,
	})
	if err != nil {
		return runtimeworkspace.RemoteMountRecord{}, mapQueryError(err)
	}
	return s.GetMount(ctx, botID, targetID.String())
}

func (s *RemoteMountStore) ListMounts(ctx context.Context, botID string) ([]runtimeworkspace.RemoteMountRecord, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListBotRemoteRuntimeMounts(ctx, id)
	if err != nil {
		return nil, mapQueryError(err)
	}
	records := make([]runtimeworkspace.RemoteMountRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, remoteMountRecord(
			row.ID, row.BotID, row.RuntimeID, row.IsPrimary, row.ToolApprovalConfig,
			row.RuntimeName, row.RuntimeUserID, row.RevokedAt,
			row.CreatedAt, row.UpdatedAt,
		))
	}
	return records, nil
}

func (s *RemoteMountStore) GetMount(ctx context.Context, botID, targetID string) (runtimeworkspace.RemoteMountRecord, error) {
	botUUID, targetUUID, err := parseRemoteMountIDs(botID, targetID)
	if err != nil {
		return runtimeworkspace.RemoteMountRecord{}, err
	}
	row, err := s.queries.GetBotRemoteRuntimeMount(ctx, runtimesqlc.GetBotRemoteRuntimeMountParams{BotID: botUUID, TargetID: targetUUID})
	if err != nil {
		return runtimeworkspace.RemoteMountRecord{}, mapQueryError(err)
	}
	return remoteMountRecord(
		row.ID, row.BotID, row.RuntimeID, row.IsPrimary, row.ToolApprovalConfig,
		row.RuntimeName, row.RuntimeUserID, row.RevokedAt,
		row.CreatedAt, row.UpdatedAt,
	), nil
}

func (s *RemoteMountStore) GetPrimaryMount(ctx context.Context, botID string) (runtimeworkspace.RemoteMountRecord, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return runtimeworkspace.RemoteMountRecord{}, err
	}
	row, err := s.queries.GetPrimaryBotRemoteRuntimeMount(ctx, id)
	if err != nil {
		return runtimeworkspace.RemoteMountRecord{}, mapQueryError(err)
	}
	return remoteMountRecord(
		row.ID, row.BotID, row.RuntimeID, row.IsPrimary, row.ToolApprovalConfig,
		row.RuntimeName, row.RuntimeUserID, row.RevokedAt,
		row.CreatedAt, row.UpdatedAt,
	), nil
}

func (s *RemoteMountStore) SetPrimary(ctx context.Context, botID, targetID string) error {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(targetID) == "" || strings.EqualFold(strings.TrimSpace(targetID), runtimeworkspace.WorkspaceTargetNative) {
		return mapQueryError(s.queries.ClearBotRemoteRuntimePrimary(ctx, botUUID))
	}
	targetUUID, err := db.ParseUUID(targetID)
	if err != nil {
		return err
	}
	return s.RunPrimaryMountTransaction(ctx, func(transaction runtimeworkspace.PrimaryMountTransaction) error {
		return transaction.SetPrimary(ctx, botUUID.String(), targetUUID.String())
	})
}

func (s *RemoteMountStore) RunPrimaryMountTransaction(ctx context.Context, fn func(runtimeworkspace.PrimaryMountTransaction) error) error {
	if fn == nil {
		return errors.New("workspace primary transaction callback is required")
	}
	if s == nil || s.transactions == nil || s.bindQueries == nil {
		return runtimeworkspace.ErrTransactionsRequired
	}
	tx, err := s.transactions.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(BindPrimaryTransaction(s.bindQueries(tx))); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// BindPrimaryTransaction binds Workspace's atomic primary-mount replacement
// to transaction-scoped PostgreSQL queries.
func BindPrimaryTransaction(queries mountQueries) runtimeworkspace.PrimaryMountTransaction {
	return &primaryTransaction{queries: queries}
}

func (tx *primaryTransaction) SetPrimary(ctx context.Context, botID, targetID string) error {
	botUUID, targetUUID, err := parseRemoteMountIDs(botID, targetID)
	if err != nil {
		return err
	}
	if err := tx.queries.ClearBotRemoteRuntimePrimary(ctx, botUUID); err != nil {
		return mapQueryError(err)
	}
	rows, err := tx.queries.SetBotRemoteRuntimePrimary(ctx, runtimesqlc.SetBotRemoteRuntimePrimaryParams{
		BotID: botUUID, TargetID: targetUUID,
	})
	if err != nil {
		return mapQueryError(err)
	}
	if rows == 0 {
		return runtimeworkspace.ErrRecordNotFound
	}
	return nil
}

func (s *RemoteMountStore) UpdateToolApproval(ctx context.Context, botID, targetID string, config []byte) error {
	botUUID, targetUUID, err := parseRemoteMountIDs(botID, targetID)
	if err != nil {
		return err
	}
	_, err = s.queries.UpdateBotRemoteRuntimeMountToolApproval(ctx, runtimesqlc.UpdateBotRemoteRuntimeMountToolApprovalParams{
		BotID: botUUID, TargetID: targetUUID, ToolApprovalConfig: config,
	})
	return mapQueryError(err)
}

func (s *RemoteMountStore) DeleteMount(ctx context.Context, botID, targetID string) error {
	botUUID, targetUUID, err := parseRemoteMountIDs(botID, targetID)
	if err != nil {
		return err
	}
	_, err = s.queries.DeleteBotRemoteRuntimeMount(ctx, runtimesqlc.DeleteBotRemoteRuntimeMountParams{
		BotID: botUUID, TargetID: targetUUID,
	})
	return mapQueryError(err)
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
	revokedAt pgtype.Timestamptz,
	createdAt, updatedAt pgtype.Timestamptz,
) runtimeworkspace.RemoteMountRecord {
	return runtimeworkspace.RemoteMountRecord{
		ID:             id.String(),
		BotID:          botID.String(),
		RuntimeID:      runtimeID.String(),
		IsPrimary:      isPrimary,
		ToolApproval:   append([]byte(nil), toolApproval...),
		RuntimeName:    runtimeName,
		RuntimeUserID:  runtimeUserID.String(),
		RuntimeRevoked: revokedAt.Valid,
		CreatedAt:      db.TimeFromPg(createdAt),
		UpdatedAt:      db.TimeFromPg(updatedAt),
	}
}

func mapQueryError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return runtimeworkspace.ErrRecordNotFound
	}
	return err
}
