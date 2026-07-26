// Package persistence defines the chat usage read-model ports and the records
// they exchange, separately from the service that consumes them.
package persistence

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound reports that a requested chat usage read model does not exist.
var ErrNotFound = errors.New("chat usage read model not found")

// ErrCountRecords classifies a failure to count a filtered usage page.
var ErrCountRecords = errors.New("count chat usage records")

// CacheStats contains session-level prompt cache usage.
type CacheStats struct {
	TotalInputTokens int64
	CacheReadTokens  int64
}

// Daily contains token usage for one session type and calendar day.
type Daily struct {
	SessionType     string
	Day             time.Time
	InputTokens     int64
	OutputTokens    int64
	CacheReadTokens int64
	ReasoningTokens int64
}

// Model contains token usage aggregated by model.
type Model struct {
	ModelID      string
	ModelSlug    string
	ModelName    string
	ProviderName string
	InputTokens  int64
	OutputTokens int64
}

// ModelProjection contains the display fields usage needs from the Model owner.
type ModelProjection struct {
	ModelSlug    string
	ModelName    string
	ProviderName string
}

// ModelProjectionReader batch-loads Model-owned display fields by internal UUID.
type ModelProjectionReader interface {
	GetModelProjections(context.Context, []string) (map[string]ModelProjection, error)
}

// Record contains token usage for one assistant message.
type Record struct {
	ID              string
	CreatedAt       time.Time
	SessionID       string
	SessionType     string
	ModelID         string
	ModelSlug       string
	ModelName       string
	ProviderName    string
	InputTokens     int64
	OutputTokens    int64
	CacheReadTokens int64
	ReasoningTokens int64
}

// Session contains the fields required to authorize a session usage read.
type Session struct {
	ID              string
	BotID           string
	Type            string
	SessionMode     string
	RuntimeType     string
	CreatedByUserID string
}

// Filter scopes token usage reads. Empty ModelID and SessionType match all.
type Filter struct {
	BotID       string
	From        time.Time
	To          time.Time
	ModelID     string
	SessionType string
}

// Pagination selects a stable offset page.
type Pagination struct {
	Limit  int
	Offset int
}

// Page is a page of individual usage records and its unpaginated total.
type Page struct {
	Items []Record
	Total int64
}

// CommandReader is the minimal usage surface consumed by slash commands.
type CommandReader interface {
	GetLatestSessionIDByBot(ctx context.Context, botID string) (string, error)
	CountMessagesBySession(ctx context.Context, sessionID string) (int64, error)
	GetLatestAssistantUsage(ctx context.Context, sessionID string) (int64, error)
	GetSessionCacheStats(ctx context.Context, sessionID string) (CacheStats, error)
	GetSessionUsedSkills(ctx context.Context, sessionID string) ([]string, error)
	GetTokenUsageByDayAndType(ctx context.Context, botID string, from, to time.Time) ([]Daily, error)
	GetTokenUsageByModel(ctx context.Context, botID string, from, to time.Time) ([]Model, error)
}

// Reader is the persistence-neutral Agent Chat usage read model.
type Reader interface {
	CommandReader
	GetSession(ctx context.Context, sessionID string) (Session, error)
	GetDaily(ctx context.Context, filter Filter) ([]Daily, error)
	GetByModel(ctx context.Context, filter Filter) ([]Model, error)
	ListRecords(ctx context.Context, filter Filter, pagination Pagination) (Page, error)
}
