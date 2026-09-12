package turn

import "context"

type RuntimeCommandKind string

const (
	RuntimeCommandTurn      RuntimeCommandKind = "turn"
	RuntimeCommandRead      RuntimeCommandKind = "read"
	RuntimeCommandOperation RuntimeCommandKind = "operation"
)

type RuntimeCommand struct {
	Name          string             `json:"name"`
	Description   string             `json:"description"`
	InputHint     string             `json:"input_hint,omitempty"`
	Kind          RuntimeCommandKind `json:"kind"`
	RunningText   string             `json:"running_text,omitempty"`
	CompletedText string             `json:"completed_text,omitempty"`
}

type RuntimeMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Icon is a presentation hint; clients may fall back for unknown values.
	Icon string `json:"icon,omitempty"`
	// Warning marks a runtime-declared elevated permission option.
	Warning bool `json:"warning,omitempty"`
}

type RuntimeModeState struct {
	AvailableModes []RuntimeMode `json:"available_modes"`
	CurrentModeID  string        `json:"current_mode_id"`
	Supported      bool          `json:"supported"`
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
	Language    string `json:"language,omitempty"`
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
	ExecuteRuntimeCommand(context.Context, RuntimeControlRequest) (string, error)
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
