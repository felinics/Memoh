package gateway

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/domains/channel"
	channelsqlc "github.com/memohai/memoh/domains/channel/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type conversationProjectionQueries interface {
	ListConversationProjections(context.Context, channelsqlc.ListConversationProjectionsParams) ([]channelsqlc.ListConversationProjectionsRow, error)
}

func (s *Store) ListConversationProjections(ctx context.Context, request channel.ConversationProjectionRequest) ([]channel.ConversationProjection, error) {
	botID, err := db.ParseUUID(request.BotID)
	if err != nil {
		return nil, err
	}
	if len(request.RouteIDs) == 0 {
		return []channel.ConversationProjection{}, nil
	}

	routeIDs := make([]pgtype.UUID, 0, len(request.RouteIDs))
	for _, routeID := range request.RouteIDs {
		id, err := db.ParseUUID(routeID)
		if err != nil {
			return nil, err
		}
		routeIDs = append(routeIDs, id)
	}

	rows, err := s.projections.ListConversationProjections(ctx, channelsqlc.ListConversationProjectionsParams{
		BotID:       botID,
		RouteIds:    routeIDs,
		ChannelType: optionalText(request.ChannelType),
	})
	if err != nil {
		return nil, err
	}
	items := make([]channel.ConversationProjection, 0, len(rows))
	for _, row := range rows {
		items = append(items, channel.ConversationProjection{
			RouteID:               db.UUIDString(row.RouteID),
			Channel:               strings.TrimSpace(row.Channel),
			ConversationType:      strings.TrimSpace(row.ConversationType),
			ConversationID:        strings.TrimSpace(row.ConversationID),
			ThreadID:              strings.TrimSpace(row.ThreadID),
			ConversationName:      strings.TrimSpace(row.ConversationName),
			ConversationAvatarURL: strings.TrimSpace(row.ConversationAvatarUrl),
		})
	}
	return items, nil
}
