package account

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/domains/iam/account/persistence"
	dbsqlc "github.com/memohai/memoh/domains/iam/internal/postgres/sqlc"
)

type RecoveryStore struct {
	pool *pgxpool.Pool
}

var _ persistence.AdminRecoveryStore = (*RecoveryStore)(nil)

func NewRecoveryStore(pool *pgxpool.Pool) *RecoveryStore {
	return &RecoveryStore{pool: pool}
}

func (s *RecoveryStore) RecoverAdmin(ctx context.Context, identity, passwordHash string) error {
	if s == nil || s.pool == nil {
		return errors.New("account recovery database not configured")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin recovery: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	queries := dbsqlc.New(tx)
	account, err := queries.GetAccountByIdentity(ctx, pgtype.Text{String: identity, Valid: true})
	if err != nil {
		return fmt.Errorf("find account: %w", err)
	}
	if _, err := queries.RecoverAccountCredentials(ctx, dbsqlc.RecoverAccountCredentialsParams{
		PasswordHash: pgtype.Text{String: passwordHash, Valid: true},
		UserID:       account.ID,
	}); err != nil {
		return fmt.Errorf("update credentials: %w", err)
	}
	if _, err := queries.UpdateAccountAdmin(ctx, dbsqlc.UpdateAccountAdminParams{
		UserID:   account.ID,
		Role:     "admin",
		IsActive: pgtype.Bool{Bool: true, Valid: true},
	}); err != nil {
		return fmt.Errorf("restore admin membership: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit recovery: %w", err)
	}
	return nil
}
