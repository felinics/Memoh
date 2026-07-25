package assembly

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/domains/agent/chat/timeline"
	channelsqlc "github.com/memohai/memoh/domains/channel/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type timelineStore struct {
	queries *channelsqlc.Queries
}

func provideTimelineStore(pool *pgxpool.Pool) timeline.Store {
	return &timelineStore{queries: channelsqlc.New(pool)}
}

func (s *timelineStore) CreateEvent(ctx context.Context, record timeline.EventRecord) (string, error) {
	botID, err := db.ParseUUID(record.BotID)
	if err != nil {
		return "", fmt.Errorf("invalid bot id: %w", err)
	}
	sessionID, err := db.ParseUUID(record.SessionID)
	if err != nil {
		return "", fmt.Errorf("invalid session id: %w", err)
	}
	senderID, err := timelineOptionalUUID(record.SenderChannelIdentityID)
	if err != nil {
		return "", fmt.Errorf("invalid sender channel identity id: %w", err)
	}
	id, err := s.queries.CreateSessionEvent(ctx, channelsqlc.CreateSessionEventParams{
		BotID:                   botID,
		SessionID:               sessionID,
		EventKind:               string(record.Kind),
		EventData:               record.Data,
		ExternalMessageID:       timelineOptionalText(record.ExternalMessageID),
		SenderChannelIdentityID: senderID,
		ReceivedAtMs:            record.ReceivedAtMS,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return timelineUUIDString(id), nil
}

func (s *timelineStore) ListEvents(ctx context.Context, sessionID string) ([]timeline.StoredEvent, error) {
	id, err := db.ParseUUID(sessionID)
	if err != nil {
		return nil, fmt.Errorf("invalid session id: %w", err)
	}
	rows, err := s.queries.ListSessionEventsBySession(ctx, id)
	if err != nil {
		return nil, err
	}
	events := make([]timeline.StoredEvent, 0, len(rows))
	for _, row := range rows {
		events = append(events, timeline.StoredEvent{
			ID:   timelineUUIDString(row.ID),
			Kind: timeline.EventKind(row.EventKind),
			Data: append([]byte(nil), row.EventData...),
		})
	}
	return events, nil
}

func (s *timelineStore) CountEvents(ctx context.Context, sessionID string) (int64, error) {
	id, err := db.ParseUUID(sessionID)
	if err != nil {
		return 0, fmt.Errorf("invalid session id: %w", err)
	}
	return s.queries.CountSessionEvents(ctx, id)
}

func (s *timelineStore) GetDiscussCursor(ctx context.Context, sessionID, scopeKey string) (int64, error) {
	id, err := db.ParseUUID(sessionID)
	if err != nil {
		return 0, fmt.Errorf("invalid session id: %w", err)
	}
	row, err := s.queries.GetSessionDiscussCursor(ctx, channelsqlc.GetSessionDiscussCursorParams{
		SessionID: id,
		ScopeKey:  scopeKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return row.ConsumedCursor, nil
}

func (s *timelineStore) UpsertDiscussCursor(ctx context.Context, record timeline.DiscussCursorRecord) error {
	botID, err := db.ParseUUID(record.BotID)
	if err != nil {
		return fmt.Errorf("invalid bot id: %w", err)
	}
	sessionID, err := db.ParseUUID(record.SessionID)
	if err != nil {
		return fmt.Errorf("invalid session id: %w", err)
	}
	routeID, err := timelineOptionalUUID(record.RouteID)
	if err != nil {
		return fmt.Errorf("invalid route id: %w", err)
	}
	_, err = s.queries.UpsertSessionDiscussCursor(ctx, channelsqlc.UpsertSessionDiscussCursorParams{
		BotID:          botID,
		SessionID:      sessionID,
		ScopeKey:       record.ScopeKey,
		RouteID:        routeID,
		Source:         strings.TrimSpace(record.Source),
		ConsumedCursor: record.ConsumedCursor,
	})
	return err
}

func timelineOptionalUUID(value string) (pgtype.UUID, error) {
	if strings.TrimSpace(value) == "" {
		return pgtype.UUID{}, nil
	}
	return db.ParseUUID(value)
}

func timelineOptionalText(value string) pgtype.Text {
	value = strings.TrimSpace(value)
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func timelineUUIDString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return value.String()
}
