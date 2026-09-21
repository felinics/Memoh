package bots

import (
	"context"
	"time"
)

// Bot represents a bot entity.
type Bot struct {
	ID              string `json:"id"`
	OwnerUserID     string `json:"owner_user_id"`
	Name            string `json:"name"`
	DisplayName     string `json:"display_name"`
	AvatarURL       string `json:"avatar_url,omitempty"`
	Timezone        string `json:"timezone,omitempty"`
	IsActive        bool   `json:"is_active"`
	Status          string `json:"status"`
	CheckState      string `json:"check_state"`
	CheckIssueCount int32  `json:"check_issue_count"`
	// CurrentUserPermissions lists the effective access permissions of the
	// requesting user on this bot (e.g. "chat", "manage"). It is populated by
	// the API layer per request and is not persisted.
	CurrentUserPermissions []string       `json:"current_user_permissions,omitempty"`
	Metadata               map[string]any `json:"metadata,omitempty"`
	CreatedAt              time.Time      `json:"created_at"`
	UpdatedAt              time.Time      `json:"updated_at"`
}

// BotCheck represents one resource check row for a bot.
type BotCheck struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	TitleKey string         `json:"title_key"`
	Subtitle string         `json:"subtitle,omitempty"`
	Status   string         `json:"status"`
	Summary  string         `json:"summary"`
	Detail   string         `json:"detail,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// CreateBotRequest is the input for creating a bot.
type CreateBotRequest struct {
	Name          string         `json:"name,omitempty"`
	DisplayName   string         `json:"display_name,omitempty"`
	AvatarURL     string         `json:"avatar_url,omitempty"`
	Timezone      *string        `json:"timezone,omitempty"`
	IsActive      *bool          `json:"is_active,omitempty"`
	AclPreset     string         `json:"acl_preset,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	WaitForReady  bool           `json:"wait_for_ready,omitempty"`
	SkipLifecycle bool           `json:"-"`
}

// UpdateBotRequest is the input for updating a bot.
type UpdateBotRequest struct {
	Name        *string        `json:"name,omitempty"`
	DisplayName *string        `json:"display_name,omitempty"`
	AvatarURL   *string        `json:"avatar_url,omitempty"`
	Timezone    *string        `json:"timezone,omitempty"`
	IsActive    *bool          `json:"is_active,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// NameAvailability describes whether a bot name can be used.
type NameAvailability struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// TransferBotRequest is the input for transferring bot ownership.
type TransferBotRequest struct {
	OwnerUserID string `json:"owner_user_id"`
}

// ListBotsResponse wraps a list of bots.
type ListBotsResponse struct {
	Items []Bot `json:"items"`
}

// ListChecksResponse wraps a list of bot checks.
type ListChecksResponse struct {
	Items []BotCheck `json:"items"`
}

// WorkspaceIntents records the desired workspace state of a bot; the
// botworkspace reconciler converges the actual workspace toward it and derives
// bots.status. The bots service never drives provisioning itself.
type WorkspaceIntents interface {
	// EnsurePresent asks for a running workspace built from image (empty keeps
	// the previous or default image) and returns the intent generation.
	EnsurePresent(ctx context.Context, botID, image string) (int64, error)
	// RequestAbsent asks for the workspace to be removed, optionally exporting
	// its data first, and returns the intent generation.
	RequestAbsent(ctx context.Context, botID string, preserve bool) (int64, error)
	// AwaitSettled blocks until the workspace has answered the given intent
	// generation (running, stopped, failed, or absent).
	AwaitSettled(ctx context.Context, botID string, generation int64) (WorkspaceOutcome, error)
	// Current returns the latest observation; ok is false when the bot has no
	// workspace row.
	Current(ctx context.Context, botID string) (WorkspaceOutcome, bool, error)
}

// WorkspaceOutcome is the bots-facing projection of a workspace observation.
type WorkspaceOutcome struct {
	Desired        string
	Observed       string
	LastError      string
	LastErrorPhase string
	EverReady      bool
}

// Workspace observation values the bots service branches on.
const (
	WorkspaceObservedAbsent  = "absent"
	WorkspaceObservedRunning = "running"
	WorkspaceObservedFailed  = "failed"
	WorkspacePhaseBootstrap  = "bootstrap"
)

// ConnectorLifecycle removes external connector credentials before the local
// bot row and its bindings are deleted.
type ConnectorLifecycle interface {
	CleanupBotConnectors(ctx context.Context, botID string) error
}

// RuntimeChecker produces runtime check items for a bot.
type RuntimeChecker interface {
	// ListChecks evaluates dynamic runtime checks for a bot.
	ListChecks(ctx context.Context, botID string) []BotCheck
}

const (
	BotStatusCreating = "creating"
	BotStatusReady    = "ready"
	BotStatusDeleting = "deleting"
	// BotStatusFailed marks a bot whose workspace never became ready. It is
	// derived by the workspace reconciler; the bot stays visible so the user
	// can retry the workspace or delete the bot.
	BotStatusFailed = "failed"
)

const (
	BotCheckStateOK      = "ok"
	BotCheckStateIssue   = "issue"
	BotCheckStateUnknown = "unknown"
)

const (
	BotCheckStatusOK      = "ok"
	BotCheckStatusWarn    = "warn"
	BotCheckStatusError   = "error"
	BotCheckStatusUnknown = "unknown"
)

const (
	BotCheckTypeContainerInit   = "container.init"
	BotCheckTypeContainerRecord = "container.record"
	BotCheckTypeContainerTask   = "container.task"
	BotCheckTypeContainerData   = "container.data_path"
	BotCheckTypeDelete          = "bot.delete"
	BotCheckTypeMCPConnection   = "mcp.connection"
	BotCheckTypeChannelConn     = "channel.connection"
)
