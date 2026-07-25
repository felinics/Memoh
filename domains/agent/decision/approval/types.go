package approval

import (
	"context"
	"errors"
	"time"
)

const (
	StatusPending   = "pending"
	StatusApproved  = "approved"
	StatusRejected  = "rejected"
	StatusExpired   = "expired"
	StatusCancelled = "cancelled"

	OperationRead  = "read"
	OperationWrite = "write"
	OperationExec  = "exec"

	DecisionBypass        = "bypass"
	DecisionNeedsApproval = "needs_approval"
	DecisionDeny          = "deny"

	PolicyDeniedReason = "tool execution denied by policy"

	ExecutionLocationMetadataKey = "execution_location"
)

var (
	ErrNotFound       = errors.New("tool approval request not found")
	ErrAlreadyDecided = errors.New("tool approval request already decided")
	ErrForbidden      = errors.New("tool approval forbidden")
	ErrAmbiguous      = errors.New("tool approval request is ambiguous")
)

type CreatePendingInput struct {
	BotID                        string
	SessionID                    string
	RouteID                      string
	ChannelIdentityID            string
	WorkspaceTargetID            string
	RequestedByChannelIdentityID string
	RequestedMessageID           string
	ToolCallID                   string
	ToolName                     string
	ToolInput                    any
	SourcePlatform               string
	ReplyTarget                  string
	ConversationType             string
	WorkspaceTargeted            bool
	ExecutionLocation            *ExecutionLocation
}

type WorkspaceTargetPolicy struct {
	TargetID string
	Kind     string
	Name     string
	Config   PolicyConfig
}

// ExecutionLocation is the stable, user-facing identity of the workspace
// target selected for one tool call. Runtime IDs stay internal; clients render
// Name (or localize Kind for the Server Workspace).
type ExecutionLocation struct {
	TargetID string `json:"-"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
}

type WorkspaceTargetPolicyResolver interface {
	ResolveWorkspaceTargetPolicy(ctx context.Context, botID, targetID string) (WorkspaceTargetPolicy, error)
}

type Evaluation struct {
	Decision          string
	Request           Request
	ExecutionLocation *ExecutionLocation
}

type ResolveInput struct {
	BotID                  string
	SessionID              string
	ExplicitID             string
	ReplyExternalMessageID string
}

type Request struct {
	ID                      string             `json:"id"`
	BotID                   string             `json:"bot_id"`
	SessionID               string             `json:"session_id"`
	RouteID                 string             `json:"route_id,omitempty"`
	ChannelIdentityID       string             `json:"channel_identity_id,omitempty"`
	WorkspaceTargetID       string             `json:"workspace_target_id,omitempty"`
	ToolCallID              string             `json:"tool_call_id"`
	ToolName                string             `json:"tool_name"`
	Operation               string             `json:"operation"`
	ToolInput               map[string]any     `json:"tool_input,omitempty"`
	ShortID                 int                `json:"short_id"`
	Status                  string             `json:"status"`
	DecisionReason          string             `json:"decision_reason,omitempty"`
	PromptExternalMessageID string             `json:"prompt_external_message_id,omitempty"`
	SourcePlatform          string             `json:"source_platform,omitempty"`
	ReplyTarget             string             `json:"reply_target,omitempty"`
	ConversationType        string             `json:"conversation_type,omitempty"`
	CreatedAt               time.Time          `json:"created_at"`
	DecidedAt               *time.Time         `json:"decided_at,omitempty"`
	DecidedByUser           bool               `json:"decided_by_user,omitempty"`
	ExecutionLocation       *ExecutionLocation `json:"execution_location,omitempty"`
	RuntimeFenced           bool               `json:"-"`
}
