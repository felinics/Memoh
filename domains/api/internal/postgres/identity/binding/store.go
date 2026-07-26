// Package binding implements API-owned outbound delivery bindings against
// the Channel consumer port.
package binding

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	apisqlc "github.com/memohai/memoh/domains/api/internal/postgres/sqlc"
	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/internal/db"
)

type channelIdentityBindingStore struct {
	queries *apisqlc.Queries
}

// NewStore adapts API-owned outbound delivery bindings to the Channel
// consumer port.
func NewStore(pool *pgxpool.Pool) gateway.IdentityBindingStore {
	return &channelIdentityBindingStore{queries: apisqlc.New(pool)}
}

func (s *channelIdentityBindingStore) UpsertIdentityBinding(ctx context.Context, input gateway.IdentityBindingWrite) (gateway.ChannelIdentityBinding, error) {
	identityID, err := db.ParseUUID(input.ChannelIdentityID)
	if err != nil {
		return gateway.ChannelIdentityBinding{}, err
	}
	config, err := json.Marshal(input.Config)
	if err != nil {
		return gateway.ChannelIdentityBinding{}, err
	}
	row, err := s.queries.UpsertUserChannelBinding(ctx, apisqlc.UpsertUserChannelBindingParams{
		UserID:      identityID,
		ChannelType: input.ChannelType.String(),
		Config:      config,
	})
	if err != nil {
		return gateway.ChannelIdentityBinding{}, err
	}
	return channelIdentityBinding(row)
}

func (s *channelIdentityBindingStore) FindIdentityBinding(ctx context.Context, identityID string, channelType gateway.ChannelType) (gateway.ChannelIdentityBinding, error) {
	id, err := db.ParseUUID(identityID)
	if err != nil {
		return gateway.ChannelIdentityBinding{}, err
	}
	row, err := s.queries.GetUserChannelBinding(ctx, apisqlc.GetUserChannelBindingParams{
		UserID:      id,
		ChannelType: channelType.String(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return gateway.ChannelIdentityBinding{}, gateway.ErrChannelIdentityConfigNotFound
	}
	if err != nil {
		return gateway.ChannelIdentityBinding{}, err
	}
	return channelIdentityBinding(row)
}

func (s *channelIdentityBindingStore) ListIdentityBindingsByType(ctx context.Context, channelType gateway.ChannelType) ([]gateway.ChannelIdentityBinding, error) {
	rows, err := s.queries.ListUserChannelBindingsByPlatform(ctx, channelType.String())
	if err != nil {
		return nil, err
	}
	items := make([]gateway.ChannelIdentityBinding, 0, len(rows))
	for _, row := range rows {
		item, err := channelIdentityBinding(row)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func channelIdentityBinding(row apisqlc.ApiUserChannelBinding) (gateway.ChannelIdentityBinding, error) {
	config, err := gateway.DecodeConfigMap(row.Config)
	if err != nil {
		return gateway.ChannelIdentityBinding{}, err
	}
	return gateway.ChannelIdentityBinding{
		ID:                bindingUUIDString(row.ID),
		ChannelType:       gateway.ChannelType(row.ChannelType),
		ChannelIdentityID: bindingUUIDString(row.UserID),
		Config:            config,
		CreatedAt:         bindingTimestamp(row.CreatedAt),
		UpdatedAt:         bindingTimestamp(row.UpdatedAt),
	}, nil
}

func bindingUUIDString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return value.String()
}

func bindingTimestamp(value pgtype.Timestamptz) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time
}
