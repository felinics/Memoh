package schedule

import (
	"encoding/json"
	"time"

	"github.com/felinics/memoh/internal/runtimekind"
)

// Run targets: where one fire of a schedule executes.
const (
	// RunTargetNewSession creates a fresh user-visible session per fire.
	RunTargetNewSession = "new_session"
	// RunTargetExistingSession reuses one stored session for every fire.
	// The target session pins runtime and workdir; only model and
	// reasoning effort stay overridable per schedule.
	RunTargetExistingSession = "existing_session"
)

// Runtime types are the bot_sessions vocabulary, pinned to the shared
// runtimekind table (the schedule domain cannot import the chat packages).
const (
	RuntimeModel    = string(runtimekind.Model)
	RuntimeACPAgent = string(runtimekind.ACPAgent)
)

// ExecutionConfig is the per-schedule execution parameter block: where a
// fire runs and with which runtime/model/effort/workdir. The zero value
// means "new session with all bot defaults" — exactly the pre-parameter
// behavior.
type ExecutionConfig struct {
	// RunTarget is new_session (default) or existing_session.
	RunTarget string `json:"run_target,omitempty"`
	// TargetSessionID names the session reused by existing_session mode.
	TargetSessionID string `json:"target_session_id,omitempty"`
	// RuntimeType selects the runtime for new sessions: "" or "model" for
	// the native model runtime, "acp_agent" for an ACP agent. Must be empty
	// in existing_session mode (inherited from the target session).
	RuntimeType string `json:"runtime_type,omitempty"`
	// BotAgentID selects one persisted BotAgent for a new session. Empty means
	// the built-in Native runtime (or the legacy ACP fields below).
	BotAgentID string `json:"bot_agent_id,omitempty"`
	// ACPAgentID names the ACP agent when RuntimeType is acp_agent.
	ACPAgentID string `json:"acp_agent_id,omitempty"`
	// ModelID is a native model UUID override (models.id).
	ModelID string `json:"model_id,omitempty"`
	// ACPModelID is an agent-reported model identifier override for External
	// Agent runs. Mutually exclusive with ModelID.
	ACPModelID string `json:"acp_model_id,omitempty"`
	// ReasoningEffort overrides the reasoning effort for this schedule.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// WorkdirID binds new sessions to a bot workdir. Must be empty in
	// existing_session mode (inherited from the target session).
	WorkdirID string `json:"workdir_id,omitempty"`
}

type Schedule struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	Pattern      string    `json:"pattern"`
	MaxCalls     *int      `json:"max_calls,omitempty"`
	CurrentCalls int       `json:"current_calls"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Enabled      bool      `json:"enabled"`
	Command      string    `json:"command"`
	BotID        string    `json:"bot_id"`
	ExecutionConfig
}

type NullableInt struct {
	Value *int
	Set   bool
}

func (n NullableInt) IsZero() bool {
	return !n.Set
}

func (n NullableInt) MarshalJSON() ([]byte, error) {
	if !n.Set || n.Value == nil {
		return []byte("null"), nil
	}
	return json.Marshal(*n.Value)
}

func (n *NullableInt) UnmarshalJSON(data []byte) error {
	n.Set = true
	if string(data) == "null" {
		n.Value = nil
		return nil
	}
	var value int
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	n.Value = &value
	return nil
}

type CreateRequest struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Pattern     string      `json:"pattern"`
	MaxCalls    NullableInt `json:"max_calls,omitempty"`
	Command     string      `json:"command"`
	Enabled     *bool       `json:"enabled,omitempty"`
	ExecutionConfig
}

type UpdateRequest struct {
	Name        *string     `json:"name,omitempty"`
	Description *string     `json:"description,omitempty"`
	Pattern     *string     `json:"pattern,omitempty"`
	MaxCalls    NullableInt `json:"max_calls,omitempty"`
	Command     *string     `json:"command,omitempty"`
	Enabled     *bool       `json:"enabled,omitempty"`
	// Execution replaces the whole execution parameter block when present.
	// Field-level patching is deliberately not offered: the block carries
	// cross-field constraints (run target vs runtime vs workdir), so
	// callers send the full desired state and the service validates it as
	// one unit.
	Execution *ExecutionConfig `json:"execution,omitempty"`
}

type ListResponse struct {
	Items []Schedule `json:"items"`
}

type Log struct {
	ID           string     `json:"id"`
	ScheduleID   string     `json:"schedule_id"`
	BotID        string     `json:"bot_id"`
	SessionID    string     `json:"session_id,omitempty"`
	Status       string     `json:"status"`
	ResultText   string     `json:"result_text"`
	ErrorMessage string     `json:"error_message"`
	Usage        any        `json:"usage,omitempty"`
	StartedAt    time.Time  `json:"started_at"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
}

type ListLogsResponse struct {
	Items      []Log `json:"items"`
	TotalCount int64 `json:"total_count"`
}
