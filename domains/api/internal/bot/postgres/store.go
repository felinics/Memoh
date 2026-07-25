// Package postgres implements Bots-owned PostgreSQL projections.
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/domains/api/bot"
	apisqlc "github.com/memohai/memoh/domains/api/internal/postgres/sqlc"
	"github.com/memohai/memoh/domains/runtime/workspace"
	"github.com/memohai/memoh/internal/db"
)

type queries interface {
	CreateBot(context.Context, apisqlc.CreateBotParams) (apisqlc.CreateBotRow, error)
	DeleteBotByID(context.Context, pgtype.UUID) error
	GetBotByID(context.Context, pgtype.UUID) (apisqlc.GetBotByIDRow, error)
	GetBotByName(context.Context, string) (apisqlc.GetBotByNameRow, error)
	ListHeartbeatEnabledBots(context.Context) ([]apisqlc.ListHeartbeatEnabledBotsRow, error)
	ListBotsByOwner(context.Context, pgtype.UUID) ([]apisqlc.ListBotsByOwnerRow, error)
	ListAccessibleBots(context.Context, pgtype.UUID) ([]apisqlc.ListAccessibleBotsRow, error)
	TouchBotActivity(context.Context, pgtype.UUID) error
	UpdateBotProfile(context.Context, apisqlc.UpdateBotProfileParams) (apisqlc.UpdateBotProfileRow, error)
	UpdateBotOwner(context.Context, apisqlc.UpdateBotOwnerParams) (apisqlc.UpdateBotOwnerRow, error)
	UpdateBotStatus(context.Context, apisqlc.UpdateBotStatusParams) error
	ListBotUserGrants(context.Context, pgtype.UUID) ([]apisqlc.ListBotUserGrantsRow, error)
	ListBotUserGrantsForUser(context.Context, apisqlc.ListBotUserGrantsForUserParams) ([]apisqlc.ListBotUserGrantsForUserRow, error)
	GetBotUserGrantByID(context.Context, pgtype.UUID) (apisqlc.ApiBotUserGrant, error)
	CreateBotUserGrant(context.Context, apisqlc.CreateBotUserGrantParams) (apisqlc.ApiBotUserGrant, error)
	UpdateBotUserGrantPermissions(context.Context, apisqlc.UpdateBotUserGrantPermissionsParams) (apisqlc.ApiBotUserGrant, error)
	DeleteBotUserGrantByID(context.Context, pgtype.UUID) error
}

// Store exposes Runtime's narrow projection of API-owned bot profiles.
type Store struct {
	queries queries
}

var (
	_ bot.BotStore              = (*Store)(nil)
	_ bot.ActivityWriter        = (*Store)(nil)
	_ bot.HeartbeatReader       = (*Store)(nil)
	_ bot.GrantStore            = (*Store)(nil)
	_ workspace.BotProfileStore = (*Store)(nil)
	_ workspace.BotOwnerReader  = (*Store)(nil)
)

func (s *Store) TouchBot(ctx context.Context, botID string) error {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	return s.queries.TouchBotActivity(ctx, id)
}

func (s *Store) ListHeartbeatEnabledBots(ctx context.Context) ([]bot.HeartbeatRecord, error) {
	rows, err := s.queries.ListHeartbeatEnabledBots(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]bot.HeartbeatRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, bot.HeartbeatRecord{
			ID:                row.ID.String(),
			OwnerUserID:       row.OwnerUserID.String(),
			Status:            "ready",
			HeartbeatEnabled:  row.HeartbeatEnabled,
			HeartbeatInterval: int(row.HeartbeatInterval),
		})
	}
	return items, nil
}

func NewStore(queries queries) *Store {
	return &Store{queries: queries}
}

func (s *Store) CreateBot(ctx context.Context, input bot.CreateInput) (bot.Record, error) {
	ownerID, err := db.ParseUUID(input.OwnerUserID)
	if err != nil {
		return bot.Record{}, err
	}
	row, err := s.queries.CreateBot(ctx, apisqlc.CreateBotParams{
		OwnerUserID: ownerID,
		Name:        input.Name,
		DisplayName: text(input.DisplayName),
		AvatarUrl:   text(input.AvatarURL),
		Timezone:    text(input.Timezone),
		IsActive:    input.IsActive,
		Metadata:    cloneBytes(input.Metadata),
		Status:      input.Status,
	})
	if err != nil {
		return bot.Record{}, mapBotError(err)
	}
	return record(row.ID, row.OwnerUserID, row.Name, row.DisplayName, row.AvatarUrl, row.Timezone, row.IsActive, row.Status, row.Metadata, row.CreatedAt, row.UpdatedAt), nil
}

func (s *Store) GetBotByID(ctx context.Context, botID string) (bot.Record, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return bot.Record{}, err
	}
	row, err := s.queries.GetBotByID(ctx, id)
	if err != nil {
		return bot.Record{}, mapBotError(err)
	}
	return record(row.ID, row.OwnerUserID, row.Name, row.DisplayName, row.AvatarUrl, row.Timezone, row.IsActive, row.Status, row.Metadata, row.CreatedAt, row.UpdatedAt), nil
}

func (s *Store) GetBotByName(ctx context.Context, name string) (bot.Record, error) {
	row, err := s.queries.GetBotByName(ctx, name)
	if err != nil {
		return bot.Record{}, mapBotError(err)
	}
	return record(row.ID, row.OwnerUserID, row.Name, row.DisplayName, row.AvatarUrl, row.Timezone, row.IsActive, row.Status, row.Metadata, row.CreatedAt, row.UpdatedAt), nil
}

func (s *Store) ListBotsByOwner(ctx context.Context, ownerUserID string) ([]bot.Record, error) {
	id, err := db.ParseUUID(ownerUserID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListBotsByOwner(ctx, id)
	if err != nil {
		return nil, err
	}
	items := make([]bot.Record, 0, len(rows))
	for _, row := range rows {
		items = append(items, record(row.ID, row.OwnerUserID, row.Name, row.DisplayName, row.AvatarUrl, row.Timezone, row.IsActive, row.Status, row.Metadata, row.CreatedAt, row.UpdatedAt))
	}
	return items, nil
}

func (s *Store) ListAccessibleBots(ctx context.Context, userID string) ([]bot.Record, error) {
	id, err := db.ParseUUID(userID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListAccessibleBots(ctx, id)
	if err != nil {
		return nil, err
	}
	items := make([]bot.Record, 0, len(rows))
	for _, row := range rows {
		items = append(items, record(row.ID, row.OwnerUserID, row.Name, row.DisplayName, row.AvatarUrl, row.Timezone, row.IsActive, row.Status, row.Metadata, row.CreatedAt, row.UpdatedAt))
	}
	return items, nil
}

func (s *Store) UpdateBot(ctx context.Context, input bot.UpdateInput) (bot.Record, error) {
	id, err := db.ParseUUID(input.ID)
	if err != nil {
		return bot.Record{}, err
	}
	row, err := s.queries.UpdateBotProfile(ctx, apisqlc.UpdateBotProfileParams{
		ID:          id,
		Name:        input.Name,
		DisplayName: text(input.DisplayName),
		AvatarUrl:   text(input.AvatarURL),
		Timezone:    text(input.Timezone),
		IsActive:    input.IsActive,
		Metadata:    cloneBytes(input.Metadata),
	})
	if err != nil {
		return bot.Record{}, mapBotError(err)
	}
	return record(row.ID, row.OwnerUserID, row.Name, row.DisplayName, row.AvatarUrl, row.Timezone, row.IsActive, row.Status, row.Metadata, row.CreatedAt, row.UpdatedAt), nil
}

func (s *Store) UpdateBotOwner(ctx context.Context, botID, ownerUserID string) (bot.Record, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return bot.Record{}, err
	}
	ownerID, err := db.ParseUUID(ownerUserID)
	if err != nil {
		return bot.Record{}, err
	}
	row, err := s.queries.UpdateBotOwner(ctx, apisqlc.UpdateBotOwnerParams{ID: id, OwnerUserID: ownerID})
	if err != nil {
		return bot.Record{}, mapBotError(err)
	}
	return record(row.ID, row.OwnerUserID, row.Name, row.DisplayName, row.AvatarUrl, row.Timezone, row.IsActive, row.Status, row.Metadata, row.CreatedAt, row.UpdatedAt), nil
}

func (s *Store) UpdateBotStatus(ctx context.Context, botID, status string) error {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	return mapBotError(s.queries.UpdateBotStatus(ctx, apisqlc.UpdateBotStatusParams{ID: id, Status: status}))
}

func (s *Store) DeleteBot(ctx context.Context, botID string) error {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	return mapBotError(s.queries.DeleteBotByID(ctx, id))
}

func (s *Store) ListGrants(ctx context.Context, botID string) ([]bot.GrantRecord, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListBotUserGrants(ctx, id)
	if err != nil {
		return nil, err
	}
	items := make([]bot.GrantRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, bot.GrantRecord{
			ID: row.ID.String(), BotID: row.BotID.String(), SubjectType: row.SubjectType,
			UserID: uuidString(row.UserID), Permissions: cloneBytes(row.Permissions),
			CreatedAt: timestamp(row.CreatedAt), UpdatedAt: timestamp(row.UpdatedAt),
		})
	}
	return items, nil
}

func (s *Store) ListGrantsForUser(ctx context.Context, botID, userID string) ([]bot.GrantRecord, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	user, err := optionalUUID(userID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListBotUserGrantsForUser(ctx, apisqlc.ListBotUserGrantsForUserParams{BotID: id, UserID: user})
	if err != nil {
		return nil, err
	}
	items := make([]bot.GrantRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, bot.GrantRecord{
			ID: row.ID.String(), BotID: row.BotID.String(), SubjectType: row.SubjectType,
			UserID: uuidString(row.UserID), Permissions: cloneBytes(row.Permissions),
		})
	}
	return items, nil
}

func (s *Store) GetGrant(ctx context.Context, grantID string) (bot.GrantRecord, error) {
	id, err := db.ParseUUID(grantID)
	if err != nil {
		return bot.GrantRecord{}, err
	}
	row, err := s.queries.GetBotUserGrantByID(ctx, id)
	if err != nil {
		return bot.GrantRecord{}, mapGrantError(err)
	}
	return grantRecord(row), nil
}

func (s *Store) CreateGrant(ctx context.Context, input bot.CreateGrantInput) (bot.GrantRecord, error) {
	botID, err := db.ParseUUID(input.BotID)
	if err != nil {
		return bot.GrantRecord{}, err
	}
	userID, err := optionalUUID(input.UserID)
	if err != nil {
		return bot.GrantRecord{}, err
	}
	createdByUserID, err := optionalUUID(input.CreatedByUserID)
	if err != nil {
		return bot.GrantRecord{}, err
	}
	row, err := s.queries.CreateBotUserGrant(ctx, apisqlc.CreateBotUserGrantParams{
		BotID: botID, SubjectType: input.SubjectType, UserID: userID,
		Permissions: cloneBytes(input.Permissions), CreatedByUserID: createdByUserID,
	})
	if err != nil {
		return bot.GrantRecord{}, mapGrantError(err)
	}
	return grantRecord(row), nil
}

func (s *Store) UpdateGrantPermissions(ctx context.Context, grantID string, permissions []byte) (bot.GrantRecord, error) {
	id, err := db.ParseUUID(grantID)
	if err != nil {
		return bot.GrantRecord{}, err
	}
	row, err := s.queries.UpdateBotUserGrantPermissions(ctx, apisqlc.UpdateBotUserGrantPermissionsParams{
		ID: id, Permissions: cloneBytes(permissions),
	})
	if err != nil {
		return bot.GrantRecord{}, mapGrantError(err)
	}
	return grantRecord(row), nil
}

func (s *Store) DeleteGrant(ctx context.Context, grantID string) error {
	id, err := db.ParseUUID(grantID)
	if err != nil {
		return err
	}
	return mapGrantError(s.queries.DeleteBotUserGrantByID(ctx, id))
}

func (s *Store) LookupWorkspacePreferences(ctx context.Context, botID string) (workspace.WorkspacePreferences, bool, error) {
	row, found, err := s.findBot(ctx, botID)
	if err != nil || !found {
		return workspace.WorkspacePreferences{}, found, err
	}
	preferences, err := workspace.DecodeWorkspacePreferences(row.Metadata)
	return preferences, true, err
}

func (s *Store) RequireBot(ctx context.Context, botID string) error {
	_, found, err := s.findBot(ctx, botID)
	if err != nil {
		return err
	}
	if !found {
		return db.ErrNotFound
	}
	return nil
}

func (s *Store) BotOwnerUserID(ctx context.Context, botID string) (string, error) {
	row, err := s.GetBotByID(ctx, botID)
	if err != nil {
		return "", err
	}
	return row.OwnerUserID, nil
}

func (s *Store) SetWorkspaceImagePreference(ctx context.Context, botID, image string) error {
	return s.updateWorkspaceMetadata(ctx, botID, func(metadata []byte) ([]byte, error) {
		return workspace.PatchWorkspaceImagePreference(metadata, &image)
	})
}

func (s *Store) ClearWorkspaceImagePreference(ctx context.Context, botID string) error {
	return s.updateWorkspaceMetadata(ctx, botID, func(metadata []byte) ([]byte, error) {
		return workspace.PatchWorkspaceImagePreference(metadata, nil)
	})
}

func (s *Store) SetWorkspaceGPUPreference(ctx context.Context, botID string, gpu workspace.WorkspaceGPUConfig) error {
	return s.updateWorkspaceMetadata(ctx, botID, func(metadata []byte) ([]byte, error) {
		return workspace.PatchWorkspaceGPUPreference(metadata, &gpu)
	})
}

func (s *Store) ClearWorkspaceGPUPreference(ctx context.Context, botID string) error {
	return s.updateWorkspaceMetadata(ctx, botID, func(metadata []byte) ([]byte, error) {
		return workspace.PatchWorkspaceGPUPreference(metadata, nil)
	})
}

func (s *Store) findBot(ctx context.Context, botID string) (apisqlc.GetBotByIDRow, bool, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return apisqlc.GetBotByIDRow{}, false, err
	}
	row, err := s.queries.GetBotByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return apisqlc.GetBotByIDRow{}, false, nil
	}
	if err != nil {
		return apisqlc.GetBotByIDRow{}, false, err
	}
	return row, true, nil
}

func (s *Store) updateWorkspaceMetadata(ctx context.Context, botID string, patch func([]byte) ([]byte, error)) error {
	row, found, err := s.findBot(ctx, botID)
	if err != nil {
		return err
	}
	if !found {
		return db.ErrNotFound
	}
	metadata, err := patch(row.Metadata)
	if err != nil {
		return err
	}
	_, err = s.queries.UpdateBotProfile(ctx, apisqlc.UpdateBotProfileParams{
		ID:          row.ID,
		Name:        row.Name,
		DisplayName: row.DisplayName,
		AvatarUrl:   row.AvatarUrl,
		Timezone:    row.Timezone,
		IsActive:    row.IsActive,
		Metadata:    metadata,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.ErrNotFound
	}
	return err
}

func record(
	id, ownerUserID pgtype.UUID,
	name string,
	displayName, avatarURL, timezone pgtype.Text,
	isActive bool,
	status string,
	metadata []byte,
	createdAt, updatedAt pgtype.Timestamptz,
) bot.Record {
	return bot.Record{
		ID:          id.String(),
		OwnerUserID: ownerUserID.String(),
		Name:        name,
		DisplayName: textString(displayName),
		AvatarURL:   textString(avatarURL),
		Timezone:    textString(timezone),
		IsActive:    isActive,
		Status:      status,
		Metadata:    cloneBytes(metadata),
		CreatedAt:   timestamp(createdAt),
		UpdatedAt:   timestamp(updatedAt),
	}
}

func grantRecord(row apisqlc.ApiBotUserGrant) bot.GrantRecord {
	return bot.GrantRecord{
		ID:          row.ID.String(),
		BotID:       row.BotID.String(),
		SubjectType: row.SubjectType,
		UserID:      uuidString(row.UserID),
		Permissions: cloneBytes(row.Permissions),
		CreatedAt:   timestamp(row.CreatedAt),
		UpdatedAt:   timestamp(row.UpdatedAt),
	}
}

func mapBotError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return bot.ErrBotNotFound
	}
	if db.IsUniqueViolation(err) {
		return bot.ErrBotNameTaken
	}
	return err
}

func mapGrantError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return bot.ErrGrantNotFound
	}
	if db.IsUniqueViolation(err) {
		return bot.ErrGrantExists
	}
	return err
}

func optionalUUID(value string) (pgtype.UUID, error) {
	if value == "" {
		return pgtype.UUID{}, nil
	}
	return db.ParseUUID(value)
}

func uuidString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return value.String()
}

func text(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

func textString(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func timestamp(value pgtype.Timestamptz) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time
}

func cloneBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}

// NewStoreFromPool builds a Store backed by pool-scoped generated queries.
func NewStoreFromPool(pool *pgxpool.Pool) *Store {
	if pool == nil {
		return NewStore(nil)
	}
	return NewStore(apisqlc.New(pool))
}
