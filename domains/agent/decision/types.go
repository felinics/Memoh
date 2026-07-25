package decision

import "time"

const (
	KindApproval  = "approval"
	KindUserInput = "user_input"
)

// Request is the app-layer decision DTO. Storage remains owned by the
// kind-specific services: toolapproval writes tool_approval_requests, and
// userinput writes user_input_requests.
type Request struct {
	ID                      string         `json:"id"`
	BotID                   string         `json:"bot_id"`
	SessionID               string         `json:"session_id"`
	RouteID                 string         `json:"route_id,omitempty"`
	ChannelIdentityID       string         `json:"channel_identity_id,omitempty"`
	WorkspaceTargetID       string         `json:"workspace_target_id,omitempty"`
	Kind                    string         `json:"kind"`
	ToolCallID              string         `json:"tool_call_id"`
	ToolName                string         `json:"tool_name"`
	ToolInput               map[string]any `json:"tool_input,omitempty"`
	UIPayloadRaw            []byte         `json:"-"`
	Result                  map[string]any `json:"result,omitempty"`
	ProviderMetadata        map[string]any `json:"-"`
	ShortID                 int            `json:"short_id"`
	Status                  string         `json:"status"`
	DecisionReason          string         `json:"decision_reason,omitempty"`
	PromptExternalMessageID string         `json:"prompt_external_message_id,omitempty"`
	SourcePlatform          string         `json:"source_platform,omitempty"`
	ReplyTarget             string         `json:"reply_target,omitempty"`
	ConversationType        string         `json:"conversation_type,omitempty"`
	CreatedAt               time.Time      `json:"created_at"`
	DecidedAt               *time.Time     `json:"decided_at,omitempty"`
	ExpiresAt               *time.Time     `json:"expires_at,omitempty"`
}
