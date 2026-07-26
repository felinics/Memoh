// Package timeline implements Channel-owned timeline event persistence.
package timeline

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	agenttimeline "github.com/memohai/memoh/domains/agent/chat/timeline"
	channelsqlc "github.com/memohai/memoh/domains/channel/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

// New builds the Channel timeline store from a pool.
func New(pool *pgxpool.Pool) agenttimeline.Store {
	return &Store{queries: channelsqlc.New(pool)}
}

type Store struct {
	queries *channelsqlc.Queries
}

func (s *Store) CreateEvent(ctx context.Context, record agenttimeline.EventRecord) (string, error) {
	botID, err := db.ParseUUID(record.BotID)
	if err != nil {
		return "", fmt.Errorf("invalid bot id: %w", err)
	}
	sessionID, err := db.ParseUUID(record.SessionID)
	if err != nil {
		return "", fmt.Errorf("invalid session id: %w", err)
	}
	senderID, err := optionalUUID(record.SenderChannelIdentityID)
	if err != nil {
		return "", fmt.Errorf("invalid sender channel identity id: %w", err)
	}
	id, err := s.queries.CreateSessionEvent(ctx, channelsqlc.CreateSessionEventParams{
		BotID:                   botID,
		SessionID:               sessionID,
		EventKind:               string(record.Kind),
		EventData:               record.Data,
		ExternalMessageID:       optionalText(record.ExternalMessageID),
		SenderChannelIdentityID: senderID,
		ReceivedAtMs:            record.ReceivedAtMS,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return db.UUIDString(id), nil
}

func (s *Store) ListEvents(ctx context.Context, sessionID string) ([]agenttimeline.StoredEvent, error) {
	id, err := db.ParseUUID(sessionID)
	if err != nil {
		return nil, fmt.Errorf("invalid session id: %w", err)
	}
	rows, err := s.queries.ListSessionEventsBySession(ctx, id)
	if err != nil {
		return nil, err
	}
	events := make([]agenttimeline.StoredEvent, 0, len(rows))
	for _, row := range rows {
		events = append(events, agenttimeline.StoredEvent{
			ID:   db.UUIDString(row.ID),
			Kind: agenttimeline.EventKind(row.EventKind),
			Data: append([]byte(nil), row.EventData...),
		})
	}
	return events, nil
}

func (s *Store) CountEvents(ctx context.Context, sessionID string) (int64, error) {
	id, err := db.ParseUUID(sessionID)
	if err != nil {
		return 0, fmt.Errorf("invalid session id: %w", err)
	}
	return s.queries.CountSessionEvents(ctx, id)
}

func (s *Store) NextEventCursor(ctx context.Context) (int64, error) {
	return s.queries.NextSessionEventCursor(ctx)
}

func (s *Store) GetDiscussCursor(ctx context.Context, sessionID, scopeKey string) (agenttimeline.DiscussCursorPosition, error) {
	id, err := db.ParseUUID(sessionID)
	if err != nil {
		return agenttimeline.DiscussCursorPosition{}, fmt.Errorf("invalid session id: %w", err)
	}
	row, err := s.queries.GetSessionDiscussCursor(ctx, channelsqlc.GetSessionDiscussCursorParams{
		SessionID: id,
		ScopeKey:  scopeKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return agenttimeline.DiscussCursorPosition{}, nil
	}
	if err != nil {
		return agenttimeline.DiscussCursorPosition{}, err
	}
	return agenttimeline.DiscussCursorPosition{
		SourceCursor: row.ConsumedCursor,
		EventCursor:  row.ConsumedEventCursor,
	}, nil
}

func (s *Store) UpsertDiscussCursor(ctx context.Context, record agenttimeline.DiscussCursorRecord) error {
	botID, err := db.ParseUUID(record.BotID)
	if err != nil {
		return fmt.Errorf("invalid bot id: %w", err)
	}
	sessionID, err := db.ParseUUID(record.SessionID)
	if err != nil {
		return fmt.Errorf("invalid session id: %w", err)
	}
	routeID, err := optionalUUID(record.RouteID)
	if err != nil {
		return fmt.Errorf("invalid route id: %w", err)
	}
	_, err = s.queries.UpsertSessionDiscussCursor(ctx, channelsqlc.UpsertSessionDiscussCursorParams{
		BotID:               botID,
		SessionID:           sessionID,
		ScopeKey:            record.ScopeKey,
		RouteID:             routeID,
		Source:              strings.TrimSpace(record.Source),
		ConsumedCursor:      record.ConsumedCursor,
		ConsumedEventCursor: record.ConsumedEventCursor,
	})
	return err
}

func optionalUUID(value string) (pgtype.UUID, error) {
	if strings.TrimSpace(value) == "" {
		return pgtype.UUID{}, nil
	}
	return db.ParseUUID(value)
}

func optionalText(value string) pgtype.Text {
	value = strings.TrimSpace(value)
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

// provideCompactionArtifacts exposes the agent-owned artifact frontier to the
// discuss driver. Split-mode channel runs in its own process, so it binds the
// store directly instead of reaching through the server's composition root.
