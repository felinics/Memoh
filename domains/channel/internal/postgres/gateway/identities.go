package gateway

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/domains/channel/identity"
	channelsqlc "github.com/memohai/memoh/domains/channel/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type identityQueries interface {
	CreateChannelIdentity(context.Context, channelsqlc.CreateChannelIdentityParams) (channelsqlc.ChannelChannelIdentity, error)
	GetChannelIdentityByID(context.Context, pgtype.UUID) (channelsqlc.ChannelChannelIdentity, error)
	ListChannelIdentitiesByIDs(context.Context, []pgtype.UUID) ([]channelsqlc.ChannelChannelIdentity, error)
	UpsertChannelIdentityByChannelSubject(context.Context, channelsqlc.UpsertChannelIdentityByChannelSubjectParams) (channelsqlc.ChannelChannelIdentity, error)
	SearchChannelIdentities(context.Context, channelsqlc.SearchChannelIdentitiesParams) ([]channelsqlc.ChannelChannelIdentity, error)
}

func (s *Store) Create(ctx context.Context, input identity.WriteInput) (identity.ChannelIdentity, error) {
	metadata, err := marshalMap(input.Metadata)
	if err != nil {
		return identity.ChannelIdentity{}, err
	}
	row, err := s.identities.CreateChannelIdentity(ctx, channelsqlc.CreateChannelIdentityParams{
		ChannelType:      input.Channel,
		ChannelSubjectID: input.ChannelSubjectID,
		DisplayName:      optionalText(input.DisplayName),
		AvatarUrl:        optionalText(input.AvatarURL),
		Metadata:         metadata,
	})
	if err != nil {
		return identity.ChannelIdentity{}, err
	}
	return channelIdentity(row), nil
}

func (s *Store) FindByID(ctx context.Context, identityID string) (identity.ChannelIdentity, error) {
	id, err := db.ParseUUID(identityID)
	if err != nil {
		return identity.ChannelIdentity{}, err
	}
	row, err := s.identities.GetChannelIdentityByID(ctx, id)
	if err != nil {
		return identity.ChannelIdentity{}, mapIdentityError(err)
	}
	return channelIdentity(row), nil
}

func (s *Store) ListByIDs(ctx context.Context, identityIDs []string) ([]identity.ChannelIdentity, error) {
	if len(identityIDs) == 0 {
		return []identity.ChannelIdentity{}, nil
	}
	ids := make([]pgtype.UUID, 0, len(identityIDs))
	for _, identityID := range identityIDs {
		id, err := db.ParseUUID(identityID)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	rows, err := s.identities.ListChannelIdentitiesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	items := make([]identity.ChannelIdentity, 0, len(rows))
	for _, row := range rows {
		items = append(items, channelIdentity(row))
	}
	return items, nil
}

func (s *Store) Upsert(ctx context.Context, input identity.WriteInput) (identity.ChannelIdentity, error) {
	metadata, err := marshalMap(input.Metadata)
	if err != nil {
		return identity.ChannelIdentity{}, err
	}
	row, err := s.identities.UpsertChannelIdentityByChannelSubject(ctx, channelsqlc.UpsertChannelIdentityByChannelSubjectParams{
		ChannelType:      input.Channel,
		ChannelSubjectID: input.ChannelSubjectID,
		DisplayName:      optionalText(input.DisplayName),
		AvatarUrl:        optionalText(input.AvatarURL),
		Metadata:         metadata,
	})
	if err != nil {
		return identity.ChannelIdentity{}, err
	}
	return channelIdentity(row), nil
}

func (s *Store) Search(ctx context.Context, query string, limit int) ([]identity.ChannelIdentity, error) {
	rows, err := s.identities.SearchChannelIdentities(ctx, channelsqlc.SearchChannelIdentitiesParams{
		Query:      strings.TrimSpace(query),
		LimitCount: int32(limit), //nolint:gosec // Service supplies the bounded positive limit.
	})
	if err != nil {
		return nil, err
	}
	items := make([]identity.ChannelIdentity, 0, len(rows))
	for _, row := range rows {
		items = append(items, channelIdentity(row))
	}
	return items, nil
}

func mapIdentityError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.ErrChannelIdentityNotFound
	}
	return err
}

func channelIdentity(row channelsqlc.ChannelChannelIdentity) identity.ChannelIdentity {
	return identity.ChannelIdentity{
		ID:               db.UUIDString(row.ID),
		Channel:          strings.TrimSpace(row.ChannelType),
		ChannelSubjectID: strings.TrimSpace(row.ChannelSubjectID),
		DisplayName:      strings.TrimSpace(row.DisplayName.String),
		AvatarURL:        strings.TrimSpace(row.AvatarUrl.String),
		Metadata:         decodeMap(row.Metadata),
		CreatedAt:        timestamp(row.CreatedAt),
		UpdatedAt:        timestamp(row.UpdatedAt),
	}
}
