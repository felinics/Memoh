package timeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// EventStore persists and loads canonical timeline events.
type EventStore struct {
	store  Store
	logger *slog.Logger
}

// NewEventStore creates an EventStore.
func NewEventStore(log *slog.Logger, store Store) *EventStore {
	if log == nil {
		log = slog.Default()
	}
	return &EventStore{
		store:  store,
		logger: log.With(slog.String("service", "chat/timeline_event_store")),
	}
}

// PersistEvent writes a CanonicalEvent. Duplicate events return an empty ID.
func (s *EventStore) PersistEvent(ctx context.Context, botID, sessionID string, event CanonicalEvent) (string, error) {
	eventData, err := json.Marshal(event)
	if err != nil {
		return "", fmt.Errorf("marshal event data: %w", err)
	}
	id, err := s.store.CreateEvent(ctx, EventRecord{
		BotID:                   botID,
		SessionID:               sessionID,
		Kind:                    event.Kind(),
		Data:                    eventData,
		ExternalMessageID:       extractExternalMessageID(event),
		SenderChannelIdentityID: extractSenderChannelIdentityID(event),
		ReceivedAtMS:            event.GetReceivedAtMs(),
	})
	if err != nil {
		return "", fmt.Errorf("persist session event: %w", err)
	}
	return id, nil
}

// LoadEvents loads all events for a session in persistence order.
func (s *EventStore) LoadEvents(ctx context.Context, sessionID string) ([]CanonicalEvent, error) {
	rows, err := s.store.ListEvents(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list session events: %w", err)
	}
	events := make([]CanonicalEvent, 0, len(rows))
	for _, row := range rows {
		event, parseErr := parseEventData(string(row.Kind), row.Data)
		if parseErr != nil {
			s.logger.WarnContext(ctx, "skip unparseable event",
				slog.String("session_id", sessionID),
				slog.String("event_id", row.ID),
				slog.Any("error", parseErr))
			continue
		}
		events = append(events, event)
	}
	return events, nil
}

// HasEvents reports whether a session has persisted events.
func (s *EventStore) HasEvents(ctx context.Context, sessionID string) (bool, error) {
	count, err := s.store.CountEvents(ctx, sessionID)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *EventStore) GetDiscussConsumedCursor(ctx context.Context, sessionID, scopeKey string) (int64, error) {
	if s == nil || s.store == nil {
		return 0, nil
	}
	cursor, err := s.store.GetDiscussCursor(ctx, sessionID, normalizeDiscussCursorScope(scopeKey))
	if err != nil {
		return 0, fmt.Errorf("get discuss cursor: %w", err)
	}
	return cursor, nil
}

func (s *EventStore) UpsertDiscussConsumedCursor(ctx context.Context, botID, sessionID, scopeKey, routeID, source string, cursor int64) error {
	if s == nil || s.store == nil || cursor <= 0 {
		return nil
	}
	err := s.store.UpsertDiscussCursor(ctx, DiscussCursorRecord{
		BotID:          botID,
		SessionID:      sessionID,
		ScopeKey:       normalizeDiscussCursorScope(scopeKey),
		RouteID:        routeID,
		Source:         strings.TrimSpace(source),
		ConsumedCursor: cursor,
	})
	if err != nil {
		return fmt.Errorf("upsert discuss cursor: %w", err)
	}
	return nil
}

func normalizeDiscussCursorScope(scopeKey string) string {
	if strings.TrimSpace(scopeKey) == "" {
		return "default"
	}
	return strings.TrimSpace(scopeKey)
}

func parseEventData(kind string, data []byte) (CanonicalEvent, error) {
	switch EventKind(kind) {
	case EventMessage:
		var event MessageEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return nil, err
		}
		return event, nil
	case EventEdit:
		var event EditEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return nil, err
		}
		return event, nil
	case EventDelete:
		var event DeleteEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return nil, err
		}
		return event, nil
	case EventService:
		var event ServiceEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return nil, err
		}
		return event, nil
	default:
		return nil, fmt.Errorf("unknown event kind: %s", kind)
	}
}

func extractExternalMessageID(event CanonicalEvent) string {
	switch event := event.(type) {
	case MessageEvent:
		return strings.TrimSpace(event.MessageID)
	case EditEvent:
		return strings.TrimSpace(event.MessageID)
	default:
		return ""
	}
}

func extractSenderChannelIdentityID(event CanonicalEvent) string {
	switch event := event.(type) {
	case MessageEvent:
		if event.Sender != nil {
			return strings.TrimSpace(event.Sender.ID)
		}
	case EditEvent:
		if event.Sender != nil {
			return strings.TrimSpace(event.Sender.ID)
		}
	case ServiceEvent:
		if event.Actor != nil {
			return strings.TrimSpace(event.Actor.ID)
		}
	}
	return ""
}
