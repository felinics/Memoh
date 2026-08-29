package timeline

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

// EventStore persists and loads CanonicalEvents from the database.
type EventStore struct {
	queries dbstore.Queries
	logger  *slog.Logger
}

// NewEventStore creates an EventStore.
func NewEventStore(log *slog.Logger, queries dbstore.Queries) *EventStore {
	if log == nil {
		log = slog.Default()
	}
	return &EventStore{
		queries: queries,
		logger:  log.With(slog.String("service", "chat/timeline_event_store")),
	}
}

// PersistEvent writes a CanonicalEvent to the bot_session_events table with a
// freshly allocated monotonic event cursor stamped into its payload. Returns
// the UUID of the persisted event row (empty for ON CONFLICT duplicates) and
// the stamped event the caller must project instead of the input.
func (s *EventStore) PersistEvent(ctx context.Context, botID, sessionID string, event CanonicalEvent) (string, CanonicalEvent, error) {
	pgBotID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		return "", event, fmt.Errorf("invalid bot id: %w", err)
	}
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return "", event, fmt.Errorf("invalid session id: %w", err)
	}

	original := event
	if cursor, cursorErr := s.queries.NextSessionEventCursor(ctx); cursorErr != nil {
		s.logger.Warn("allocate session event cursor failed", slog.Any("error", cursorErr))
	} else if stamped, stampErr := assignEventCursor(event, cursor); stampErr != nil {
		s.logger.Warn("stamp session event cursor failed", slog.Any("error", stampErr))
	} else {
		event = stamped
	}

	eventData, err := json.Marshal(event)
	if err != nil {
		return "", event, fmt.Errorf("marshal event data: %w", err)
	}

	externalMessageID := extractExternalMessageID(event)
	senderID := extractSenderChannelIdentityID(event)

	pgExternalMsgID := pgtype.Text{}
	if externalMessageID != "" {
		pgExternalMsgID = pgtype.Text{String: externalMessageID, Valid: true}
	}

	pgSenderID := pgtype.UUID{}
	if senderID != "" {
		if parsed, parseErr := dbpkg.ParseUUID(senderID); parseErr == nil {
			pgSenderID = parsed
		}
	}

	pgID, err := s.queries.CreateSessionEvent(ctx, sqlc.CreateSessionEventParams{
		BotID:                   pgBotID,
		SessionID:               pgSessionID,
		EventKind:               string(event.Kind()),
		EventData:               eventData,
		ExternalMessageID:       pgExternalMsgID,
		SenderChannelIdentityID: pgSenderID,
		ReceivedAtMs:            event.GetReceivedAtMs(),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", original, nil
		}
		return "", event, fmt.Errorf("persist session event: %w", err)
	}

	if pgID.Valid {
		return pgID.String(), event, nil
	}
	return "", original, nil
}

// LoadEvents loads all events for a session, ordered by received_at_ms.
func (s *EventStore) LoadEvents(ctx context.Context, sessionID string) ([]CanonicalEvent, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return nil, fmt.Errorf("invalid session id: %w", err)
	}

	rows, err := s.queries.ListSessionEventsBySession(ctx, pgSessionID)
	if err != nil {
		return nil, fmt.Errorf("list session events: %w", err)
	}

	events := make([]CanonicalEvent, 0, len(rows))
	for _, row := range rows {
		event, parseErr := parseEventData(row.EventKind, row.EventData)
		if parseErr != nil {
			s.logger.Warn("skip unparseable event",
				slog.String("session_id", sessionID),
				slog.String("event_id", row.ID.String()),
				slog.Any("error", parseErr))
			continue
		}
		events = append(events, event)
	}

	return events, nil
}

// HasEvents checks whether a session has any events persisted.
func (s *EventStore) HasEvents(ctx context.Context, sessionID string) (bool, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return false, fmt.Errorf("invalid session id: %w", err)
	}

	count, err := s.queries.CountSessionEvents(ctx, pgSessionID)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *EventStore) GetDiscussCursor(ctx context.Context, sessionID, scopeKey string) (DiscussCursorPosition, error) {
	if s == nil || s.queries == nil {
		return DiscussCursorPosition{}, nil
	}
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return DiscussCursorPosition{}, fmt.Errorf("invalid session id: %w", err)
	}
	row, err := s.queries.GetSessionDiscussCursor(ctx, sqlc.GetSessionDiscussCursorParams{
		SessionID: pgSessionID,
		ScopeKey:  normalizeDiscussCursorScope(scopeKey),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DiscussCursorPosition{}, nil
		}
		return DiscussCursorPosition{}, fmt.Errorf("get discuss cursor: %w", err)
	}
	return DiscussCursorPosition{
		SourceCursor: row.ConsumedCursor,
		EventCursor:  row.ConsumedEventCursor,
	}, nil
}

func (s *EventStore) UpsertDiscussCursor(ctx context.Context, sessionID, scopeKey, routeID, source string, position DiscussCursorPosition) error {
	if s == nil || s.queries == nil || (position.SourceCursor <= 0 && position.EventCursor <= 0) {
		return nil
	}
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return fmt.Errorf("invalid session id: %w", err)
	}
	pgRouteID := pgtype.UUID{}
	if strings.TrimSpace(routeID) != "" {
		parsed, parseErr := dbpkg.ParseUUID(routeID)
		if parseErr != nil {
			return fmt.Errorf("invalid route id: %w", parseErr)
		}
		pgRouteID = parsed
	}
	_, err = s.queries.UpsertSessionDiscussCursor(ctx, sqlc.UpsertSessionDiscussCursorParams{
		SessionID:           pgSessionID,
		ScopeKey:            normalizeDiscussCursorScope(scopeKey),
		RouteID:             pgRouteID,
		Source:              strings.TrimSpace(source),
		ConsumedCursor:      position.SourceCursor,
		ConsumedEventCursor: position.EventCursor,
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
		var e MessageEvent
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, err
		}
		return e, nil
	case EventEdit:
		var e EditEvent
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, err
		}
		return e, nil
	case EventDelete:
		var e DeleteEvent
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, err
		}
		return e, nil
	case EventService:
		var e ServiceEvent
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, err
		}
		return e, nil
	default:
		return nil, fmt.Errorf("unknown event kind: %s", kind)
	}
}

func extractExternalMessageID(event CanonicalEvent) string {
	switch e := event.(type) {
	case MessageEvent:
		return strings.TrimSpace(e.MessageID)
	case EditEvent:
		return strings.TrimSpace(e.MessageID)
	default:
		return ""
	}
}

func extractSenderChannelIdentityID(event CanonicalEvent) string {
	switch e := event.(type) {
	case MessageEvent:
		if e.Sender != nil {
			return strings.TrimSpace(e.Sender.ID)
		}
	case EditEvent:
		if e.Sender != nil {
			return strings.TrimSpace(e.Sender.ID)
		}
	case ServiceEvent:
		if e.Actor != nil {
			return strings.TrimSpace(e.Actor.ID)
		}
	}
	return ""
}
