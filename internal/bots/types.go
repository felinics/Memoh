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

// ContainerLifecycle handles container lifecycle events bound to bot operations.
type ContainerLifecycle interface {
	SetupBotContainer(ctx context.Context, botID string) error
	CleanupBotContainer(ctx context.Context, botID string, preserveData bool) error
}

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
