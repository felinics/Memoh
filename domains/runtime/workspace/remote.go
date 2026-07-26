package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/google/uuid"

	settingpersistence "github.com/memohai/memoh/domains/api/bot/setting/persistence"
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
	Read  settingpersistence.ToolApprovalMode `json:"read"`
	Write settingpersistence.ToolApprovalMode `json:"write"`
	Exec  settingpersistence.ToolApprovalMode `json:"exec"`
}

// WorkspaceTarget is the aggregate shape consumed by clients. Callers address
// mounts by TargetID; RuntimeID identifies the backing remote Runtime and is
// empty for the Native target.
type WorkspaceTarget struct {
	TargetID           string                                `json:"target_id"`
	Kind               string                                `json:"kind"`
	RuntimeID          string                                `json:"runtime_id,omitempty"`
	Name               string                                `json:"name"`
	Primary            bool                                  `json:"primary"`
	Online             bool                                  `json:"online"`
	Status             string                                `json:"status"`
	ToolApproval       WorkspaceTargetToolApproval           `json:"tool_approval"`
	ToolApprovalConfig settingpersistence.ToolApprovalConfig `json:"tool_approval_config"`
}

type WorkspaceTargetsResponse struct {
	Targets []WorkspaceTarget `json:"targets"`
}

type SetPrimaryWorkspaceTargetRequest struct {
	TargetID string `json:"target_id" validate:"required"`
}

type UpdateWorkspaceTargetToolApprovalRequest struct {
	Enabled            *bool                                  `json:"enabled,omitempty"`
	Read               settingpersistence.ToolApprovalMode    `json:"read,omitempty"`
	Write              settingpersistence.ToolApprovalMode    `json:"write,omitempty"`
	Exec               settingpersistence.ToolApprovalMode    `json:"exec,omitempty"`
	ToolApprovalConfig *settingpersistence.ToolApprovalConfig `json:"tool_approval_config,omitempty"`
}

type ResolvedWorkspaceTarget struct {
	TargetID string
	Kind     string
	Name     string
	Primary  bool
	Client   *bridge.Client
	Info     bridge.WorkspaceInfo
	Approval settingpersistence.ToolApprovalConfig
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
	UpdateToolApprovalConfig(ctx context.Context, botID, targetID string, config settingpersistence.ToolApprovalConfig) error
	DeleteMount(ctx context.Context, botID, targetID string) error
	ResolveMount(ctx context.Context, botID, targetID string) (ResolvedWorkspaceTarget, error)
	ResolvePrimary(ctx context.Context, botID string) (ResolvedWorkspaceTarget, bool, error)
	EnsurePrimaryReady(ctx context.Context, botID string) (bool, error)
}

func DefaultRemoteToolApprovalConfig() settingpersistence.ToolApprovalConfig {
	config := settingpersistence.ToolApprovalConfig{
		Enabled: true,
		Read: settingpersistence.ToolApprovalFilePolicy{
			Mode: settingpersistence.ToolApprovalAllow, BypassGlobs: []string{}, ForceReviewGlobs: []string{},
		},
		Write: settingpersistence.ToolApprovalFilePolicy{
			Mode: settingpersistence.ToolApprovalAsk, BypassGlobs: []string{}, ForceReviewGlobs: []string{},
		},
		Exec: settingpersistence.ToolApprovalExecPolicy{
			Mode: settingpersistence.ToolApprovalAsk, BypassCommands: []string{}, ForceReviewCommands: []string{},
		},
	}
	return settingpersistence.NormalizeToolApprovalConfig(config)
}

func WorkspaceToolApprovalModes(config settingpersistence.ToolApprovalConfig) WorkspaceTargetToolApproval {
	if !config.Enabled && config.Read.Mode == "" && config.Write.Mode == "" && config.Exec.Mode == "" {
		return WorkspaceTargetToolApproval{Read: settingpersistence.ToolApprovalAllow, Write: settingpersistence.ToolApprovalAllow, Exec: settingpersistence.ToolApprovalAllow}
	}
	return WorkspaceTargetToolApproval{
		Read:  effectiveFileApprovalMode(config.Read),
		Write: effectiveFileApprovalMode(config.Write),
		Exec:  effectiveExecApprovalMode(config.Exec),
	}
}

func ApplyWorkspaceToolApprovalModes(config settingpersistence.ToolApprovalConfig, modes WorkspaceTargetToolApproval) (settingpersistence.ToolApprovalConfig, error) {
	if !validToolApprovalMode(modes.Read) || !validToolApprovalMode(modes.Write) || !validToolApprovalMode(modes.Exec) {
		return settingpersistence.ToolApprovalConfig{}, ErrInvalidWorkspaceToolApprovalMode
	}
	config.Read.Mode = modes.Read
	config.Write.Mode = modes.Write
	config.Exec.Mode = modes.Exec
	return settingpersistence.NormalizeToolApprovalConfig(config), nil
}

func effectiveFileApprovalMode(policy settingpersistence.ToolApprovalFilePolicy) settingpersistence.ToolApprovalMode {
	if validToolApprovalMode(policy.Mode) {
		return policy.Mode
	}
	if policy.RequireApproval {
		return settingpersistence.ToolApprovalAsk
	}
	return settingpersistence.ToolApprovalAllow
}

func effectiveExecApprovalMode(policy settingpersistence.ToolApprovalExecPolicy) settingpersistence.ToolApprovalMode {
	if validToolApprovalMode(policy.Mode) {
		return policy.Mode
	}
	if policy.RequireApproval {
		return settingpersistence.ToolApprovalAsk
	}
	return settingpersistence.ToolApprovalAllow
}

func validToolApprovalMode(mode settingpersistence.ToolApprovalMode) bool {
	return mode == settingpersistence.ToolApprovalAllow || mode == settingpersistence.ToolApprovalAsk || mode == settingpersistence.ToolApprovalDeny
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
func ToolApprovalConfigFromRaw(raw []byte) settingpersistence.ToolApprovalConfig {
	if len(raw) == 0 {
		return DefaultRemoteToolApprovalConfig()
	}
	var config settingpersistence.ToolApprovalConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return DefaultRemoteToolApprovalConfig()
	}
	return settingpersistence.NormalizeToolApprovalConfig(config)
}
