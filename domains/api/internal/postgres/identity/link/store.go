// Package link implements Identity Link-owned PostgreSQL persistence.
package link

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	accesspersistence "github.com/memohai/memoh/domains/api/identity/link/persistence"
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

var _ accesspersistence.Store = (*Store)(nil)

func NewStore(pool *pgxpool.Pool) *Store {
	return newStore(apisqlc.New(pool))
}

func newStore(queries queries) *Store {
	return &Store{queries: queries}
}

func (s *Store) CreateLinkCode(ctx context.Context, command accesspersistence.CreateLinkCodeCommand) (accesspersistence.LinkCode, error) {
	userID, err := db.ParseUUID(command.UserID)
	if err != nil {
		return accesspersistence.LinkCode{}, err
	}
	row, err := s.queries.CreateChannelLinkCode(ctx, apisqlc.CreateChannelLinkCodeParams{
		Token:     command.Token,
		UserID:    userID,
		ExpiresAt: pgtype.Timestamptz{Time: command.ExpiresAt, Valid: true},
		// channel_type is NOT NULL and the empty string is the no-platform sentinel.
		ChannelType: pgtype.Text{String: command.ChannelType, Valid: true},
	})
	if err != nil {
		return accesspersistence.LinkCode{}, err
	}
	return accesspersistence.LinkCode{
		Token:       row.Token,
		UserID:      db.UUIDString(row.UserID),
		ChannelType: row.ChannelType,
		ExpiresAt:   db.TimeFromPg(row.ExpiresAt),
		CreatedAt:   db.TimeFromPg(row.CreatedAt),
	}, nil
}

func (s *Store) FindLinkCode(ctx context.Context, token string) (accesspersistence.LinkCodeState, bool, error) {
	row, err := s.queries.GetChannelLinkCodeByToken(ctx, token)
	if errors.Is(err, pgx.ErrNoRows) {
		return accesspersistence.LinkCodeState{}, false, nil
	}
	if err != nil {
		return accesspersistence.LinkCodeState{}, false, err
	}
	return accesspersistence.LinkCodeState{
		ExpiresAt: db.TimeFromPg(row.ExpiresAt),
		Consumed:  row.ConsumedAt.Valid,
	}, true, nil
}

func (s *Store) RedeemLinkCode(ctx context.Context, token, channelIdentityID string) (accesspersistence.Binding, bool, error) {
	identityID, err := db.ParseUUID(channelIdentityID)
	if err != nil {
		return accesspersistence.Binding{}, false, err
	}
	row, err := s.queries.RedeemChannelLinkCode(ctx, apisqlc.RedeemChannelLinkCodeParams{
		Token:             token,
		ChannelIdentityID: identityID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return accesspersistence.Binding{}, false, nil
	}
	if err != nil {
		return accesspersistence.Binding{}, false, err
	}
	return accesspersistence.Binding{
		ID:                db.UUIDString(row.ID),
		UserID:            db.UUIDString(row.UserID),
		ChannelIdentityID: db.UUIDString(row.ChannelIdentityID),
		CreatedAt:         db.TimeFromPg(row.CreatedAt),
	}, true, nil
}

func (s *Store) ListBindingsForUser(ctx context.Context, userID string) ([]accesspersistence.Binding, error) {
	id, err := db.ParseUUID(userID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListChannelIdentityBindingsForUser(ctx, id)
	if err != nil {
		return nil, err
	}
	items := make([]accesspersistence.Binding, 0, len(rows))
	for _, row := range rows {
		items = append(items, binding(row))
	}
	return items, nil
}

func (s *Store) ListBindingsForBot(ctx context.Context, botID string) ([]accesspersistence.Binding, error) {
	// Preserve the legacy behavior: malformed bot IDs query with an invalid UUID
	// value and return no scoped bindings rather than failing manager listing.
	rows, err := s.queries.ListChannelIdentityBindingsForBot(ctx, db.ParseUUIDOrEmpty(botID))
	if err != nil {
		return nil, err
	}
	items := make([]accesspersistence.Binding, 0, len(rows))
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
		if value := db.UUIDString(row); value != "" {
			items = append(items, value)
		}
	}
	return items, nil
}

func binding(row apisqlc.ApiUserChannelIdentityBinding) accesspersistence.Binding {
	return accesspersistence.Binding{
		ID:                db.UUIDString(row.ID),
		UserID:            db.UUIDString(row.UserID),
		ChannelIdentityID: db.UUIDString(row.ChannelIdentityID),
		CreatedAt:         db.TimeFromPg(row.CreatedAt),
	}
}
