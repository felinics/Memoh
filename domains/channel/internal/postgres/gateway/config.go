package gateway

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/domains/channel/gateway"
	channelsqlc "github.com/memohai/memoh/domains/channel/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type configQueries interface {
	UpsertBotChannelConfig(context.Context, channelsqlc.UpsertBotChannelConfigParams) (channelsqlc.ChannelBotChannelConfig, error)
	DeleteBotChannelConfig(context.Context, channelsqlc.DeleteBotChannelConfigParams) error
	UpdateBotChannelConfigDisabled(context.Context, channelsqlc.UpdateBotChannelConfigDisabledParams) (channelsqlc.ChannelBotChannelConfig, error)
	SaveMatrixSyncSinceToken(context.Context, channelsqlc.SaveMatrixSyncSinceTokenParams) (int64, error)
	GetBotChannelConfig(context.Context, channelsqlc.GetBotChannelConfigParams) (channelsqlc.ChannelBotChannelConfig, error)
	ListBotChannelConfigsByType(context.Context, string) ([]channelsqlc.ChannelBotChannelConfig, error)
}

func (s *Store) UpsertConfig(ctx context.Context, input gateway.ConfigWrite) (gateway.ChannelConfig, error) {
	botID, err := db.ParseUUID(input.BotID)
	if err != nil {
		return gateway.ChannelConfig{}, err
	}
	credentials, err := marshalMap(input.Credentials)
	if err != nil {
		return gateway.ChannelConfig{}, err
	}
	selfIdentity, err := marshalMap(input.SelfIdentity)
	if err != nil {
		return gateway.ChannelConfig{}, err
	}
	routing, err := marshalMap(input.Routing)
	if err != nil {
		return gateway.ChannelConfig{}, err
	}
	verifiedAt := pgtype.Timestamptz{}
	if input.VerifiedAt != nil {
		verifiedAt = pgtype.Timestamptz{Time: input.VerifiedAt.UTC(), Valid: true}
	}
	row, err := s.configs.UpsertBotChannelConfig(ctx, channelsqlc.UpsertBotChannelConfigParams{
		BotID:            botID,
		ChannelType:      input.ChannelType.String(),
		Credentials:      credentials,
		ExternalIdentity: optionalText(input.ExternalIdentity),
		SelfIdentity:     selfIdentity,
		Routing:          routing,
		Capabilities:     []byte("{}"),
		Disabled:         input.Disabled,
		VerifiedAt:       verifiedAt,
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return gateway.ChannelConfig{}, gateway.ErrExternalIdentityConflict
		}
		return gateway.ChannelConfig{}, err
	}
	return channelConfig(row)
}

func (s *Store) DeleteConfig(ctx context.Context, botID string, channelType gateway.ChannelType) error {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	return s.configs.DeleteBotChannelConfig(ctx, channelsqlc.DeleteBotChannelConfigParams{
		BotID:       id,
		ChannelType: channelType.String(),
	})
}

func (s *Store) UpdateConfigDisabled(ctx context.Context, botID string, channelType gateway.ChannelType, disabled bool) (gateway.ChannelConfig, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return gateway.ChannelConfig{}, err
	}
	row, err := s.configs.UpdateBotChannelConfigDisabled(ctx, channelsqlc.UpdateBotChannelConfigDisabledParams{
		BotID:       id,
		ChannelType: channelType.String(),
		Disabled:    disabled,
	})
	if err != nil {
		return gateway.ChannelConfig{}, mapConfigError(err)
	}
	return channelConfig(row)
}

func (s *Store) SaveMatrixSyncSinceToken(ctx context.Context, configID, since string) error {
	id, err := db.ParseUUID(configID)
	if err != nil {
		return err
	}
	rows, err := s.configs.SaveMatrixSyncSinceToken(ctx, channelsqlc.SaveMatrixSyncSinceTokenParams{
		ID:         id,
		SinceToken: strings.TrimSpace(since),
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return gateway.ErrChannelConfigNotFound
	}
	return nil
}

func (s *Store) FindConfig(ctx context.Context, botID string, channelType gateway.ChannelType) (gateway.ChannelConfig, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return gateway.ChannelConfig{}, err
	}
	row, err := s.configs.GetBotChannelConfig(ctx, channelsqlc.GetBotChannelConfigParams{
		BotID:       id,
		ChannelType: channelType.String(),
	})
	if err != nil {
		return gateway.ChannelConfig{}, mapConfigError(err)
	}
	return channelConfig(row)
}

func (s *Store) ListConfigsByType(ctx context.Context, channelType gateway.ChannelType) ([]gateway.ChannelConfig, error) {
	rows, err := s.configs.ListBotChannelConfigsByType(ctx, channelType.String())
	if err != nil {
		return nil, err
	}
	items := make([]gateway.ChannelConfig, 0, len(rows))
	for _, row := range rows {
		item, err := channelConfig(row)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func mapConfigError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return gateway.ErrChannelConfigNotFound
	}
	return err
}

func channelConfig(row channelsqlc.ChannelBotChannelConfig) (gateway.ChannelConfig, error) {
	credentials, err := gateway.DecodeConfigMap(row.Credentials)
	if err != nil {
		return gateway.ChannelConfig{}, err
	}
	selfIdentity, err := gateway.DecodeConfigMap(row.SelfIdentity)
	if err != nil {
		return gateway.ChannelConfig{}, err
	}
	routing, err := gateway.DecodeConfigMap(row.Routing)
	if err != nil {
		return gateway.ChannelConfig{}, err
	}
	return gateway.ChannelConfig{
		ID:               db.UUIDString(row.ID),
		TeamID:           db.UUIDString(row.TeamID),
		BotID:            db.UUIDString(row.BotID),
		ChannelType:      gateway.ChannelType(row.ChannelType),
		Credentials:      credentials,
		ExternalIdentity: strings.TrimSpace(row.ExternalIdentity.String),
		SelfIdentity:     selfIdentity,
		Routing:          routing,
		Disabled:         row.Disabled,
		VerifiedAt:       timestamp(row.VerifiedAt),
		CreatedAt:        timestamp(row.CreatedAt),
		UpdatedAt:        timestamp(row.UpdatedAt),
	}, nil
}
