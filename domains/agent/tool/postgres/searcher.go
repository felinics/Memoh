// Package postgres adapts SQLC history queries to the History tool's persistence port.
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	dbsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
	"github.com/memohai/memoh/domains/agent/tool"
	dbpkg "github.com/memohai/memoh/internal/db"
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

var _ tool.HistorySearcher = (*Searcher)(nil)

// SearchHistory searches persisted history using SQLC.
func (s *Searcher) SearchHistory(ctx context.Context, filter tool.HistorySearchFilter) ([]tool.HistorySearchResult, error) {
	botID, err := dbpkg.ParseUUID(filter.BotID)
	if err != nil {
		return nil, errors.New("invalid bot_id")
	}

	rows, err := s.queries.SearchMessages(ctx, dbsqlc.SearchMessagesParams{
		BotID:     botID,
		SessionID: dbpkg.ParseUUIDOrEmpty(filter.SessionID),
		ContactID: dbpkg.ParseUUIDOrEmpty(filter.ContactID),
		StartTime: optionalTime(filter.StartTime),
		EndTime:   optionalTime(filter.EndTime),
		Role:      optionalText(filter.Role),
		Keyword:   optionalText(filter.Keyword),
		MaxCount:  filter.Limit,
	})
	if err != nil {
		return nil, err
	}

	results := make([]tool.HistorySearchResult, len(rows))
	for i, row := range rows {
		results[i] = tool.HistorySearchResult{
			ID:        row.ID.String(),
			SessionID: row.SessionID.String(),
			ContactID: uuidString(row.SenderChannelIdentityID),
			Role:      row.Role,
			Content:   append([]byte(nil), row.Content...),
			CreatedAt: dbpkg.TimeFromPg(row.CreatedAt),
			Sender:    dbpkg.TextToString(row.SenderDisplayName),
			Platform:  dbpkg.TextToString(row.Platform),
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

func uuidString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return value.String()
}
