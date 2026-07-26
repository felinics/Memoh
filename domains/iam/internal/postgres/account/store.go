// Package account implements Accounts-owned PostgreSQL persistence.
package account

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/domains/iam/account/persistence"
	dbsqlc "github.com/memohai/memoh/domains/iam/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type accountQueries interface {
	CountAccounts(context.Context) (int64, error)
	GetAccountByUserID(context.Context, pgtype.UUID) (dbsqlc.IamTeamAccount, error)
	GetAccountByIdentity(context.Context, pgtype.Text) (dbsqlc.IamTeamAccount, error)
	ListAccounts(context.Context) ([]dbsqlc.IamTeamAccount, error)
	SearchAccounts(context.Context, dbsqlc.SearchAccountsParams) ([]dbsqlc.IamTeamAccount, error)
	CreateUser(context.Context, dbsqlc.CreateUserParams) (dbsqlc.CreateUserRow, error)
	CreateAccount(context.Context, dbsqlc.CreateAccountParams) (dbsqlc.CreateAccountRow, error)
	UpdateAccountLastLogin(context.Context, pgtype.UUID) (pgtype.UUID, error)
	UpdateAccountAdmin(context.Context, dbsqlc.UpdateAccountAdminParams) (dbsqlc.UpdateAccountAdminRow, error)
	UpdateAccountProfile(context.Context, dbsqlc.UpdateAccountProfileParams) (dbsqlc.UpdateAccountProfileRow, error)
	UpdateAccountPassword(context.Context, dbsqlc.UpdateAccountPasswordParams) (pgtype.UUID, error)
	RemoveMember(context.Context, pgtype.UUID) (pgtype.UUID, error)
}

// Store adapts IAM-owned statements to the Accounts persistence port.
type Store struct {
	queries accountQueries
}

var (
	_ persistence.Store          = (*Store)(nil)
	_ persistence.AccountCounter = (*Store)(nil)
)

func NewStore(pool *pgxpool.Pool) *Store {
	return newStore(dbsqlc.New(pool))
}

func newStore(queries accountQueries) *Store {
	return &Store{queries: queries}
}

func (s *Store) CountAccounts(ctx context.Context) (int64, error) {
	return s.queries.CountAccounts(ctx)
}

func (s *Store) GetByUserID(ctx context.Context, userID string) (persistence.Record, error) {
	id, err := db.ParseUUID(userID)
	if err != nil {
		return persistence.Record{}, err
	}
	row, err := s.queries.GetAccountByUserID(ctx, id)
	if err != nil {
		return persistence.Record{}, mapQueryErr(err)
	}
	return record(row), nil
}

func (s *Store) GetByIdentity(ctx context.Context, identity string) (persistence.Record, error) {
	row, err := s.queries.GetAccountByIdentity(ctx, pgtype.Text{String: identity, Valid: identity != ""})
	if err != nil {
		return persistence.Record{}, mapQueryErr(err)
	}
	return record(row), nil
}

func (s *Store) List(ctx context.Context) ([]persistence.Record, error) {
	rows, err := s.queries.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	return records(rows), nil
}

func (s *Store) Search(ctx context.Context, query string, limit int) ([]persistence.Record, error) {
	rows, err := s.queries.SearchAccounts(ctx, dbsqlc.SearchAccountsParams{
		Query:      query,
		LimitCount: int32(limit), //nolint:gosec // Preserve the existing SQL limit conversion at this boundary.
	})
	if err != nil {
		return nil, err
	}
	return records(rows), nil
}

func (s *Store) CreateUser(ctx context.Context, input persistence.CreateUserInput) (persistence.Record, error) {
	row, err := s.queries.CreateUser(ctx, dbsqlc.CreateUserParams{
		IsActive: input.IsActive,
		Metadata: input.Metadata,
	})
	if err != nil {
		return persistence.Record{}, err
	}
	return record(dbsqlc.IamTeamAccount(row)), nil
}

func (s *Store) CreateAccount(ctx context.Context, input persistence.CreateInput) (persistence.Record, error) {
	userID, err := db.ParseUUID(input.UserID)
	if err != nil {
		return persistence.Record{}, err
	}
	row, err := s.queries.CreateAccount(ctx, dbsqlc.CreateAccountParams{
		UserID:       userID,
		Username:     requiredText(input.Username),
		Email:        optionalText(input.Email),
		PasswordHash: requiredText(input.PasswordHash),
		Role:         input.Role,
		DisplayName:  optionalText(input.DisplayName),
		AvatarUrl:    optionalText(input.AvatarURL),
		IsActive:     input.IsActive,
	})
	if err != nil {
		return persistence.Record{}, err
	}
	return record(dbsqlc.IamTeamAccount(row)), nil
}

func (s *Store) UpdateLastLogin(ctx context.Context, accountID string) error {
	id, err := db.ParseUUID(accountID)
	if err != nil {
		return err
	}
	_, err = s.queries.UpdateAccountLastLogin(ctx, id)
	return mapQueryErr(err)
}

func (s *Store) UpdateAdmin(ctx context.Context, input persistence.AdminUpdate) (persistence.Record, error) {
	userID, err := db.ParseUUID(input.UserID)
	if err != nil {
		return persistence.Record{}, err
	}
	row, err := s.queries.UpdateAccountAdmin(ctx, dbsqlc.UpdateAccountAdminParams{
		UserID:   userID,
		Role:     input.Role,
		IsActive: optionalBool(input.IsActive),
	})
	if err != nil {
		return persistence.Record{}, mapQueryErr(err)
	}
	return record(dbsqlc.IamTeamAccount(row)), nil
}

func (s *Store) UpdateProfile(ctx context.Context, input persistence.ProfileUpdate) (persistence.Record, error) {
	userID, err := db.ParseUUID(input.UserID)
	if err != nil {
		return persistence.Record{}, err
	}
	row, err := s.queries.UpdateAccountProfile(ctx, dbsqlc.UpdateAccountProfileParams{
		UserID:       userID,
		DisplayName:  optionalText(input.DisplayName),
		AvatarUrl:    optionalText(input.AvatarURL),
		Timezone:     input.Timezone,
		Metadata:     []byte(input.Metadata),
		TitleModelID: optionalUUID(input.TitleModelID),
	})
	if err != nil {
		return persistence.Record{}, mapQueryErr(err)
	}
	return record(dbsqlc.IamTeamAccount(row)), nil
}

func (s *Store) UpdatePassword(ctx context.Context, input persistence.PasswordUpdate) error {
	userID, err := db.ParseUUID(input.UserID)
	if err != nil {
		return err
	}
	_, err = s.queries.UpdateAccountPassword(ctx, dbsqlc.UpdateAccountPasswordParams{
		PasswordHash: requiredText(input.PasswordHash),
		UserID:       userID,
	})
	return mapQueryErr(err)
}

func (s *Store) RemoveMember(ctx context.Context, userID string) error {
	id, err := db.ParseUUID(userID)
	if err != nil {
		return err
	}
	_, err = s.queries.RemoveMember(ctx, id)
	return mapQueryErr(err)
}

func mapQueryErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return persistence.ErrAccountNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName == "team_members_last_active_admin" {
		return persistence.ErrLastActiveAdmin
	}
	return err
}

func requiredText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: true}
}

func optionalText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

func optionalBool(value *bool) pgtype.Bool {
	if value == nil {
		return pgtype.Bool{}
	}
	return pgtype.Bool{Bool: *value, Valid: true}
}

func optionalUUID(value string) pgtype.UUID {
	parsed, err := db.ParseUUID(value)
	if err != nil {
		return pgtype.UUID{}
	}
	return parsed
}

func records(rows []dbsqlc.IamTeamAccount) []persistence.Record {
	items := make([]persistence.Record, 0, len(rows))
	for _, row := range rows {
		items = append(items, record(row))
	}
	return items
}

func record(row dbsqlc.IamTeamAccount) persistence.Record {
	result := persistence.Record{
		ID:               row.ID.String(),
		Username:         row.Username.String,
		Email:            row.Email.String,
		Role:             row.Role,
		DisplayName:      row.DisplayName.String,
		AvatarURL:        row.AvatarUrl.String,
		Timezone:         row.Timezone,
		PasswordHash:     row.PasswordHash.String,
		HasPasswordHash:  row.PasswordHash.Valid,
		IsActive:         row.IsActive.Bool,
		PrincipalActive:  row.PrincipalIsActive,
		MembershipActive: row.MembershipIsActive,
		Metadata:         string(row.Metadata),
	}
	if row.CreatedAt.Valid {
		result.CreatedAt = row.CreatedAt.Time
	}
	if row.UpdatedAt.Valid {
		result.UpdatedAt = row.UpdatedAt.Time
	}
	if row.JoinedAt.Valid {
		result.JoinedAt = row.JoinedAt.Time
	}
	if row.MembershipUpdatedAt.Valid {
		result.MembershipUpdatedAt = row.MembershipUpdatedAt.Time
	}
	if row.LastLoginAt.Valid {
		result.LastLoginAt = row.LastLoginAt.Time
	}
	if row.TitleModelID.Valid {
		result.TitleModelID = uuid.UUID(row.TitleModelID.Bytes).String()
	}
	return result
}
