package postgresstore

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

func (s *Store) CountAccounts(ctx context.Context) (int64, error) {
	return s.queries.CountAccounts(ctx)
}

func (s *Store) GetByUserID(ctx context.Context, userID string) (dbstore.AccountRecord, error) {
	id, err := db.ParseUUID(userID)
	if err != nil {
		return dbstore.AccountRecord{}, err
	}
	row, err := s.queries.GetAccountByUserID(ctx, id)
	if err != nil {
		return dbstore.AccountRecord{}, mapQueryErr(err)
	}
	return accountRecord(row), nil
}

func (s *Store) GetByIdentity(ctx context.Context, identity string) (dbstore.AccountRecord, error) {
	row, err := s.queries.GetAccountByIdentity(ctx, pgtype.Text{String: identity, Valid: identity != ""})
	if err != nil {
		return dbstore.AccountRecord{}, mapQueryErr(err)
	}
	return accountRecord(row), nil
}

func (s *Store) List(ctx context.Context) ([]dbstore.AccountRecord, error) {
	rows, err := s.queries.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	return accountRecords(rows), nil
}

func (s *Store) Search(ctx context.Context, query string, limit int32) ([]dbstore.AccountRecord, error) {
	rows, err := s.queries.SearchAccounts(ctx, dbsqlc.SearchAccountsParams{
		Query:      query,
		LimitCount: limit,
	})
	if err != nil {
		return nil, err
	}
	return accountRecords(rows), nil
}

func (s *Store) CreateUser(ctx context.Context, input dbstore.CreateUserInput) (dbstore.AccountRecord, error) {
	row, err := s.queries.CreateUser(ctx, dbsqlc.CreateUserParams{
		IsActive: input.IsActive,
		Metadata: input.Metadata,
	})
	if err != nil {
		return dbstore.AccountRecord{}, err
	}
	return accountRecord(dbsqlc.TeamAccount(row)), nil
}

func (s *Store) CreateAccount(ctx context.Context, input dbstore.CreateAccountInput) (dbstore.AccountRecord, error) {
	userID, err := db.ParseUUID(input.UserID)
	if err != nil {
		return dbstore.AccountRecord{}, err
	}
	row, err := s.queries.CreateAccount(ctx, dbsqlc.CreateAccountParams{
		UserID:       userID,
		Username:     text(input.Username),
		Email:        optionalText(input.Email),
		PasswordHash: text(input.PasswordHash),
		Role:         input.Role,
		DisplayName:  optionalText(input.DisplayName),
		AvatarUrl:    optionalText(input.AvatarURL),
		IsActive:     input.IsActive,
		DataRoot:     optionalText(input.DataRoot),
	})
	if err != nil {
		return dbstore.AccountRecord{}, err
	}
	return accountRecord(dbsqlc.TeamAccount(row)), nil
}

func (s *Store) UpdateLastLogin(ctx context.Context, accountID string) error {
	id, err := db.ParseUUID(accountID)
	if err != nil {
		return err
	}
	_, err = s.queries.UpdateAccountLastLogin(ctx, id)
	return err
}

func (s *Store) UpdateAdmin(ctx context.Context, input dbstore.UpdateAccountAdminInput) (dbstore.AccountRecord, error) {
	userID, err := db.ParseUUID(input.UserID)
	if err != nil {
		return dbstore.AccountRecord{}, err
	}
	row, err := s.queries.UpdateAccountAdmin(ctx, dbsqlc.UpdateAccountAdminParams{
		UserID:   userID,
		Role:     input.Role,
		IsActive: optionalBool(input.IsActive),
	})
	if err != nil {
		return dbstore.AccountRecord{}, mapQueryErr(err)
	}
	return accountRecord(dbsqlc.TeamAccount(row)), nil
}

func (s *Store) UpdateProfile(ctx context.Context, input dbstore.UpdateAccountProfileInput) (dbstore.AccountRecord, error) {
	userID, err := db.ParseUUID(input.UserID)
	if err != nil {
		return dbstore.AccountRecord{}, err
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
		return dbstore.AccountRecord{}, mapQueryErr(err)
	}
	return accountRecord(dbsqlc.TeamAccount(row)), nil
}

func (s *Store) IsValidTitleModel(ctx context.Context, modelID string) (bool, error) {
	id, err := db.ParseUUID(modelID)
	if err != nil {
		return false, nil
	}
	model, err := s.queries.GetModelByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return model.Type == "chat", nil
}

func (s *Store) UpdatePassword(ctx context.Context, input dbstore.UpdateAccountPasswordInput) error {
	userID, err := db.ParseUUID(input.UserID)
	if err != nil {
		return err
	}
	_, err = s.queries.UpdateAccountPassword(ctx, dbsqlc.UpdateAccountPasswordParams{
		PasswordHash: text(input.PasswordHash),
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
		return db.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName == "team_members_last_active_admin" {
		return db.ErrLastActiveAdmin
	}
	return err
}

func text(value string) pgtype.Text {
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

func accountRecords(rows []dbsqlc.TeamAccount) []dbstore.AccountRecord {
	items := make([]dbstore.AccountRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, accountRecord(row))
	}
	return items
}

func accountRecord(row dbsqlc.TeamAccount) dbstore.AccountRecord {
	rec := dbstore.AccountRecord{
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
		rec.CreatedAt = row.CreatedAt.Time
	}
	if row.UpdatedAt.Valid {
		rec.UpdatedAt = row.UpdatedAt.Time
	}
	if row.JoinedAt.Valid {
		rec.JoinedAt = row.JoinedAt.Time
	}
	if row.MembershipUpdatedAt.Valid {
		rec.MembershipUpdatedAt = row.MembershipUpdatedAt.Time
	}
	if row.LastLoginAt.Valid {
		rec.LastLoginAt = row.LastLoginAt.Time
	}
	if row.TitleModelID.Valid {
		rec.TitleModelID = uuid.UUID(row.TitleModelID.Bytes).String()
	}
	return rec
}

func optionalUUID(value string) pgtype.UUID {
	parsed, err := db.ParseUUID(value)
	if err != nil {
		return pgtype.UUID{}
	}
	return parsed
}
