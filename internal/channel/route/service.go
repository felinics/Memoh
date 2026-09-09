package route

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// DBService manages channel routes and route-to-bot/thread resolution.
type DBService struct {
	queries dbstore.Queries
	logger  *slog.Logger
}

// NewService creates a channel route service.
func NewService(log *slog.Logger, queries dbstore.Queries) *DBService {
	if log == nil {
		log = slog.Default()
	}
	return &DBService{
		queries: queries,
		logger:  log.With(slog.String("service", "channel/route")),
	}
}

// Create creates a route.
func (s *DBService) Create(ctx context.Context, input CreateInput) (Route, error) {
	pgBotID, err := dbpkg.ParseUUID(input.BotID)
	if err != nil {
		return Route{}, err
	}
	var pgConfigID pgtype.UUID
	if strings.TrimSpace(input.ChannelConfigID) != "" {
		pgConfigID, err = dbpkg.ParseUUID(input.ChannelConfigID)
		if err != nil {
			return Route{}, err
		}
	}
	metadata, err := json.Marshal(nonNilMap(input.Metadata))
	if err != nil {
		return Route{}, fmt.Errorf("marshal route metadata: %w", err)
	}

	row, err := s.queries.CreateChatRoute(ctx, sqlc.CreateChatRouteParams{
		BotID:            pgBotID,
		Platform:         input.Platform,
		ChannelConfigID:  pgConfigID,
		ConversationID:   input.ExternalConversationID,
		ThreadID:         toPgText(input.ExternalThreadID),
		ConversationType: toPgText(input.ConversationType),
		ReplyTarget:      toPgText(input.ReplyTarget),
		Metadata:         metadata,
	})
	if err != nil {
		return Route{}, fmt.Errorf("create route: %w", err)
	}

	return toRouteFromCreate(row), nil
}

// Find finds a route by bot/platform/external-conversation/thread.
func (s *DBService) Find(ctx context.Context, botID, platform, externalConversationID, externalThreadID string) (Route, error) {
	pgBotID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		return Route{}, err
	}
	row, err := s.queries.FindChatRoute(ctx, sqlc.FindChatRouteParams{
		BotID:          pgBotID,
		Platform:       platform,
		ConversationID: externalConversationID,
		ThreadID:       toPgText(externalThreadID),
	})
	if err != nil {
		return Route{}, err
	}
	return toRouteFromFind(row), nil
}

// GetByID gets a route by ID.
func (s *DBService) GetByID(ctx context.Context, routeID string) (Route, error) {
	pgID, err := dbpkg.ParseUUID(routeID)
	if err != nil {
		return Route{}, err
	}
	row, err := s.queries.GetChatRouteByID(ctx, pgID)
	if err != nil {
		return Route{}, err
	}
	return toRouteFromGet(row), nil
}

// List lists all routes for a bot.
func (s *DBService) List(ctx context.Context, botID string) ([]Route, error) {
	pgID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListChatRoutes(ctx, pgID)
	if err != nil {
		return nil, err
	}
	routes := make([]Route, 0, len(rows))
	for _, row := range rows {
		routes = append(routes, toRouteFromList(row))
	}
	return routes, nil
}

// Delete deletes a route by ID.
func (s *DBService) Delete(ctx context.Context, routeID string) error {
	pgID, err := dbpkg.ParseUUID(routeID)
	if err != nil {
		return err
	}
	return s.queries.DeleteChatRoute(ctx, pgID)
}

// UpdateReplyTarget updates default reply target.
func (s *DBService) UpdateReplyTarget(ctx context.Context, routeID, replyTarget string) error {
	pgID, err := dbpkg.ParseUUID(routeID)
	if err != nil {
		return err
	}
	return s.queries.UpdateChatRouteReplyTarget(ctx, sqlc.UpdateChatRouteReplyTargetParams{
		ID:          pgID,
		ReplyTarget: toPgText(replyTarget),
	})
}

// UpdateMetadata replaces the route metadata.
func (s *DBService) UpdateMetadata(ctx context.Context, routeID string, metadata map[string]any) error {
	pgID, err := dbpkg.ParseUUID(routeID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(nonNilMap(metadata))
	if err != nil {
		return fmt.Errorf("marshal route metadata: %w", err)
	}
	return s.queries.UpdateChatRouteMetadata(ctx, sqlc.UpdateChatRouteMetadataParams{
		ID:       pgID,
		Metadata: data,
	})
}

// ResolveConversation finds or creates a bot route for an inbound message.
func (s *DBService) ResolveConversation(ctx context.Context, input ResolveInput) (ResolveConversationResult, error) {
	route, err := s.Find(ctx, input.BotID, input.Platform, input.ExternalConversationID, input.ExternalThreadID)
	if err == nil {
		if strings.TrimSpace(input.ReplyTarget) != "" && input.ReplyTarget != route.ReplyTarget {
			if updateErr := s.UpdateReplyTarget(ctx, route.ID, input.ReplyTarget); updateErr != nil && s.logger != nil {
				s.logger.Warn("update route reply target failed", slog.Any("error", updateErr))
			}
		}
		if len(input.Metadata) > 0 && metadataChanged(route.Metadata, input.Metadata) {
			merged := mergeMetadata(route.Metadata, input.Metadata)
			if updateErr := s.UpdateMetadata(ctx, route.ID, merged); updateErr != nil && s.logger != nil {
				s.logger.Warn("update route metadata failed", slog.Any("error", updateErr))
			}
		}
		pgBotID, parseErr := dbpkg.ParseUUID(route.BotID)
		if parseErr != nil {
			return ResolveConversationResult{}, fmt.Errorf("parse route bot id: %w", parseErr)
		}
		if touchErr := s.queries.TouchBotActivity(ctx, pgBotID); touchErr != nil && s.logger != nil {
			s.logger.Warn("touch bot activity failed", slog.Any("error", touchErr))
		}
		return ResolveConversationResult{BotID: route.BotID, RouteID: route.ID, Created: false}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ResolveConversationResult{}, fmt.Errorf("find route: %w", err)
	}

	pgBotID, err := dbpkg.ParseUUID(input.BotID)
	if err != nil {
		return ResolveConversationResult{}, fmt.Errorf("parse route bot id: %w", err)
	}
	if _, err := s.queries.GetBotByID(ctx, pgBotID); err != nil {
		return ResolveConversationResult{}, fmt.Errorf("get route bot: %w", err)
	}

	newRoute, err := s.Create(ctx, CreateInput{
		BotID:                  input.BotID,
		Platform:               input.Platform,
		ChannelConfigID:        input.ChannelConfigID,
		ExternalConversationID: input.ExternalConversationID,
		ExternalThreadID:       input.ExternalThreadID,
		ConversationType:       input.ConversationType,
		ReplyTarget:            input.ReplyTarget,
		Metadata:               input.Metadata,
	})
	if err != nil {
		// Concurrent insert race: another goroutine created the same route between
		// our Find and Create calls. Fall back to Find the winning row.
		if dbpkg.IsUniqueViolation(err) {
			existing, findErr := s.Find(ctx, input.BotID, input.Platform, input.ExternalConversationID, input.ExternalThreadID)
			if findErr == nil {
				return ResolveConversationResult{BotID: existing.BotID, RouteID: existing.ID, Created: false}, nil
			}
		}
		return ResolveConversationResult{}, fmt.Errorf("create route: %w", err)
	}

	return ResolveConversationResult{BotID: newRoute.BotID, RouteID: newRoute.ID, Created: true}, nil
}

func toRouteFromCreate(row sqlc.CreateChatRouteRow) Route {
	return toRouteFields(
		row.ID, row.BotID, row.Platform, row.ChannelConfigID,
		row.ConversationID, row.ThreadID, row.ConversationType, row.ReplyTarget,
		row.ActiveSessionID, row.Metadata, row.CreatedAt, row.UpdatedAt,
	)
}

func toRouteFromFind(row sqlc.FindChatRouteRow) Route {
	return toRouteFields(
		row.ID, row.BotID, row.Platform, row.ChannelConfigID,
		row.ConversationID, row.ThreadID, row.ConversationType, row.ReplyTarget,
		row.ActiveSessionID, row.Metadata, row.CreatedAt, row.UpdatedAt,
	)
}

func toRouteFromGet(row sqlc.GetChatRouteByIDRow) Route {
	return toRouteFields(
		row.ID, row.BotID, row.Platform, row.ChannelConfigID,
		row.ConversationID, row.ThreadID, row.ConversationType, row.ReplyTarget,
		row.ActiveSessionID, row.Metadata, row.CreatedAt, row.UpdatedAt,
	)
}

func toRouteFromList(row sqlc.ListChatRoutesRow) Route {
	return toRouteFields(
		row.ID, row.BotID, row.Platform, row.ChannelConfigID,
		row.ConversationID, row.ThreadID, row.ConversationType, row.ReplyTarget,
		row.ActiveSessionID, row.Metadata, row.CreatedAt, row.UpdatedAt,
	)
}

func toRouteFields(id, botID pgtype.UUID, platform string, channelConfigID pgtype.UUID, externalConversationID string, externalThreadID, conversationType, replyTarget pgtype.Text, activeThreadID pgtype.UUID, metadata []byte, createdAt, updatedAt pgtype.Timestamptz) Route {
	return Route{
		ID:                     id.String(),
		BotID:                  botID.String(),
		Platform:               platform,
		ChannelConfigID:        channelConfigID.String(),
		ExternalConversationID: externalConversationID,
		ExternalThreadID:       dbpkg.TextToString(externalThreadID),
		ConversationType:       dbpkg.TextToString(conversationType),
		ReplyTarget:            dbpkg.TextToString(replyTarget),
		ActiveThreadID:         activeThreadID.String(),
		Metadata:               parseJSONMap(metadata),
		CreatedAt:              createdAt.Time,
		UpdatedAt:              updatedAt.Time,
	}
}

func toPgText(value string) pgtype.Text {
	value = strings.TrimSpace(value)
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func nonNilMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func parseJSONMap(data []byte) map[string]any {
	if len(data) == 0 {
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	return m
}

// metadataChanged returns true when any key in incoming differs from existing.
func metadataChanged(existing, incoming map[string]any) bool {
	for k, v := range incoming {
		old, ok := existing[k]
		if !ok {
			return true
		}
		oldJSON, _ := json.Marshal(old)
		newJSON, _ := json.Marshal(v)
		if string(oldJSON) != string(newJSON) {
			return true
		}
	}
	return false
}

// mergeMetadata merges incoming keys into existing, preserving keys not in incoming.
func mergeMetadata(existing, incoming map[string]any) map[string]any {
	merged := make(map[string]any, len(existing)+len(incoming))
	for k, v := range existing {
		merged[k] = v
	}
	for k, v := range incoming {
		merged[k] = v
	}
	return merged
}
