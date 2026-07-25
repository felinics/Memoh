// Package postgres implements Channel Access-owned PostgreSQL persistence.
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/domains/api/access"
	apisqlc "github.com/memohai/memoh/domains/api/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type queries interface {
	CreateChannelLinkCode(context.Context, apisqlc.CreateChannelLinkCodeParams) (apisqlc.ApiChannelLinkCode, error)
	DeleteUserChannelIdentityBinding(context.Context, apisqlc.DeleteUserChannelIdentityBindingParams) error
	GetChannelLinkCodeByToken(context.Context, string) (apisqlc.ApiChannelLinkCode, error)
	ListChannelIdentityBindingsForBot(context.Context, pgtype.UUID) ([]apisqlc.ApiUserChannelIdentityBinding, error)
	ListChannelIdentityBindingsForUser(context.Context, pgtype.UUID) ([]apisqlc.ApiUserChannelIdentityBinding, error)
	ListUserIDsByChannelIdentity(context.Context, pgtype.UUID) ([]pgtype.UUID, error)
	RedeemChannelLinkCode(context.Context, apisqlc.RedeemChannelLinkCodeParams) (apisqlc.ApiUserChannelIdentityBinding, error)
}

type Store struct {
	queries queries
}

var _ access.Store = (*Store)(nil)

func NewStore(queries queries) *Store {
	return &Store{queries: queries}
}

func (s *Store) CreateLinkCode(ctx context.Context, command access.CreateLinkCodeCommand) (access.LinkCode, error) {
	userID, err := db.ParseUUID(command.UserID)
	if err != nil {
		return access.LinkCode{}, err
	}
	row, err := s.queries.CreateChannelLinkCode(ctx, apisqlc.CreateChannelLinkCodeParams{
		Token:     command.Token,
		UserID:    userID,
		ExpiresAt: pgtype.Timestamptz{Time: command.ExpiresAt, Valid: true},
		// channel_type is NOT NULL and the empty string is the no-platform sentinel.
		ChannelType: pgtype.Text{String: command.ChannelType, Valid: true},
	})
	if err != nil {
		return access.LinkCode{}, err
	}
	return access.LinkCode{
		Token:       row.Token,
		UserID:      uuidString(row.UserID),
		ChannelType: row.ChannelType,
		ExpiresAt:   pgTime(row.ExpiresAt),
		CreatedAt:   pgTime(row.CreatedAt),
	}, nil
}

func (s *Store) FindLinkCode(ctx context.Context, token string) (access.LinkCodeState, bool, error) {
	row, err := s.queries.GetChannelLinkCodeByToken(ctx, token)
	if errors.Is(err, pgx.ErrNoRows) {
		return access.LinkCodeState{}, false, nil
	}
	if err != nil {
		return access.LinkCodeState{}, false, err
	}
	return access.LinkCodeState{
		ExpiresAt: pgTime(row.ExpiresAt),
		Consumed:  row.ConsumedAt.Valid,
	}, true, nil
}

func (s *Store) RedeemLinkCode(ctx context.Context, token, channelIdentityID string) (access.Binding, bool, error) {
	identityID, err := db.ParseUUID(channelIdentityID)
	if err != nil {
		return access.Binding{}, false, err
	}
	row, err := s.queries.RedeemChannelLinkCode(ctx, apisqlc.RedeemChannelLinkCodeParams{
		Token:             token,
		ChannelIdentityID: identityID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return access.Binding{}, false, nil
	}
	if err != nil {
		return access.Binding{}, false, err
	}
	return access.Binding{
		ID:                uuidString(row.ID),
		UserID:            uuidString(row.UserID),
		ChannelIdentityID: uuidString(row.ChannelIdentityID),
		CreatedAt:         pgTime(row.CreatedAt),
	}, true, nil
}

func (s *Store) ListBindingsForUser(ctx context.Context, userID string) ([]access.Binding, error) {
	id, err := db.ParseUUID(userID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListChannelIdentityBindingsForUser(ctx, id)
	if err != nil {
		return nil, err
	}
	items := make([]access.Binding, 0, len(rows))
	for _, row := range rows {
		items = append(items, binding(row))
	}
	return items, nil
}

func (s *Store) ListBindingsForBot(ctx context.Context, botID string) ([]access.Binding, error) {
	// Preserve the legacy behavior: malformed bot IDs query with an invalid UUID
	// value and return no scoped bindings rather than failing manager listing.
	rows, err := s.queries.ListChannelIdentityBindingsForBot(ctx, db.ParseUUIDOrEmpty(botID))
	if err != nil {
		return nil, err
	}
	items := make([]access.Binding, 0, len(rows))
	for _, row := range rows {
		items = append(items, binding(row))
	}
	return items, nil
}

func (s *Store) DeleteBinding(ctx context.Context, userID, channelIdentityID string) error {
	user, err := db.ParseUUID(userID)
	if err != nil {
		return err
	}
	identity, err := db.ParseUUID(channelIdentityID)
	if err != nil {
		return err
	}
	return s.queries.DeleteUserChannelIdentityBinding(ctx, apisqlc.DeleteUserChannelIdentityBindingParams{
		UserID:            user,
		ChannelIdentityID: identity,
	})
}

func (s *Store) ListUserIDsByChannelIdentity(ctx context.Context, channelIdentityID string) ([]string, error) {
	id, err := db.ParseUUID(channelIdentityID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListUserIDsByChannelIdentity(ctx, id)
	if err != nil {
		return nil, err
	}
	items := make([]string, 0, len(rows))
	for _, row := range rows {
		if value := uuidString(row); value != "" {
			items = append(items, value)
		}
	}
	return items, nil
}

func binding(row apisqlc.ApiUserChannelIdentityBinding) access.Binding {
	return access.Binding{
		ID:                uuidString(row.ID),
		UserID:            uuidString(row.UserID),
		ChannelIdentityID: uuidString(row.ChannelIdentityID),
		CreatedAt:         pgTime(row.CreatedAt),
	}
}

func uuidString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return uuid.UUID(id.Bytes).String()
}

func pgTime(value pgtype.Timestamptz) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time
}
