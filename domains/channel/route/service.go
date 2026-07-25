package route

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// Manager provides channel route management and conversation resolution.
type Manager struct {
	store  Store
	logger *slog.Logger
}

// DBService is retained as an API-compatible name for Manager.
type DBService = Manager

// NewService creates a channel route service.
func NewService(log *slog.Logger, store Store) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		store:  store,
		logger: log.With(slog.String("service", "channel/route")),
	}
}

// Create creates a route.
func (s *Manager) Create(ctx context.Context, input CreateInput) (Route, error) {
	row, err := s.store.CreateRoute(ctx, input)
	if err != nil {
		return Route{}, fmt.Errorf("create route: %w", err)
	}
	return row, nil
}

// Find finds a route by bot/platform/external-conversation/thread.
func (s *Manager) Find(ctx context.Context, botID, platform, externalConversationID, externalThreadID string) (Route, error) {
	return s.store.FindRoute(ctx, botID, platform, externalConversationID, externalThreadID)
}

// GetByID gets a route by ID.
func (s *Manager) GetByID(ctx context.Context, routeID string) (Route, error) {
	return s.store.FindRouteByID(ctx, routeID)
}

// List lists all routes for a bot.
func (s *Manager) List(ctx context.Context, botID string) ([]Route, error) {
	return s.store.ListRoutes(ctx, botID)
}

// Delete deletes a route by ID.
func (s *Manager) Delete(ctx context.Context, routeID string) error {
	return s.store.DeleteRoute(ctx, routeID)
}

// UpdateReplyTarget updates default reply target.
func (s *Manager) UpdateReplyTarget(ctx context.Context, routeID, replyTarget string) error {
	return s.store.SetReplyTarget(ctx, routeID, replyTarget)
}

// UpdateMetadata replaces the route metadata.
func (s *Manager) UpdateMetadata(ctx context.Context, routeID string, metadata map[string]any) error {
	return s.store.SetMetadata(ctx, routeID, nonNilMap(metadata))
}

// ResolveConversation finds or creates a bot route for an inbound message.
func (s *Manager) ResolveConversation(ctx context.Context, input ResolveInput) (ResolveConversationResult, error) {
	route, err := s.Find(ctx, input.BotID, input.Platform, input.ExternalConversationID, input.ExternalThreadID)
	if err == nil {
		if strings.TrimSpace(input.ReplyTarget) != "" && input.ReplyTarget != route.ReplyTarget {
			if updateErr := s.UpdateReplyTarget(ctx, route.ID, input.ReplyTarget); updateErr != nil && s.logger != nil {
				s.logger.WarnContext(ctx, "update route reply target failed", slog.Any("error", updateErr))
			}
		}
		if len(input.Metadata) > 0 && metadataChanged(route.Metadata, input.Metadata) {
			merged := mergeMetadata(route.Metadata, input.Metadata)
			if updateErr := s.UpdateMetadata(ctx, route.ID, merged); updateErr != nil && s.logger != nil {
				s.logger.WarnContext(ctx, "update route metadata failed", slog.Any("error", updateErr))
			}
		}
		if touchErr := s.store.TouchBot(ctx, route.BotID); touchErr != nil && s.logger != nil {
			s.logger.WarnContext(ctx, "touch bot activity failed", slog.Any("error", touchErr))
		}
		return ResolveConversationResult{BotID: route.BotID, RouteID: route.ID, Created: false}, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return ResolveConversationResult{}, fmt.Errorf("find route: %w", err)
	}

	if err := s.store.EnsureBot(ctx, input.BotID); err != nil {
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
		if errors.Is(err, ErrConflict) {
			existing, findErr := s.Find(ctx, input.BotID, input.Platform, input.ExternalConversationID, input.ExternalThreadID)
			if findErr == nil {
				return ResolveConversationResult{BotID: existing.BotID, RouteID: existing.ID, Created: false}, nil
			}
		}
		return ResolveConversationResult{}, fmt.Errorf("create route: %w", err)
	}

	return ResolveConversationResult{BotID: newRoute.BotID, RouteID: newRoute.ID, Created: true}, nil
}

func nonNilMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
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
