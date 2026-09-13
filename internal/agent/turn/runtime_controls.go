package turn

import "context"

type RuntimeCommandKind string

const (
	RuntimeCommandTurn      RuntimeCommandKind = "turn"
	RuntimeCommandRead      RuntimeCommandKind = "read"
	RuntimeCommandOperation RuntimeCommandKind = "operation"
)

// RuntimeCommandResult preserves structured observations for each surface to render.
// Text is reserved for native, already textual output; Notice is a stable catalog code.
type RuntimeCommandResult struct {
	Text   string `json:"text,omitempty"`
	Data   any    `json:"data,omitempty"`
	Notice string `json:"notice,omitempty"`
}

type RuntimeCommand struct {
	// I18nKey identifies host-owned copy. Native descriptions take precedence.
	I18nKey       string             `json:"i18n_key,omitempty"`
	Name          string             `json:"name"`
	Description   string             `json:"description"`
	InputHint     string             `json:"input_hint,omitempty"`
	Kind          RuntimeCommandKind `json:"kind"`
	RunningText   string             `json:"running_text,omitempty"`
	CompletedText string             `json:"completed_text,omitempty"`
}

type RuntimeMode struct {
	I18nKey     string `json:"i18n_key,omitempty"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Icon is a presentation hint; clients may fall back for unknown values.
	Icon string `json:"icon,omitempty"`
	// Warning marks a runtime-declared elevated permission option.
	Warning bool `json:"warning,omitempty"`
}

type RuntimeModeState struct {
	// Kind distinguishes permission presets from arbitrary agent session modes.
	Kind string `json:"kind,omitempty"`
	// Direct runtimes save a preference and apply it when starting the next turn.
	ApplyOnNextTurn bool          `json:"apply_on_next_turn,omitempty"`
	AvailableModes  []RuntimeMode `json:"available_modes"`
	CurrentModeID   string        `json:"current_mode_id"`
	Supported       bool          `json:"supported"`
}

type RuntimeControlCapabilities struct {
	PermissionModes bool `json:"permission_modes"`
	Compact         bool `json:"compact"`
	PlanMode        bool `json:"plan_mode"`
	Goal            bool `json:"goal"`
}

type RuntimeControls struct {
	SessionID    string                     `json:"session_id"`
	Commands     []RuntimeCommand           `json:"commands"`
	Modes        RuntimeModeState           `json:"modes"`
	PlanMode     *RuntimeModeState          `json:"plan_mode,omitempty"`
	Capabilities RuntimeControlCapabilities `json:"capabilities"`
}

type RuntimeControlRequest struct {
	TeamID      string `json:"team_id,omitempty"`
	BotID       string `json:"bot_id"`
	ThreadID    string `json:"session_id"`
	ActorID     string `json:"actor_id"`
	Command     string `json:"command,omitempty"`
	ModeID      string `json:"mode_id,omitempty"`
	ModeKind    string `json:"mode_kind,omitempty"`
	ToolHTTPURL string `json:"tool_http_url,omitempty"`
}

// RuntimeControlService is the channel-safe, optional control port.
type RuntimeControlService interface {
	RuntimeCommands(context.Context, RuntimeControlRequest) ([]RuntimeCommand, error)
	RuntimeControls(context.Context, RuntimeControlRequest) (RuntimeControls, error)
	SetRuntimeMode(context.Context, RuntimeControlRequest) (RuntimeModeState, error)
	ExecuteRuntimeCommand(context.Context, RuntimeControlRequest) (RuntimeCommandResult, error)
}

// RuntimeGoal is a view of the runtime-owned goal, not another goal store.
type RuntimeGoal struct {
	Objective       string `json:"objective"`
	Status          string `json:"status"`
	TokensUsed      int64  `json:"tokens_used"`
	TokenBudget     *int64 `json:"token_budget,omitempty"`
	TimeUsedSeconds int64  `json:"time_used_seconds"`
}

// RuntimeGoalService controls an existing goal without claiming a second run.
// Creation and resumption use the ordinary admitted conversation path.
type RuntimeGoalService interface {
	RuntimeGoal(context.Context, RuntimeControlRequest) (*RuntimeGoal, error)
	ControlRuntimeGoal(context.Context, RuntimeControlRequest, string) error
}
