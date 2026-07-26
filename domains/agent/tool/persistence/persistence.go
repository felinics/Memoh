// Package persistence defines Agent tool persistence ports and the records
// they exchange, separately from the tool providers that consume them.
package persistence

import (
	"context"
	"time"
)

// HistorySearchFilter contains persistence-neutral message search criteria.
type HistorySearchFilter struct {
	BotID     string
	SessionID string
	ContactID string
	Role      string
	Keyword   string
	StartTime time.Time
	EndTime   time.Time
	Limit     int32
}

// HistorySearchResult is a persistence-neutral message search result.
type HistorySearchResult struct {
	ID        string
	SessionID string
	ContactID string
	Role      string
	Content   []byte
	CreatedAt time.Time
	Sender    string
	Platform  string
}

// HistorySearcher is the minimal interface for searching persisted messages.
type HistorySearcher interface {
	SearchHistory(ctx context.Context, filter HistorySearchFilter) ([]HistorySearchResult, error)
}
