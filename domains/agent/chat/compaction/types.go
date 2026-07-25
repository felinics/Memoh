package compaction

import (
	"net/http"
	"time"
)

// Compaction result statuses reported by RunCompactionSync.
const (
	StatusOK   = "ok"   // messages were compacted into a summary
	StatusNoop = "noop" // nothing to compact (already compact, cooled down, or in flight)
)

// Result is the scoped outcome of a synchronous compaction. Callers use it to
// respond with this session's own result instead of reading unscoped bot-wide
// logs. A failed attempt returns an error, not a Result.
type Result struct {
	Status       string
	Summary      string
	MessageCount int
}

// Log represents a compaction log entry.
type Log struct {
	ID           string     `json:"id"`
	BotID        string     `json:"bot_id"`
	SessionID    string     `json:"session_id,omitempty"`
	Status       string     `json:"status"`
	Summary      string     `json:"summary"`
	MessageCount int        `json:"message_count"`
	ErrorMessage string     `json:"error_message"`
	Usage        any        `json:"usage,omitempty"`
	ModelID      string     `json:"model_id,omitempty"`
	StartedAt    time.Time  `json:"started_at"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
} // @name compaction.Log

// ListLogsResponse is the API response for listing compaction logs.
type ListLogsResponse struct {
	Items      []Log `json:"items"`
	TotalCount int64 `json:"total_count"`
} // @name compaction.ListLogsResponse

// TriggerConfig holds the parameters needed to trigger a compaction.
type TriggerConfig struct {
	BotID            string
	SessionID        string
	ModelID          string
	ClientType       string
	APIKey           string //nolint:gosec // runtime credential, not a hardcoded secret
	CodexAccountID   string
	BaseURL          string
	HTTPClient       *http.Client
	Ratio            int
	TotalInputTokens int
	MaxCompactTokens int // if > 0, cap compaction input to this many tokens (e.g. 90% of model window)
	TargetTokens     int // if > 0, compaction goal: reduce context to this many tokens (used by sync compaction)
	PromptCacheTTL   string

	// Manual marks a user-initiated compaction (slash command, HTTP endpoint).
	// Such a request bypasses the per-session failure cooldown so a user who
	// just fixed their credentials/model isn't told "done" while nothing runs.
	// Automatic per-request paths leave this false to keep the cooldown backstop.
	Manual bool
}
