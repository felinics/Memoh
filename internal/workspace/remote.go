package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/memohai/memoh/internal/db"
	dbstore "github.com/memohai/memoh/internal/db/store"
	"github.com/memohai/memoh/internal/settings"
	"github.com/memohai/memoh/internal/userruntime"
	"github.com/memohai/memoh/internal/workspace/bridge"
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
	Read  settings.ToolApprovalMode `json:"read"`
	Write settings.ToolApprovalMode `json:"write"`
	Exec  settings.ToolApprovalMode `json:"exec"`
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
	ToolApprovalConfig settings.ToolApprovalConfig `json:"tool_approval_config"`
}

type WorkspaceTargetsResponse struct {
	Targets []WorkspaceTarget `json:"targets"`
}

type SetPrimaryWorkspaceTargetRequest struct {
	TargetID string `json:"target_id" validate:"required"`
}

type UpdateWorkspaceTargetToolApprovalRequest struct {
	Enabled            *bool                        `json:"enabled,omitempty"`
	Read               settings.ToolApprovalMode    `json:"read,omitempty"`
	Write              settings.ToolApprovalMode    `json:"write,omitempty"`
	Exec               settings.ToolApprovalMode    `json:"exec,omitempty"`
	ToolApprovalConfig *settings.ToolApprovalConfig `json:"tool_approval_config,omitempty"`
}

type ResolvedWorkspaceTarget struct {
	TargetID string
	Kind     string
	Name     string
	Primary  bool
	Client   *bridge.Client
	Info     bridge.WorkspaceInfo
	Approval settings.ToolApprovalConfig
}

// WorkspaceTargetDescriptor contains request-routing metadata without opening
// the target's filesystem client. Agent turns use this shape until a workspace
// tool actually needs the runtime.
type WorkspaceTargetDescriptor struct {
	TargetID string
	Kind     string
	Name     string
	Primary  bool
	Info     bridge.WorkspaceInfo
	Approval settings.ToolApprovalConfig
}

// RemoteWorkspaceService owns persistent remote mounts. Live runtime
// connections remain owned by userruntime.Service.
type RemoteWorkspaceService struct {
	store    dbstore.BotRemoteRuntimeBindingStore
	runtimes runtimeConnectionResolver
}

type runtimeConnectionResolver interface {
	Connection(runtimeID string) (*userruntime.Connection, bool)
}

func NewRemoteWorkspaceService(store dbstore.BotRemoteRuntimeBindingStore, runtimes *userruntime.Service) *RemoteWorkspaceService {
	return &RemoteWorkspaceService{store: store, runtimes: runtimes}
}

func (s *RemoteWorkspaceService) Mount(ctx context.Context, botID, runtimeID string) (WorkspaceTarget, error) {
	if s == nil || s.store == nil {
		return WorkspaceTarget{}, errors.New("remote workspace service not configured")
	}
	botID, ok := canonicalWorkspaceUUID(botID)
	if !ok {
		return WorkspaceTarget{}, userruntime.ErrInvalidInput
	}
	runtimeID, ok = canonicalWorkspaceUUID(runtimeID)
	if !ok {
		return WorkspaceTarget{}, userruntime.ErrInvalidInput
	}
	record, err := s.store.CreateOrUpdateMount(ctx, botID, runtimeID)
	if errors.Is(err, db.ErrNotFound) {
		return WorkspaceTarget{}, ErrRemoteRuntimeNotUsable
	}
	if err != nil {
		return WorkspaceTarget{}, err
	}
	return s.target(record), nil
}

func (s *RemoteWorkspaceService) ListMounts(ctx context.Context, botID string) ([]WorkspaceTarget, error) {
	records, err := s.listRecords(ctx, botID)
	if err != nil {
		return nil, err
	}
	targets := make([]WorkspaceTarget, 0, len(records))
	for _, record := range records {
		targets = append(targets, s.target(record))
	}
	return targets, nil
}

func (s *RemoteWorkspaceService) GetMount(ctx context.Context, botID, targetID string) (WorkspaceTarget, error) {
	record, err := s.getRecord(ctx, botID, targetID)
	if err != nil {
		return WorkspaceTarget{}, err
	}
	return s.target(record), nil
}

func (s *RemoteWorkspaceService) GetPrimaryMount(ctx context.Context, botID string) (WorkspaceTarget, error) {
	record, err := s.getPrimaryRecord(ctx, botID)
	if err != nil {
		return WorkspaceTarget{}, err
	}
	return s.target(record), nil
}

func (s *RemoteWorkspaceService) SetPrimary(ctx context.Context, botID, targetID string) error {
	if s == nil || s.store == nil {
		return errors.New("remote workspace service not configured")
	}
	botID, ok := canonicalWorkspaceUUID(botID)
	if !ok {
		return userruntime.ErrInvalidInput
	}
	targetID = strings.TrimSpace(targetID)
	if targetID == WorkspaceTargetNative {
		return s.store.SetPrimary(ctx, botID, WorkspaceTargetNative)
	}
	record, err := s.getRecord(ctx, botID, targetID)
	if err != nil {
		return err
	}
	if record.RuntimeUserID != record.BotOwnerUserID {
		return ErrRemoteRuntimeOwnerMismatch
	}
	if record.RuntimeRevoked {
		return ErrRemoteRuntimeRevoked
	}
	return s.store.SetPrimary(ctx, botID, record.ID)
}

func (s *RemoteWorkspaceService) UpdateToolApproval(ctx context.Context, botID, targetID string, modes WorkspaceTargetToolApproval) error {
	record, err := s.getRecord(ctx, botID, targetID)
	if err != nil {
		return err
	}
	config, err := ApplyWorkspaceToolApprovalModes(toolApprovalConfig(record.ToolApproval), modes)
	if err != nil {
		return err
	}
	return s.updateToolApprovalConfig(ctx, record, config)
}

func (s *RemoteWorkspaceService) UpdateToolApprovalConfig(ctx context.Context, botID, targetID string, config settings.ToolApprovalConfig) error {
	if s == nil || s.store == nil {
		return errors.New("remote workspace service not configured")
	}
	record, err := s.getRecord(ctx, botID, targetID)
	if err != nil {
		return err
	}
	return s.updateToolApprovalConfig(ctx, record, settings.NormalizeToolApprovalConfig(config))
}

func (s *RemoteWorkspaceService) updateToolApprovalConfig(ctx context.Context, record dbstore.BotRemoteRuntimeBindingRecord, config settings.ToolApprovalConfig) error {
	raw, err := json.Marshal(config)
	if err != nil {
		return err
	}
	return s.store.UpdateToolApproval(ctx, record.BotID, record.ID, raw)
}

func (s *RemoteWorkspaceService) DeleteMount(ctx context.Context, botID, targetID string) error {
	if s == nil || s.store == nil {
		return errors.New("remote workspace service not configured")
	}
	botID, ok := canonicalWorkspaceUUID(botID)
	if !ok {
		return ErrWorkspaceTargetNotFound
	}
	targetID, ok = canonicalWorkspaceUUID(targetID)
	if !ok {
		return ErrWorkspaceTargetNotFound
	}
	if err := s.store.DeleteMount(ctx, botID, targetID); errors.Is(err, db.ErrNotFound) {
		return ErrWorkspaceTargetNotFound
	} else {
		return err
	}
}

func (s *RemoteWorkspaceService) ResolveMount(ctx context.Context, botID, targetID string) (ResolvedWorkspaceTarget, error) {
	record, err := s.getRecord(ctx, botID, targetID)
	if err != nil {
		return ResolvedWorkspaceTarget{}, err
	}
	return s.resolveRecord(record)
}

func (s *RemoteWorkspaceService) ResolveMountDescriptor(ctx context.Context, botID, targetID string) (WorkspaceTargetDescriptor, error) {
	record, err := s.getRecord(ctx, botID, targetID)
	if err != nil {
		return WorkspaceTargetDescriptor{}, err
	}
	return s.describeRecord(record)
}

func (s *RemoteWorkspaceService) resolveRecord(record dbstore.BotRemoteRuntimeBindingRecord) (ResolvedWorkspaceTarget, error) {
	client, connection, err := s.clientForRecord(record)
	if err != nil {
		return ResolvedWorkspaceTarget{}, err
	}
	return ResolvedWorkspaceTarget{
		TargetID: record.ID,
		Kind:     WorkspaceTargetRemote,
		Name:     record.RuntimeName,
		Primary:  record.IsPrimary,
		Client:   client,
		Info: bridge.WorkspaceInfo{
			Backend:        bridge.WorkspaceBackendRemote,
			OS:             connection.Info.OS,
			DefaultWorkDir: connection.Info.WorkspaceBase,
		},
		Approval: toolApprovalConfig(record.ToolApproval),
	}, nil
}

func (s *RemoteWorkspaceService) describeRecord(record dbstore.BotRemoteRuntimeBindingRecord) (WorkspaceTargetDescriptor, error) {
	if record.RuntimeUserID != record.BotOwnerUserID {
		return WorkspaceTargetDescriptor{}, ErrRemoteRuntimeOwnerMismatch
	}
	if record.RuntimeRevoked {
		return WorkspaceTargetDescriptor{}, ErrRemoteRuntimeRevoked
	}
	info := bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendRemote}
	if s.runtimes != nil {
		if connection, ok := s.runtimes.Connection(record.RuntimeID); ok && connection != nil {
			info.OS = connection.Info.OS
			info.DefaultWorkDir = connection.Info.WorkspaceBase
		}
	}
	return WorkspaceTargetDescriptor{
		TargetID: record.ID,
		Kind:     WorkspaceTargetRemote,
		Name:     record.RuntimeName,
		Primary:  record.IsPrimary,
		Info:     info,
		Approval: toolApprovalConfig(record.ToolApproval),
	}, nil
}

func (s *RemoteWorkspaceService) ResolvePrimary(ctx context.Context, botID string) (ResolvedWorkspaceTarget, bool, error) {
	record, err := s.getPrimaryRecord(ctx, botID)
	if errors.Is(err, ErrRemoteWorkspaceNotBound) {
		return ResolvedWorkspaceTarget{}, false, nil
	}
	if err != nil {
		return ResolvedWorkspaceTarget{}, false, err
	}
	target, err := s.resolveRecord(record)
	return target, true, err
}

func (s *RemoteWorkspaceService) ResolvePrimaryDescriptor(ctx context.Context, botID string) (WorkspaceTargetDescriptor, bool, error) {
	record, err := s.getPrimaryRecord(ctx, botID)
	if errors.Is(err, ErrRemoteWorkspaceNotBound) {
		return WorkspaceTargetDescriptor{}, false, nil
	}
	if err != nil {
		return WorkspaceTargetDescriptor{}, false, err
	}
	target, err := s.describeRecord(record)
	return target, true, err
}

func (s *RemoteWorkspaceService) EnsurePrimaryReady(ctx context.Context, botID string) (bool, error) {
	target, primary, err := s.ResolvePrimary(ctx, botID)
	if err != nil || !primary {
		return primary, err
	}
	entry, err := target.Client.Stat(ctx, target.Info.DefaultWorkDir)
	if err != nil {
		return true, fmt.Errorf("check remote workspace: %w", err)
	}
	if entry == nil || !entry.GetIsDir() {
		return true, errors.New("remote workspace root is not a directory")
	}
	return true, nil
}

func (s *RemoteWorkspaceService) listRecords(ctx context.Context, botID string) ([]dbstore.BotRemoteRuntimeBindingRecord, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	botID, ok := canonicalWorkspaceUUID(botID)
	if !ok {
		return nil, userruntime.ErrInvalidInput
	}
	return s.store.ListMounts(ctx, botID)
}

func (s *RemoteWorkspaceService) getRecord(ctx context.Context, botID, targetID string) (dbstore.BotRemoteRuntimeBindingRecord, error) {
	if s == nil || s.store == nil {
		return dbstore.BotRemoteRuntimeBindingRecord{}, ErrWorkspaceTargetNotFound
	}
	botID, botOK := canonicalWorkspaceUUID(botID)
	targetID, targetOK := canonicalWorkspaceUUID(targetID)
	if !botOK || !targetOK {
		return dbstore.BotRemoteRuntimeBindingRecord{}, ErrWorkspaceTargetNotFound
	}
	record, err := s.store.GetMount(ctx, botID, targetID)
	if errors.Is(err, db.ErrNotFound) {
		return dbstore.BotRemoteRuntimeBindingRecord{}, ErrWorkspaceTargetNotFound
	}
	return record, err
}

func (s *RemoteWorkspaceService) getPrimaryRecord(ctx context.Context, botID string) (dbstore.BotRemoteRuntimeBindingRecord, error) {
	if s == nil || s.store == nil {
		return dbstore.BotRemoteRuntimeBindingRecord{}, ErrRemoteWorkspaceNotBound
	}
	botID, ok := canonicalWorkspaceUUID(botID)
	if !ok {
		return dbstore.BotRemoteRuntimeBindingRecord{}, userruntime.ErrInvalidInput
	}
	record, err := s.store.GetPrimaryMount(ctx, botID)
	if errors.Is(err, db.ErrNotFound) {
		return dbstore.BotRemoteRuntimeBindingRecord{}, ErrRemoteWorkspaceNotBound
	}
	return record, err
}

func (s *RemoteWorkspaceService) clientForRecord(record dbstore.BotRemoteRuntimeBindingRecord) (*bridge.Client, *userruntime.Connection, error) {
	connection, err := s.connectionForRecord(record)
	if err != nil {
		return nil, nil, err
	}
	return connection.Client, connection, nil
}

func (s *RemoteWorkspaceService) connectionForRecord(record dbstore.BotRemoteRuntimeBindingRecord) (*userruntime.Connection, error) {
	if record.RuntimeUserID != record.BotOwnerUserID {
		return nil, ErrRemoteRuntimeOwnerMismatch
	}
	if record.RuntimeRevoked {
		return nil, ErrRemoteRuntimeRevoked
	}
	if s.runtimes == nil {
		return nil, ErrRemoteRuntimeOffline
	}
	connection, ok := s.runtimes.Connection(record.RuntimeID)
	if !ok || connection == nil || connection.Client == nil {
		return nil, ErrRemoteRuntimeOffline
	}
	if !supportsRemoteWorkspace(connection.Info.Capabilities) {
		return nil, ErrRemoteRuntimeClientUpdateNeeded
	}
	return connection, nil
}

func (s *RemoteWorkspaceService) target(record dbstore.BotRemoteRuntimeBindingRecord) WorkspaceTarget {
	approval := toolApprovalConfig(record.ToolApproval)
	target := WorkspaceTarget{
		TargetID:           record.ID,
		Kind:               WorkspaceTargetRemote,
		RuntimeID:          record.RuntimeID,
		Name:               record.RuntimeName,
		Primary:            record.IsPrimary,
		ToolApproval:       WorkspaceToolApprovalModes(approval),
		ToolApprovalConfig: approval,
	}
	switch {
	case record.RuntimeUserID != record.BotOwnerUserID:
		target.Name = ""
		target.RuntimeID = ""
		target.Status = WorkspaceTargetStatusOwnerMismatch
	case record.RuntimeRevoked:
		target.Status = WorkspaceTargetStatusRevoked
	case s.runtimes == nil:
		target.Status = WorkspaceTargetStatusOffline
	default:
		connection, online := s.runtimes.Connection(record.RuntimeID)
		target.Online = online && connection != nil && connection.Client != nil
		switch {
		case !target.Online:
			target.Status = WorkspaceTargetStatusOffline
		case !supportsRemoteWorkspace(connection.Info.Capabilities):
			target.Status = WorkspaceTargetStatusClientUpdateRequired
		default:
			target.Status = WorkspaceTargetStatusOnline
		}
	}
	return target
}

func DefaultRemoteToolApprovalConfig() settings.ToolApprovalConfig {
	config := settings.ToolApprovalConfig{
		Enabled: true,
		Read: settings.ToolApprovalFilePolicy{
			Mode: settings.ToolApprovalAllow, BypassGlobs: []string{}, ForceReviewGlobs: []string{},
		},
		Write: settings.ToolApprovalFilePolicy{
			Mode: settings.ToolApprovalAsk, BypassGlobs: []string{}, ForceReviewGlobs: []string{},
		},
		Exec: settings.ToolApprovalExecPolicy{
			Mode: settings.ToolApprovalAsk, BypassCommands: []string{}, ForceReviewCommands: []string{},
		},
	}
	return settings.NormalizeToolApprovalConfig(config)
}

func toolApprovalConfig(raw dbstore.JSON) settings.ToolApprovalConfig {
	if len(raw) == 0 {
		return DefaultRemoteToolApprovalConfig()
	}
	var config settings.ToolApprovalConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return DefaultRemoteToolApprovalConfig()
	}
	return settings.NormalizeToolApprovalConfig(config)
}

func WorkspaceToolApprovalModes(config settings.ToolApprovalConfig) WorkspaceTargetToolApproval {
	if !config.Enabled && config.Read.Mode == "" && config.Write.Mode == "" && config.Exec.Mode == "" {
		return WorkspaceTargetToolApproval{Read: settings.ToolApprovalAllow, Write: settings.ToolApprovalAllow, Exec: settings.ToolApprovalAllow}
	}
	return WorkspaceTargetToolApproval{
		Read:  effectiveFileApprovalMode(config.Read),
		Write: effectiveFileApprovalMode(config.Write),
		Exec:  effectiveExecApprovalMode(config.Exec),
	}
}

func ApplyWorkspaceToolApprovalModes(config settings.ToolApprovalConfig, modes WorkspaceTargetToolApproval) (settings.ToolApprovalConfig, error) {
	if !validToolApprovalMode(modes.Read) || !validToolApprovalMode(modes.Write) || !validToolApprovalMode(modes.Exec) {
		return settings.ToolApprovalConfig{}, ErrInvalidWorkspaceToolApprovalMode
	}
	config.Read.Mode = modes.Read
	config.Write.Mode = modes.Write
	config.Exec.Mode = modes.Exec
	return settings.NormalizeToolApprovalConfig(config), nil
}

func effectiveFileApprovalMode(policy settings.ToolApprovalFilePolicy) settings.ToolApprovalMode {
	if validToolApprovalMode(policy.Mode) {
		return policy.Mode
	}
	if policy.RequireApproval {
		return settings.ToolApprovalAsk
	}
	return settings.ToolApprovalAllow
}

func effectiveExecApprovalMode(policy settings.ToolApprovalExecPolicy) settings.ToolApprovalMode {
	if validToolApprovalMode(policy.Mode) {
		return policy.Mode
	}
	if policy.RequireApproval {
		return settings.ToolApprovalAsk
	}
	return settings.ToolApprovalAllow
}

func validToolApprovalMode(mode settings.ToolApprovalMode) bool {
	return mode == settings.ToolApprovalAllow || mode == settings.ToolApprovalAsk || mode == settings.ToolApprovalDeny
}

func canonicalWorkspaceUUID(value string) (string, bool) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", false
	}
	return id.String(), true
}

func supportsRemoteWorkspace(capabilities []string) bool {
	return slices.Contains(capabilities, userruntime.CapabilityFS) &&
		slices.Contains(capabilities, userruntime.CapabilityExec) &&
		slices.Contains(capabilities, userruntime.CapabilityHostFS)
}
