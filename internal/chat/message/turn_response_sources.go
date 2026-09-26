package message

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// ListTurnResponseSourcesSinceBySessionWithinBytes projects only the fields
// used by timeline turn-response composition. In particular, legacy lifecycle
// audits must stay in the database, rather than being transferred and decoded
// alongside a small content window. Display fields and asset enrichment are
// not needed on this path.
func (s *DBService) ListTurnResponseSourcesSinceBySessionWithinBytes(ctx context.Context, sessionID string, since time.Time, maxBytes int64) ([]Message, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListTurnResponseSourcesSinceBySessionWithinBytes(ctx, sqlc.ListTurnResponseSourcesSinceBySessionWithinBytesParams{
		SessionID: pgSessionID,
		CreatedAt: pgtype.Timestamptz{Time: since, Valid: true},
		MaxBytes:  maxBytes,
	})
	if err != nil {
		return nil, err
	}
	messages := make([]Message, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, Message{
			ID:        row.ID.String(),
			Role:      row.Role,
			Content:   row.Content,
			CreatedAt: row.CreatedAt.Time,
			Metadata:  map[string]any{AgentStepInterruptedMetadataKey: row.Interrupted},
		})
	}
	return messages, nil
}
