package message

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// ListTurnResponseSourcesWithinBytes walks the raw history window as
// ListHistoryWithinBytes does and projects only the fields used by timeline
// turn-response composition. In particular, legacy lifecycle audits must
// stay in the database, rather than being transferred and decoded alongside
// a small content window. Display fields and asset enrichment are not needed
// on this path.
func (s *DBService) ListTurnResponseSourcesWithinBytes(ctx context.Context, sessionID string, after HistoryPosition, maxBytes int64) (HistoryLoad, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return HistoryLoad{}, err
	}
	ids, truncated, err := s.historyWindow(ctx, pgSessionID, HistoryWindow{After: after, MaxBytes: maxBytes})
	if err != nil || len(ids) == 0 {
		return HistoryLoad{}, err
	}
	msgs, err := s.listTurnResponseSourcesByIDs(ctx, pgSessionID, ids)
	return HistoryLoad{Messages: msgs, Truncated: truncated}, err
}

// ListTurnResponseSourcesByIDs projects the named context rows of a session
// as ListTurnResponseSourcesWithinBytes projects its window, with the
// inbound message a user row records.
func (s *DBService) ListTurnResponseSourcesByIDs(ctx context.Context, sessionID string, ids []string) ([]Message, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return nil, err
	}
	pgIDs, err := parseUUIDs(ids)
	if err != nil {
		return nil, err
	}
	return s.listTurnResponseSourcesByIDs(ctx, pgSessionID, pgIDs)
}

func (s *DBService) listTurnResponseSourcesByIDs(ctx context.Context, sessionID pgtype.UUID, ids []pgtype.UUID) ([]Message, error) {
	rows, err := s.queries.ListTurnResponseSourcesByIDs(ctx, sqlc.ListTurnResponseSourcesByIDsParams{SessionID: sessionID, Ids: ids})
	if err != nil {
		return nil, err
	}
	messages := make([]Message, 0, len(rows))
	for _, row := range rows {
		message := turnResponseSource(row.ID, row.Role, row.Content, row.CreatedAt, row.Interrupted, row.DiscussStep)
		message.ExternalMessageID = strings.TrimSpace(row.ExternalMessageID.String)
		messages = append(messages, message)
	}
	return messages, nil
}

func turnResponseSource(id pgtype.UUID, role string, content []byte, createdAt pgtype.Timestamptz, interrupted, discussStep bool) Message {
	metadata := map[string]any{AgentStepInterruptedMetadataKey: interrupted}
	if discussStep {
		metadata[DiscussStepMetadataKey] = true
	}
	return Message{
		ID:        id.String(),
		Role:      role,
		Content:   content,
		CreatedAt: createdAt.Time,
		Metadata:  metadata,
	}
}
