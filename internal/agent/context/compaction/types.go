package compaction

import (
	"errors"
	"net/http"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/turn"
)

// Pass is one committed pass of a compaction event: the summary it wrote and
// what that summary replaces.
type Pass struct {
	ArtifactID string
	Summary    string
	// Covered names each row the summary folds, by history ID and, when the
	// row has one, by external ID. Absorbed names the earlier summaries it
	// folds.
	Covered  []turn.ContextMessageSource
	Absorbed []string
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
	ModelRecordID         string // models.id row UUID recorded as artifact/log provenance
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
	// CandidatesFrom is where the caller's history begins and
	// CandidatesBefore the cutoff an edit or retry replays history before.
	// Rows outside are not part of that request. Zero leaves that side open.
	CandidatesFrom   time.Time
	CandidatesBefore time.Time
	// ProtectedSources stay raw, with everything after them. Current ones are
	// the caller's current input, budgeted outside HistoryBudgetTokens; the
	// rest are history it keeps.
	ProtectedSources []turn.ContextMessageSource
	// View, when set, is what the caller's request renders: only these
	// history rows and inbound messages are candidates, whatever else the
	// session holds. Anchors stay raw where they are without ending what a
	// pass may fold; the summarizer reads them in place.
	View    []turn.ContextMessageSource
	Anchors []turn.ContextMessageSource
	// CurrentRequest is the caller's current input, shown to the summarizer
	// when no protected source is a history row yet.
	CurrentRequest string
	// HistoryBudgetTokens is the room the caller's request has for the
	// summary and the protected history it keeps raw. Anchors and current
	// sources are not in it: the caller prices what it renders for them.
	HistoryBudgetTokens int
	PromptCacheTTL      string
	// Continuation, when set, is the request the caller sent last. A pass
	// that folds everything foldable may ask for its summary by extending
	// that request, which the provider may still hold in its prompt cache,
	// instead of sending the summarizer's own.
	Continuation *sdk.Request
}
