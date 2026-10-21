package message

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// ListRecentUserMessagesBySession returns the newest user messages of a
// session, newest first, capped by maxMessages. It backs memory-query
// building, which needs the latest few user turns rather than a windowed
// history load (Context Epoch D1). The query projects id/content/created_at
// only and is bounded by its LIMIT (CM-ADM-001).
func (s *DBService) ListRecentUserMessagesBySession(ctx context.Context, sessionID string, maxMessages int32) ([]Message, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListRecentActiveUserMessagesBySession(ctx, sqlc.ListRecentActiveUserMessagesBySessionParams{
		SessionID:   pgSessionID,
		MaxMessages: maxMessages,
	})
	if err != nil {
		return nil, err
	}
	return toMessagesFromRecentUserRows(rows), nil
}

func toMessagesFromRecentUserRows(rows []sqlc.ListRecentActiveUserMessagesBySessionRow) []Message {
	messages := make([]Message, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, recentUserMessage(row.ID, row.Content, row.CreatedAt))
	}
	return messages
}

func recentUserMessage(id pgtype.UUID, content []byte, createdAt pgtype.Timestamptz) Message {
	return Message{
		ID:        id.String(),
		Role:      "user",
		Content:   content,
		CreatedAt: createdAt.Time,
	}
}

// ListVisibleFromBySessionWithinBytes is the byte-budgeted variant of
// ListVisibleFromBySession (CM-ADM-001): rows are admitted oldest-first
// starting at the cursor message — the required anchor must always load —
// until their running content byte total crosses maxBytes (the crossing row
// is kept). Metadata is returned undecoded in RawMetadata, like the other
// WithinBytes loaders; callers decode one row at a time.
func (s *DBService) ListVisibleFromBySessionWithinBytes(ctx context.Context, sessionID string, messageID string, maxBytes int64) ([]Message, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return nil, err
	}
	pgMessageID, err := dbpkg.ParseUUID(messageID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListVisibleMessagesFromBySessionWithinBytes(ctx, sqlc.ListVisibleMessagesFromBySessionWithinBytesParams{
		SessionID: pgSessionID,
		MessageID: pgMessageID,
		MaxBytes:  maxBytes,
	})
	if err != nil {
		return nil, err
	}
	msgs := make([]Message, 0, len(rows))
	for _, row := range rows {
		msgs = append(msgs, toMessageFromVisibleFromBySessionWithinBytesRow(row))
	}
	s.enrichAssets(ctx, msgs)
	return msgs, nil
}

func toMessageFromVisibleFromBySessionWithinBytesRow(row sqlc.ListVisibleMessagesFromBySessionWithinBytesRow) Message {
	m := toMessageFieldsWithMetadataMode(
		row.ID,
		row.BotID,
		row.SessionID,
		row.SenderChannelIdentityID,
		row.SenderUserID,
		row.SenderDisplayName,
		row.SenderAvatarUrl,
		row.Platform,
		row.ExternalMessageID,
		row.SourceReplyToMessageID,
		row.Role,
		row.Content,
		row.Metadata,
		row.Usage,
		row.SessionMode,
		row.RuntimeType,
		row.EventID,
		row.DisplayText,
		row.CreatedAt,
		false,
	)
	m.TurnID = uuidString(row.TurnID)
	if row.CompactID.Valid {
		m.CompactID = row.CompactID.String()
	}
	return m
}
