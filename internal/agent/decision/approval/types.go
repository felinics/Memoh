package approval

import (
	"context"
	"errors"
	"strings"
	"time"
)

const (
	StatusPending   = "pending"
	StatusApproved  = "approved"
	StatusRejected  = "rejected"
	StatusExpired   = "expired"
	StatusCancelled = "cancelled"

	OperationRead       = "read"
	OperationWrite      = "write"
	OperationExec       = "exec"
	OperationPermission = "permission"

	// Option kinds mirror the ACP PermissionOptionKind vocabulary. They are
	// stored verbatim so a decision can be validated against the agent's
	// offered options and the original option id returned to the agent.
	OptionKindAllowOnce    = "allow_once"
	OptionKindAllowAlways  = "allow_always"
	OptionKindRejectOnce   = "reject_once"
	OptionKindRejectAlways = "reject_always"

	DecisionBypass        = "bypass"
	DecisionNeedsApproval = "needs_approval"
	DecisionDeny          = "deny"

	PolicyDeniedReason = "tool execution denied by policy"

	ExecutionLocationMetadataKey = "execution_location"
)

var (
	ErrNotFound          = errors.New("tool approval request not found")
	ErrAlreadyDecided    = errors.New("tool approval request already decided")
	ErrForbidden         = errors.New("tool approval forbidden")
	ErrAmbiguous         = errors.New("tool approval request is ambiguous")
	ErrOptionUnavailable = errors.New("tool approval option is unavailable")
)

// PermissionOption is one agent-provided answer to a permission request. The
// agent's id, display name, and kind are preserved verbatim: the user picks
// among them and the agent receives its own option id back, so agent-side
// semantics (once vs session vs always scopes) survive the round trip without
// Memoh interpreting them.
type PermissionOption struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Kind string `json:"kind,omitempty"`
}

// Approves reports whether selecting this option grants the request.
func (o PermissionOption) Approves() bool {
	switch strings.ToLower(strings.TrimSpace(o.Kind)) {
	case OptionKindAllowOnce, OptionKindAllowAlways:
		return true
	default:
		return false
	}
}

// FindOption returns the option with the given id.
func FindOption(options []PermissionOption, id string) (PermissionOption, bool) {
	if id == "" {
		return PermissionOption{}, false
	}
	for _, option := range options {
		if option.ID == id {
			return option, true
		}
	}
	return PermissionOption{}, false
}

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
	Options                      []PermissionOption
	// ForceReview routes the request to the user even when the approval
	// policy is disabled. Set for consent-semantic requests (MCP elicitation
	// fallbacks): auto-answering one would report a consent no user gave.
	ForceReview       bool
	SourcePlatform    string
	ReplyTarget       string
	ConversationType  string
	WorkspaceTargeted bool
	ExecutionLocation *ExecutionLocation
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
	Options                 []PermissionOption `json:"options,omitempty"`
	SelectedOptionID        string             `json:"selected_option_id,omitempty"`
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
