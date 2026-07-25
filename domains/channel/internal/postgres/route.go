package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	channelsqlc "github.com/memohai/memoh/domains/channel/internal/postgres/sqlc"
	"github.com/memohai/memoh/domains/channel/route"
	"github.com/memohai/memoh/internal/db"
)

// Owner-local channel.bot_channel_routes statements (no cross-schema JOIN).
type routeQueries interface {
	CreateChatRoute(context.Context, channelsqlc.CreateChatRouteParams) (channelsqlc.CreateChatRouteRow, error)
	FindChatRoute(context.Context, channelsqlc.FindChatRouteParams) (channelsqlc.FindChatRouteRow, error)
	GetChatRouteByID(context.Context, pgtype.UUID) (channelsqlc.GetChatRouteByIDRow, error)
	ListChatRoutes(context.Context, pgtype.UUID) ([]channelsqlc.ListChatRoutesRow, error)
	ListChatRouteThreadProjectionsByIDs(context.Context, channelsqlc.ListChatRouteThreadProjectionsByIDsParams) ([]channelsqlc.ListChatRouteThreadProjectionsByIDsRow, error)
	UpdateChatRouteReplyTarget(context.Context, channelsqlc.UpdateChatRouteReplyTargetParams) error
	UpdateChatRouteMetadata(context.Context, channelsqlc.UpdateChatRouteMetadataParams) error
}

type routeSessionCoordinator interface {
	WithLockedRouteSessions(context.Context, string, func(pgx.Tx) error) error
	WithLockedSession(context.Context, string, func(pgx.Tx) error) error
}

func (s *Store) CreateRoute(ctx context.Context, input route.CreateInput) (route.Route, error) {
	botID, err := db.ParseUUID(input.BotID)
	if err != nil {
		return route.Route{}, err
	}
	configID, err := optionalUUID(input.ChannelConfigID)
	if err != nil {
		return route.Route{}, err
	}
	metadata, err := marshalMap(input.Metadata)
	if err != nil {
		return route.Route{}, fmt.Errorf("marshal route metadata: %w", err)
	}
	row, err := s.routes.CreateChatRoute(ctx, channelsqlc.CreateChatRouteParams{
		BotID:            botID,
		Platform:         input.Platform,
		ChannelConfigID:  configID,
		ConversationID:   input.ExternalConversationID,
		ThreadID:         optionalText(input.ExternalThreadID),
		ConversationType: optionalText(input.ConversationType),
		ReplyTarget:      optionalText(input.ReplyTarget),
		Metadata:         metadata,
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return route.Route{}, route.ErrConflict
		}
		return route.Route{}, err
	}
	return routeRecord(
		row.ID, row.BotID, row.Platform, row.ChannelConfigID,
		row.ConversationID, row.ThreadID, row.ConversationType, row.ReplyTarget,
		row.ActiveSessionID, row.Metadata, row.CreatedAt, row.UpdatedAt,
	), nil
}

func (s *Store) FindRoute(ctx context.Context, botID, platform, conversationID, threadID string) (route.Route, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return route.Route{}, err
	}
	row, err := s.routes.FindChatRoute(ctx, channelsqlc.FindChatRouteParams{
		BotID:          id,
		Platform:       platform,
		ConversationID: conversationID,
		ThreadID:       optionalText(threadID),
	})
	if err != nil {
		return route.Route{}, mapRouteError(err)
	}
	return routeRecord(
		row.ID, row.BotID, row.Platform, row.ChannelConfigID,
		row.ConversationID, row.ThreadID, row.ConversationType, row.ReplyTarget,
		row.ActiveSessionID, row.Metadata, row.CreatedAt, row.UpdatedAt,
	), nil
}

func (s *Store) FindRouteByID(ctx context.Context, routeID string) (route.Route, error) {
	id, err := db.ParseUUID(routeID)
	if err != nil {
		return route.Route{}, err
	}
	row, err := s.routes.GetChatRouteByID(ctx, id)
	if err != nil {
		return route.Route{}, mapRouteError(err)
	}
	return routeRecord(
		row.ID, row.BotID, row.Platform, row.ChannelConfigID,
		row.ConversationID, row.ThreadID, row.ConversationType, row.ReplyTarget,
		row.ActiveSessionID, row.Metadata, row.CreatedAt, row.UpdatedAt,
	), nil
}

func (s *Store) ListRoutes(ctx context.Context, botID string) ([]route.Route, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.routes.ListChatRoutes(ctx, id)
	if err != nil {
		return nil, err
	}
	items := make([]route.Route, 0, len(rows))
	for _, row := range rows {
		items = append(items, routeRecord(
			row.ID, row.BotID, row.Platform, row.ChannelConfigID,
			row.ConversationID, row.ThreadID, row.ConversationType, row.ReplyTarget,
			row.ActiveSessionID, row.Metadata, row.CreatedAt, row.UpdatedAt,
		))
	}
	return items, nil
}

func (s *Store) DeleteRoute(ctx context.Context, routeID string) error {
	id, err := db.ParseUUID(routeID)
	if err != nil {
		return err
	}
	return s.routeSessions.WithLockedRouteSessions(ctx, routeID, func(tx pgx.Tx) error {
		return channelsqlc.New(tx).DeleteChatRoute(ctx, id)
	})
}

func (s *Store) SetReplyTarget(ctx context.Context, routeID, replyTarget string) error {
	id, err := db.ParseUUID(routeID)
	if err != nil {
		return err
	}
	return s.routes.UpdateChatRouteReplyTarget(ctx, channelsqlc.UpdateChatRouteReplyTargetParams{
		ID:          id,
		ReplyTarget: optionalText(replyTarget),
	})
}

func (s *Store) SetMetadata(ctx context.Context, routeID string, metadata map[string]any) error {
	id, err := db.ParseUUID(routeID)
	if err != nil {
		return err
	}
	data, err := marshalMap(metadata)
	if err != nil {
		return fmt.Errorf("marshal route metadata: %w", err)
	}
	return s.routes.UpdateChatRouteMetadata(ctx, channelsqlc.UpdateChatRouteMetadataParams{
		ID:       id,
		Metadata: data,
	})
}

func (s *Store) SetActiveThread(ctx context.Context, routeID, threadID string) error {
	id, err := db.ParseUUID(routeID)
	if err != nil {
		return fmt.Errorf("invalid route id: %w", err)
	}
	activeThreadID, err := optionalUUID(threadID)
	if err != nil {
		return fmt.Errorf("invalid session id: %w", err)
	}
	return s.routeSessions.WithLockedSession(ctx, threadID, func(tx pgx.Tx) error {
		return channelsqlc.New(tx).SetRouteActiveSession(ctx, channelsqlc.SetRouteActiveSessionParams{
			ID:              id,
			ActiveSessionID: activeThreadID,
		})
	})
}

func (s *Store) TouchBot(ctx context.Context, botID string) error {
	return s.bots.TouchBot(ctx, botID)
}

func (s *Store) EnsureBot(ctx context.Context, botID string) error {
	return s.bots.EnsureBot(ctx, botID)
}

func mapRouteError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return route.ErrNotFound
	}
	return err
}

func routeRecord(
	id, botID pgtype.UUID,
	platform string,
	channelConfigID pgtype.UUID,
	conversationID string,
	threadID, conversationType, replyTarget pgtype.Text,
	activeThreadID pgtype.UUID,
	metadata []byte,
	createdAt, updatedAt pgtype.Timestamptz,
) route.Route {
	return route.Route{
		ID:                     uuidString(id),
		BotID:                  uuidString(botID),
		Platform:               platform,
		ChannelConfigID:        uuidString(channelConfigID),
		ExternalConversationID: conversationID,
		ExternalThreadID:       threadID.String,
		ConversationType:       conversationType.String,
		ReplyTarget:            replyTarget.String,
		ActiveThreadID:         uuidString(activeThreadID),
		Metadata:               decodeMap(metadata),
		CreatedAt:              timestamp(createdAt),
		UpdatedAt:              timestamp(updatedAt),
	}
}

// ListRouteThreadProjections fetches only the routes the caller references.
// Thread listings bind a handful of routes out of a bot's full set, so
// projecting by id keeps the query proportional to the page instead of the bot.
func (s *Store) ListRouteThreadProjections(ctx context.Context, botID string, routeIDs []string) ([]route.ThreadProjection, error) {
	if len(routeIDs) == 0 {
		return nil, nil
	}
	bot, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	ids := make([]pgtype.UUID, 0, len(routeIDs))
	for _, id := range routeIDs {
		parsed, parseErr := db.ParseUUID(id)
		if parseErr != nil {
			continue
		}
		ids = append(ids, parsed)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.routes.ListChatRouteThreadProjectionsByIDs(ctx, channelsqlc.ListChatRouteThreadProjectionsByIDsParams{
		BotID:    bot,
		RouteIds: ids,
	})
	if err != nil {
		return nil, err
	}
	items := make([]route.ThreadProjection, 0, len(rows))
	for _, row := range rows {
		items = append(items, route.ThreadProjection{
			RouteID:          uuidString(row.ID),
			ConversationType: row.ConversationType.String,
			Metadata:         decodeMap(row.Metadata),
		})
	}
	return items, nil
}
