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
	ID            string     `json:"id"`
	BotID         string     `json:"bot_id"`
	SessionID     string     `json:"session_id,omitempty"`
	Status        string     `json:"status"`
	Summary       string     `json:"summary"`
	MessageCount  int        `json:"message_count"`
	ErrorMessage  string     `json:"error_message"`
	Usage         any        `json:"usage,omitempty"`
	ModelID       string     `json:"model_id,omitempty"`
	ArtifactLevel int        `json:"artifact_level"`
	ParentIDs     []string   `json:"parent_ids"`
	StartedAt     time.Time  `json:"started_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
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
	ModelID               string
	ClientType            string
	APIKey                string //nolint:gosec // runtime credential, not a hardcoded secret
	CodexAccountID        string
	BaseURL               string
	ChatCompletionsCompat string
	HTTPClient            *http.Client
	Ratio                 int
	TotalInputTokens      int
	ModelContextTokens    int
	MaxCompactTokens      int // if > 0, cap compaction input to this many tokens (e.g. 90% of model window)
	TargetTokens          int // if > 0, compaction goal: reduce context to this many tokens (used by sync compaction)
	Rolling               bool
	SummaryTargetTokens   int // rolling summary output ceiling after model-window and output-limit clamping
	PromptCacheTTL        string

	// ObservedArtifactIDs is the active compaction frontier that was visible
	// when the caller measured context pressure. Automatic async triggers may
	// wait behind an in-flight compaction; if the frontier advances meanwhile,
	// their token count describes stale, pre-compaction context and must not
	// start another summarizer call.
	ObservedArtifactIDs    []string
	ObservedArtifactsKnown bool

	// Manual marks a user-initiated compaction (slash command, HTTP endpoint).
	// Such a request bypasses the per-session failure cooldown so a user who
	// just fixed their credentials/model isn't told "done" while nothing runs.
	// Automatic per-request paths leave this false to keep the cooldown backstop.
	Manual bool
}

const (
	// SummaryWindowFractionDenominator keeps enough of the model window for the
	// history being summarized. A replacement summary may use at most one
	// quarter of the configured context window.
	SummaryWindowFractionDenominator = 4
	// ConservativeSummaryOutputTokens is used until model records expose a
	// provider-independent maximum-output-token capability.
	ConservativeSummaryOutputTokens = 16384
)

// RollingSummaryTargetTokens interprets ratio as the desired replacement
// summary size relative to the configured trigger threshold, then clamps that
// target to both a quarter of the model window and a conservative output cap.
func RollingSummaryTargetTokens(threshold, ratio, modelContextTokens int) int {
	if threshold <= 0 || ratio <= 0 {
		return 0
	}
	if ratio > 100 {
		ratio = 100
	}
	target := threshold * ratio / 100
	if target == 0 {
		target = 1
	}
	return clampSummaryTargetTokens(target, modelContextTokens)
}

// ManualSummaryTargetTokens provides a positive target for user-initiated
// rolling compaction even when automatic compaction (threshold=0) is disabled.
func ManualSummaryTargetTokens(modelContextTokens int) int {
	target := ConservativeSummaryOutputTokens
	if modelContextTokens > 0 {
		target = modelContextTokens / SummaryWindowFractionDenominator
	}
	if target <= 0 {
		target = 1
	}
	return clampSummaryTargetTokens(target, modelContextTokens)
}

func clampSummaryTargetTokens(target, modelContextTokens int) int {
	if target <= 0 {
		return 0
	}
	if modelContextTokens > 0 {
		windowTarget := modelContextTokens / SummaryWindowFractionDenominator
		if windowTarget <= 0 {
			windowTarget = 1
		}
		if target > windowTarget {
			target = windowTarget
		}
	}
	if target > ConservativeSummaryOutputTokens {
		target = ConservativeSummaryOutputTokens
	}
	return target
}
