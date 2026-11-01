package compaction

import (
	"errors"
	"net/http"
	"time"

	"github.com/felinics/memoh/internal/agent/turn"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

// Pass is one committed pass of a compaction event: the summary it wrote and
// the request it leaves.
type Pass struct {
	SummaryID string
	Summary   string
	// Order is the request after the pass, by the messages of the request it
	// read: the index of each that stays, or -1 for the summary, which takes
	// the place turn.PlaceSummary gives it; cut is where the newest messages
	// the event keeps start in it.
	Order []int
	cut   int
	// More reports candidates left that the pass could not read; the event
	// folds them in a later pass.
	More bool
}

// Reason names why a compaction event failed.
type Reason string

const (
	ReasonSummarizerFailed  Reason = "summarizer_failed"
	ReasonTruncated         Reason = "truncated"
	ReasonEmpty             Reason = "empty"
	ReasonIneffective       Reason = "ineffective"
	ReasonStaleSource       Reason = "stale_source"
	ReasonInputOverflow     Reason = "input_overflow"
	ReasonStillOver         Reason = "still_over"
	ReasonProtectedOverflow Reason = "protected_overflow"
)

// EventError is a pass that failed, with the reason the event reports for it.
// Passes committed before it stay committed.
type EventError struct {
	Reason Reason
	Err    error
}

func (e *EventError) Error() string { return string(e.Reason) + ": " + e.Err.Error() }

func (e *EventError) Unwrap() error { return e.Err }

// failed names the reason a pass ends the event with.
func failed(reason Reason, err error) error {
	return &EventError{Reason: reason, Err: err}
}

// ReasonOf is the reason err reports, or empty when it reports none.
func ReasonOf(err error) Reason {
	var event *EventError
	if errors.As(err, &event) {
		return event.Reason
	}
	return ""
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
	BotID                 string
	SessionID             string
	ModelID               string // runtime model slug sent to the provider
	ModelRecordID         string // models.id row UUID recorded as the log's provenance
	ClientType            string
	APIKey                string //nolint:gosec // runtime credential, not a hardcoded secret
	CodexAccountID        string
	BaseURL               string
	ChatCompletionsCompat string
	HTTPClient            *http.Client
	// TotalInputTokens is the history pressure that called for this
	// compaction, reported to hooks.
	TotalInputTokens int
	// SummaryWindowTokens and MaxOutputTokens are the summarizer model's
	// declared window and output cap; they size the summarizer call.
	SummaryWindowTokens int
	MaxOutputTokens     int
	// CandidatesBefore, when set, is where the caller's history ends: the
	// cutoff an edit or retry replays history before, or the turn the request
	// runs as. Rows from it on are not folded.
	CandidatesBefore *messagepkg.HistoryPosition
	// ReplacedTurnID is the turn an edit or retry replaces. The replacement
	// rewinds the session to the checkpoint that does not depend on that
	// turn when its first step commits; an event the request needs earlier
	// rewinds first, so it folds onto the checkpoint the replacement keeps.
	ReplacedTurnID string
	// Anchors are kept verbatim where they are, as AnchorPolicy keeps them: a
	// run's input and steers, or the inputs a Discuss run answers.
	Anchors      []turn.ContextMessageSource
	AnchorPolicy AnchorPolicy
	// CurrentRequest is the caller's current input, which the summarizer
	// reads to judge what the work needs.
	CurrentRequest string
	// HistoryBudgetTokens is the room the caller's request has for what the
	// history renders: the summary and the rows it keeps raw.
	HistoryBudgetTokens int
}

// hookScope is what the compaction hooks are told about cfg's event.
func (cfg TriggerConfig) hookScope() hookScope {
	return hookScope{BotID: cfg.BotID, SessionID: cfg.SessionID, ModelID: cfg.ModelID, InputTokens: cfg.TotalInputTokens, BudgetTokens: cfg.HistoryBudgetTokens}
}
