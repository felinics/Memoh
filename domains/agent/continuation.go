package agent

import "time"

// AdvanceTextInput advances a pending ask_user flow with a plain-text reply.
type AdvanceTextInput struct {
	BotID                  string
	SessionID              string
	ExplicitID             string
	ReplyExternalMessageID string
	Text                   string
}

// AdvanceTextResult is the turn-contract outcome of AdvancePlainTextUserInput.
type AdvanceTextResult struct {
	Handled bool
	Invalid bool
	Request AdvanceTextRequest
}

// AdvanceTextRequest is the turn-owned wire projection of a user-input request
// returned across AdvancePlainTextUserInput. Field tags match the JSON shape of
// the decision/input Request value, excluding non-serialized fields.
type AdvanceTextRequest struct {
	ID                      string                 `json:"id"`
	BotID                   string                 `json:"bot_id"`
	SessionID               string                 `json:"session_id"`
	RouteID                 string                 `json:"route_id,omitempty"`
	ChannelIdentityID       string                 `json:"channel_identity_id,omitempty"`
	WorkspaceTargetID       string                 `json:"workspace_target_id,omitempty"`
	ToolCallID              string                 `json:"tool_call_id"`
	ToolName                string                 `json:"tool_name"`
	ShortID                 int                    `json:"short_id"`
	Status                  string                 `json:"status"`
	Input                   map[string]any         `json:"input,omitempty"`
	UIPayload               AdvanceTextUIPayload   `json:"ui_payload"`
	Interaction             AdvanceTextInteraction `json:"interaction"`
	InteractionRevision     int                    `json:"interaction_revision"`
	Result                  map[string]any         `json:"result,omitempty"`
	PromptExternalMessageID string                 `json:"prompt_external_message_id,omitempty"`
	SourcePlatform          string                 `json:"source_platform,omitempty"`
	ReplyTarget             string                 `json:"reply_target,omitempty"`
	ConversationType        string                 `json:"conversation_type,omitempty"`
	ExpiresAt               *time.Time             `json:"expires_at,omitempty"`
	CreatedAt               time.Time              `json:"created_at"`
	RespondedAt             *time.Time             `json:"responded_at,omitempty"`
	CanceledAt              *time.Time             `json:"canceled_at,omitempty"`
}

// AdvanceTextUIPayload is the ask_user UI payload carried on AdvanceTextRequest.
type AdvanceTextUIPayload struct {
	Version   int                     `json:"version"`
	Questions []AdvanceTextUIQuestion `json:"questions"`
}

// AdvanceTextUIQuestion describes one question in an AdvanceTextUIPayload.
type AdvanceTextUIQuestion struct {
	ID          string                `json:"id"`
	Text        string                `json:"text"`
	Kind        string                `json:"kind"`
	Options     []AdvanceTextUIOption `json:"options,omitempty"`
	AllowCustom bool                  `json:"allow_custom,omitempty"`
	Placeholder string                `json:"placeholder,omitempty"`
}

// AdvanceTextUIOption describes one selectable option in an AdvanceTextUIQuestion.
type AdvanceTextUIOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// AdvanceTextInteraction is the durable ask_user cursor on AdvanceTextRequest.
type AdvanceTextInteraction struct {
	QuestionIndex int              `json:"question_index"`
	Answers       []QuestionAnswer `json:"answers,omitempty"`
	Completed     bool             `json:"completed,omitempty"`
}
