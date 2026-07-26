// Package tool adapts SQLC history queries to the History tool's persistence port.
package tool

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	dbsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
	toolpersistence "github.com/memohai/memoh/domains/agent/tool/persistence"
	"github.com/memohai/memoh/internal/db"
)

// Queries is the SQLC query subset required by history search.
type Queries interface {
	SearchMessages(context.Context, dbsqlc.SearchMessagesParams) ([]dbsqlc.SearchMessagesRow, error)
}

// Searcher implements History tool search persistence with PostgreSQL.
type Searcher struct {
	queries Queries
}

// NewSearcher creates a PostgreSQL-backed History searcher.
func NewSearcher(queries Queries) *Searcher {
	return &Searcher{queries: queries}
}

// NewSearcherFromDB creates a History searcher bound to Agent-owner SQLC.
func NewSearcherFromDB(db dbsqlc.DBTX) *Searcher {
	return NewSearcher(dbsqlc.New(db))
}

var _ toolpersistence.HistorySearcher = (*Searcher)(nil)

// SearchHistory searches persisted history using SQLC.
func (s *Searcher) SearchHistory(ctx context.Context, filter toolpersistence.HistorySearchFilter) ([]toolpersistence.HistorySearchResult, error) {
	botID, err := db.ParseUUID(filter.BotID)
	if err != nil {
		return nil, errors.New("invalid bot_id")
	}

	rows, err := s.queries.SearchMessages(ctx, dbsqlc.SearchMessagesParams{
		BotID:     botID,
		SessionID: db.ParseUUIDOrEmpty(filter.SessionID),
		ContactID: db.ParseUUIDOrEmpty(filter.ContactID),
		StartTime: optionalTime(filter.StartTime),
		EndTime:   optionalTime(filter.EndTime),
		Role:      optionalText(filter.Role),
		Keyword:   optionalText(filter.Keyword),
		MaxCount:  filter.Limit,
	})
	if err != nil {
		return nil, err
	}

	results := make([]toolpersistence.HistorySearchResult, len(rows))
	for i, row := range rows {
		results[i] = toolpersistence.HistorySearchResult{
			ID:        row.ID.String(),
			SessionID: row.SessionID.String(),
			ContactID: db.UUIDString(row.SenderChannelIdentityID),
			Role:      row.Role,
			Content:   append([]byte(nil), row.Content...),
			CreatedAt: db.TimeFromPg(row.CreatedAt),
			Sender:    db.TextToString(row.SenderDisplayName),
			Platform:  db.TextToString(row.Platform),
		}
	}
	return results, nil
}

func optionalText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func optionalTime(value time.Time) pgtype.Timestamptz {
	if value.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: value, Valid: true}
}
