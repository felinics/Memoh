package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/memohai/memoh/domains/api/setting"
	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
	userruntime "github.com/memohai/memoh/domains/runtime/client"
)

const (
	WorkspaceTargetNative = "native"
	WorkspaceTargetRemote = "remote"

	WorkspaceTargetStatusOnline               = "online"
	WorkspaceTargetStatusOffline              = "offline"
	WorkspaceTargetStatusRevoked              = "revoked"
	WorkspaceTargetStatusOwnerMismatch        = "owner_mismatch"
	WorkspaceTargetStatusClientUpdateRequired = "client_update_required"
)

var (
	ErrWorkspaceTargetNotFound          = errors.New("workspace target not found")
	ErrRemoteWorkspaceNotBound          = errors.New("remote workspace is not bound")
	ErrRemoteRuntimeNotUsable           = errors.New("remote runtime not found, revoked, or owned by another user")
	ErrRemoteRuntimeOffline             = errors.New("remote runtime is offline")
	ErrRemoteRuntimeRevoked             = errors.New("remote runtime has been revoked")
	ErrRemoteRuntimeOwnerMismatch       = errors.New("remote runtime no longer belongs to the bot owner")
	ErrRemoteRuntimeClientUpdateNeeded  = errors.New("remote runtime client must be updated")
	ErrInvalidWorkspaceToolApprovalMode = errors.New("invalid workspace tool approval mode")
)

type WorkspaceTargetToolApproval struct {
	Read  setting.ToolApprovalMode `json:"read"`
	Write setting.ToolApprovalMode `json:"write"`
	Exec  setting.ToolApprovalMode `json:"exec"`
}

// WorkspaceTarget is the aggregate shape consumed by clients. Callers address
// mounts by TargetID; RuntimeID identifies the backing remote Runtime and is
// empty for the Native target.
type WorkspaceTarget struct {
	TargetID           string                      `json:"target_id"`
	Kind               string                      `json:"kind"`
	RuntimeID          string                      `json:"runtime_id,omitempty"`
	Name               string                      `json:"name"`
	Primary            bool                        `json:"primary"`
	Online             bool                        `json:"online"`
	Status             string                      `json:"status"`
	ToolApproval       WorkspaceTargetToolApproval `json:"tool_approval"`
	ToolApprovalConfig setting.ToolApprovalConfig  `json:"tool_approval_config"`
}

type WorkspaceTargetsResponse struct {
	Targets []WorkspaceTarget `json:"targets"`
}

type SetPrimaryWorkspaceTargetRequest struct {
	TargetID string `json:"target_id" validate:"required"`
}

type UpdateWorkspaceTargetToolApprovalRequest struct {
	Enabled            *bool                       `json:"enabled,omitempty"`
	Read               setting.ToolApprovalMode    `json:"read,omitempty"`
	Write              setting.ToolApprovalMode    `json:"write,omitempty"`
	Exec               setting.ToolApprovalMode    `json:"exec,omitempty"`
	ToolApprovalConfig *setting.ToolApprovalConfig `json:"tool_approval_config,omitempty"`
}

type ResolvedWorkspaceTarget struct {
	TargetID string
	Kind     string
	Name     string
	Primary  bool
	Client   *bridge.Client
	Info     bridge.WorkspaceInfo
	Approval setting.ToolApprovalConfig
}

// RemoteService owns persistent remote mounts. Live runtime connections remain
// owned by the reverse user-runtime Service.
type RemoteService interface {
	Mount(ctx context.Context, botID, runtimeID string) (WorkspaceTarget, error)
	ListMounts(ctx context.Context, botID string) ([]WorkspaceTarget, error)
	GetMount(ctx context.Context, botID, targetID string) (WorkspaceTarget, error)
	GetPrimaryMount(ctx context.Context, botID string) (WorkspaceTarget, error)
	SetPrimary(ctx context.Context, botID, targetID string) error
	UpdateToolApproval(ctx context.Context, botID, targetID string, modes WorkspaceTargetToolApproval) error
	UpdateToolApprovalConfig(ctx context.Context, botID, targetID string, config setting.ToolApprovalConfig) error
	DeleteMount(ctx context.Context, botID, targetID string) error
	ResolveMount(ctx context.Context, botID, targetID string) (ResolvedWorkspaceTarget, error)
	ResolvePrimary(ctx context.Context, botID string) (ResolvedWorkspaceTarget, bool, error)
	EnsurePrimaryReady(ctx context.Context, botID string) (bool, error)
}

func DefaultRemoteToolApprovalConfig() setting.ToolApprovalConfig {
	config := setting.ToolApprovalConfig{
		Enabled: true,
		Read: setting.ToolApprovalFilePolicy{
			Mode: setting.ToolApprovalAllow, BypassGlobs: []string{}, ForceReviewGlobs: []string{},
		},
		Write: setting.ToolApprovalFilePolicy{
			Mode: setting.ToolApprovalAsk, BypassGlobs: []string{}, ForceReviewGlobs: []string{},
		},
		Exec: setting.ToolApprovalExecPolicy{
			Mode: setting.ToolApprovalAsk, BypassCommands: []string{}, ForceReviewCommands: []string{},
		},
	}
	return setting.NormalizeToolApprovalConfig(config)
}

func WorkspaceToolApprovalModes(config setting.ToolApprovalConfig) WorkspaceTargetToolApproval {
	if !config.Enabled && config.Read.Mode == "" && config.Write.Mode == "" && config.Exec.Mode == "" {
		return WorkspaceTargetToolApproval{Read: setting.ToolApprovalAllow, Write: setting.ToolApprovalAllow, Exec: setting.ToolApprovalAllow}
	}
	return WorkspaceTargetToolApproval{
		Read:  effectiveFileApprovalMode(config.Read),
		Write: effectiveFileApprovalMode(config.Write),
		Exec:  effectiveExecApprovalMode(config.Exec),
	}
}

func ApplyWorkspaceToolApprovalModes(config setting.ToolApprovalConfig, modes WorkspaceTargetToolApproval) (setting.ToolApprovalConfig, error) {
	if !validToolApprovalMode(modes.Read) || !validToolApprovalMode(modes.Write) || !validToolApprovalMode(modes.Exec) {
		return setting.ToolApprovalConfig{}, ErrInvalidWorkspaceToolApprovalMode
	}
	config.Read.Mode = modes.Read
	config.Write.Mode = modes.Write
	config.Exec.Mode = modes.Exec
	return setting.NormalizeToolApprovalConfig(config), nil
}

func effectiveFileApprovalMode(policy setting.ToolApprovalFilePolicy) setting.ToolApprovalMode {
	if validToolApprovalMode(policy.Mode) {
		return policy.Mode
	}
	if policy.RequireApproval {
		return setting.ToolApprovalAsk
	}
	return setting.ToolApprovalAllow
}

func effectiveExecApprovalMode(policy setting.ToolApprovalExecPolicy) setting.ToolApprovalMode {
	if validToolApprovalMode(policy.Mode) {
		return policy.Mode
	}
	if policy.RequireApproval {
		return setting.ToolApprovalAsk
	}
	return setting.ToolApprovalAllow
}

func validToolApprovalMode(mode setting.ToolApprovalMode) bool {
	return mode == setting.ToolApprovalAllow || mode == setting.ToolApprovalAsk || mode == setting.ToolApprovalDeny
}

// CanonicalWorkspaceUUID normalizes a workspace target/runtime UUID.
func CanonicalWorkspaceUUID(value string) (string, bool) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", false
	}
	return id.String(), true
}

// SupportsRemoteWorkspace reports whether reverse-runtime capabilities include
// FS+Exec+HostFS required for remote workspace mounts.
func SupportsRemoteWorkspace(capabilities []string) bool {
	return slices.Contains(capabilities, userruntime.CapabilityFS) &&
		slices.Contains(capabilities, userruntime.CapabilityExec) &&
		slices.Contains(capabilities, userruntime.CapabilityHostFS)
}

// ToolApprovalConfigFromRaw decodes persisted tool-approval JSON with defaults.
func ToolApprovalConfigFromRaw(raw []byte) setting.ToolApprovalConfig {
	if len(raw) == 0 {
		return DefaultRemoteToolApprovalConfig()
	}
	var config setting.ToolApprovalConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return DefaultRemoteToolApprovalConfig()
	}
	return setting.NormalizeToolApprovalConfig(config)
}
