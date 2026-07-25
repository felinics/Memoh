package target

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/memohai/memoh/domains/api/setting"
	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
	userruntime "github.com/memohai/memoh/domains/runtime/client"
	runtimeworkspace "github.com/memohai/memoh/domains/runtime/workspace"
)

var _ runtimeworkspace.RemoteService = (*RemoteWorkspaceService)(nil)

// RemoteWorkspaceService owns persistent remote mounts. Live runtime
// connections remain owned by userruntime.Service.
type RemoteWorkspaceService struct {
	store    runtimeworkspace.RemoteMountStore
	runtimes runtimeConnectionResolver
	owners   runtimeworkspace.BotOwnerReader
}

type runtimeConnectionResolver interface {
	Connection(runtimeID string) (*userruntime.Connection, bool)
}

func NewRemoteWorkspaceService(store runtimeworkspace.RemoteMountStore, runtimes *userruntime.Service, owners runtimeworkspace.BotOwnerReader) *RemoteWorkspaceService {
	return &RemoteWorkspaceService{store: store, runtimes: runtimes, owners: owners}
}

func (s *RemoteWorkspaceService) Mount(ctx context.Context, botID, runtimeID string) (runtimeworkspace.WorkspaceTarget, error) {
	if s == nil || s.store == nil {
		return runtimeworkspace.WorkspaceTarget{}, errors.New("remote workspace service not configured")
	}
	botID, ok := runtimeworkspace.CanonicalWorkspaceUUID(botID)
	if !ok {
		return runtimeworkspace.WorkspaceTarget{}, userruntime.ErrInvalidInput
	}
	runtimeID, ok = runtimeworkspace.CanonicalWorkspaceUUID(runtimeID)
	if !ok {
		return runtimeworkspace.WorkspaceTarget{}, userruntime.ErrInvalidInput
	}
	ownerUserID, err := s.lookupBotOwner(ctx, botID)
	if err != nil {
		return runtimeworkspace.WorkspaceTarget{}, err
	}
	record, err := s.store.CreateOrUpdateMount(ctx, botID, runtimeID, ownerUserID)
	if errors.Is(err, runtimeworkspace.ErrRecordNotFound) {
		return runtimeworkspace.WorkspaceTarget{}, runtimeworkspace.ErrRemoteRuntimeNotUsable
	}
	if err != nil {
		return runtimeworkspace.WorkspaceTarget{}, err
	}
	record.BotOwnerUserID = ownerUserID
	return s.target(record), nil
}

func (s *RemoteWorkspaceService) ListMounts(ctx context.Context, botID string) ([]runtimeworkspace.WorkspaceTarget, error) {
	records, err := s.listRecords(ctx, botID)
	if err != nil {
		return nil, err
	}
	targets := make([]runtimeworkspace.WorkspaceTarget, 0, len(records))
	for _, record := range records {
		targets = append(targets, s.target(record))
	}
	return targets, nil
}

func (s *RemoteWorkspaceService) GetMount(ctx context.Context, botID, targetID string) (runtimeworkspace.WorkspaceTarget, error) {
	record, err := s.getRecord(ctx, botID, targetID)
	if err != nil {
		return runtimeworkspace.WorkspaceTarget{}, err
	}
	return s.target(record), nil
}

func (s *RemoteWorkspaceService) GetPrimaryMount(ctx context.Context, botID string) (runtimeworkspace.WorkspaceTarget, error) {
	record, err := s.getPrimaryRecord(ctx, botID)
	if err != nil {
		return runtimeworkspace.WorkspaceTarget{}, err
	}
	return s.target(record), nil
}

func (s *RemoteWorkspaceService) SetPrimary(ctx context.Context, botID, targetID string) error {
	if s == nil || s.store == nil {
		return errors.New("remote workspace service not configured")
	}
	botID, ok := runtimeworkspace.CanonicalWorkspaceUUID(botID)
	if !ok {
		return userruntime.ErrInvalidInput
	}
	targetID = strings.TrimSpace(targetID)
	if targetID == runtimeworkspace.WorkspaceTargetNative {
		_, err := s.lookupBotOwner(ctx, botID)
		if err != nil {
			return err
		}
		return s.store.SetPrimary(ctx, botID, runtimeworkspace.WorkspaceTargetNative)
	}
	record, err := s.getRecord(ctx, botID, targetID)
	if err != nil {
		return err
	}
	if record.RuntimeUserID != record.BotOwnerUserID {
		return runtimeworkspace.ErrRemoteRuntimeOwnerMismatch
	}
	if record.RuntimeRevoked {
		return runtimeworkspace.ErrRemoteRuntimeRevoked
	}
	return s.store.SetPrimary(ctx, botID, record.ID)
}

func (s *RemoteWorkspaceService) UpdateToolApproval(ctx context.Context, botID, targetID string, modes runtimeworkspace.WorkspaceTargetToolApproval) error {
	record, err := s.getRecord(ctx, botID, targetID)
	if err != nil {
		return err
	}
	config, err := runtimeworkspace.ApplyWorkspaceToolApprovalModes(runtimeworkspace.ToolApprovalConfigFromRaw(record.ToolApproval), modes)
	if err != nil {
		return err
	}
	return s.updateToolApprovalConfig(ctx, record, config)
}

func (s *RemoteWorkspaceService) UpdateToolApprovalConfig(ctx context.Context, botID, targetID string, config setting.ToolApprovalConfig) error {
	if s == nil || s.store == nil {
		return errors.New("remote workspace service not configured")
	}
	record, err := s.getRecord(ctx, botID, targetID)
	if err != nil {
		return err
	}
	return s.updateToolApprovalConfig(ctx, record, setting.NormalizeToolApprovalConfig(config))
}

func (s *RemoteWorkspaceService) updateToolApprovalConfig(ctx context.Context, record runtimeworkspace.RemoteMountRecord, config setting.ToolApprovalConfig) error {
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
	record, err := s.getRecord(ctx, botID, targetID)
	if err != nil {
		return err
	}
	if err := s.store.DeleteMount(ctx, record.BotID, record.ID); errors.Is(err, runtimeworkspace.ErrRecordNotFound) {
		return runtimeworkspace.ErrWorkspaceTargetNotFound
	} else {
		return err
	}
}

func (s *RemoteWorkspaceService) ResolveMount(ctx context.Context, botID, targetID string) (runtimeworkspace.ResolvedWorkspaceTarget, error) {
	record, err := s.getRecord(ctx, botID, targetID)
	if err != nil {
		return runtimeworkspace.ResolvedWorkspaceTarget{}, err
	}
	return s.resolveRecord(record)
}

func (s *RemoteWorkspaceService) resolveRecord(record runtimeworkspace.RemoteMountRecord) (runtimeworkspace.ResolvedWorkspaceTarget, error) {
	client, connection, err := s.clientForRecord(record)
	if err != nil {
		return runtimeworkspace.ResolvedWorkspaceTarget{}, err
	}
	return runtimeworkspace.ResolvedWorkspaceTarget{
		TargetID: record.ID,
		Kind:     runtimeworkspace.WorkspaceTargetRemote,
		Name:     record.RuntimeName,
		Primary:  record.IsPrimary,
		Client:   client,
		Info: bridge.WorkspaceInfo{
			Backend:        bridge.WorkspaceBackendRemote,
			OS:             connection.Info.OS,
			DefaultWorkDir: connection.Info.WorkspaceBase,
		},
		Approval: runtimeworkspace.ToolApprovalConfigFromRaw(record.ToolApproval),
	}, nil
}

func (s *RemoteWorkspaceService) ResolvePrimary(ctx context.Context, botID string) (runtimeworkspace.ResolvedWorkspaceTarget, bool, error) {
	record, err := s.getPrimaryRecord(ctx, botID)
	if errors.Is(err, runtimeworkspace.ErrRemoteWorkspaceNotBound) {
		return runtimeworkspace.ResolvedWorkspaceTarget{}, false, nil
	}
	if err != nil {
		return runtimeworkspace.ResolvedWorkspaceTarget{}, false, err
	}
	target, err := s.resolveRecord(record)
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

func (s *RemoteWorkspaceService) listRecords(ctx context.Context, botID string) ([]runtimeworkspace.RemoteMountRecord, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	botID, ok := runtimeworkspace.CanonicalWorkspaceUUID(botID)
	if !ok {
		return nil, userruntime.ErrInvalidInput
	}
	ownerUserID, err := s.lookupBotOwner(ctx, botID)
	if err != nil {
		return nil, err
	}
	records, err := s.store.ListMounts(ctx, botID)
	if err != nil {
		return nil, err
	}
	setBotOwner(records, ownerUserID)
	return records, nil
}

func (s *RemoteWorkspaceService) getRecord(ctx context.Context, botID, targetID string) (runtimeworkspace.RemoteMountRecord, error) {
	if s == nil || s.store == nil {
		return runtimeworkspace.RemoteMountRecord{}, runtimeworkspace.ErrWorkspaceTargetNotFound
	}
	botID, botOK := runtimeworkspace.CanonicalWorkspaceUUID(botID)
	targetID, targetOK := runtimeworkspace.CanonicalWorkspaceUUID(targetID)
	if !botOK || !targetOK {
		return runtimeworkspace.RemoteMountRecord{}, runtimeworkspace.ErrWorkspaceTargetNotFound
	}
	ownerUserID, err := s.lookupBotOwner(ctx, botID)
	if err != nil {
		return runtimeworkspace.RemoteMountRecord{}, err
	}
	record, err := s.store.GetMount(ctx, botID, targetID)
	if errors.Is(err, runtimeworkspace.ErrRecordNotFound) {
		return runtimeworkspace.RemoteMountRecord{}, runtimeworkspace.ErrWorkspaceTargetNotFound
	}
	record.BotOwnerUserID = ownerUserID
	return record, err
}

func (s *RemoteWorkspaceService) getPrimaryRecord(ctx context.Context, botID string) (runtimeworkspace.RemoteMountRecord, error) {
	if s == nil || s.store == nil {
		return runtimeworkspace.RemoteMountRecord{}, runtimeworkspace.ErrRemoteWorkspaceNotBound
	}
	botID, ok := runtimeworkspace.CanonicalWorkspaceUUID(botID)
	if !ok {
		return runtimeworkspace.RemoteMountRecord{}, userruntime.ErrInvalidInput
	}
	ownerUserID, err := s.lookupBotOwner(ctx, botID)
	if err != nil {
		return runtimeworkspace.RemoteMountRecord{}, err
	}
	record, err := s.store.GetPrimaryMount(ctx, botID)
	if errors.Is(err, runtimeworkspace.ErrRecordNotFound) {
		return runtimeworkspace.RemoteMountRecord{}, runtimeworkspace.ErrRemoteWorkspaceNotBound
	}
	record.BotOwnerUserID = ownerUserID
	return record, err
}

func (s *RemoteWorkspaceService) lookupBotOwner(ctx context.Context, botID string) (string, error) {
	if s == nil || s.owners == nil {
		return "", errors.New("bot owner reader not configured")
	}
	return s.owners.BotOwnerUserID(ctx, botID)
}

func setBotOwner(records []runtimeworkspace.RemoteMountRecord, ownerUserID string) {
	for i := range records {
		records[i].BotOwnerUserID = ownerUserID
	}
}

func (s *RemoteWorkspaceService) clientForRecord(record runtimeworkspace.RemoteMountRecord) (*bridge.Client, *userruntime.Connection, error) {
	if record.RuntimeUserID != record.BotOwnerUserID {
		return nil, nil, runtimeworkspace.ErrRemoteRuntimeOwnerMismatch
	}
	if record.RuntimeRevoked {
		return nil, nil, runtimeworkspace.ErrRemoteRuntimeRevoked
	}
	if s.runtimes == nil {
		return nil, nil, runtimeworkspace.ErrRemoteRuntimeOffline
	}
	connection, ok := s.runtimes.Connection(record.RuntimeID)
	if !ok || connection == nil || connection.Client == nil {
		return nil, nil, runtimeworkspace.ErrRemoteRuntimeOffline
	}
	if !runtimeworkspace.SupportsRemoteWorkspace(connection.Info.Capabilities) {
		return nil, nil, runtimeworkspace.ErrRemoteRuntimeClientUpdateNeeded
	}
	client := connection.Client
	if client == nil {
		return nil, nil, runtimeworkspace.ErrRemoteRuntimeOffline
	}
	return client, connection, nil
}

func (s *RemoteWorkspaceService) target(record runtimeworkspace.RemoteMountRecord) runtimeworkspace.WorkspaceTarget {
	approval := runtimeworkspace.ToolApprovalConfigFromRaw(record.ToolApproval)
	target := runtimeworkspace.WorkspaceTarget{
		TargetID:           record.ID,
		Kind:               runtimeworkspace.WorkspaceTargetRemote,
		RuntimeID:          record.RuntimeID,
		Name:               record.RuntimeName,
		Primary:            record.IsPrimary,
		ToolApproval:       runtimeworkspace.WorkspaceToolApprovalModes(approval),
		ToolApprovalConfig: approval,
	}
	switch {
	case record.RuntimeUserID != record.BotOwnerUserID:
		target.Name = ""
		target.RuntimeID = ""
		target.Status = runtimeworkspace.WorkspaceTargetStatusOwnerMismatch
	case record.RuntimeRevoked:
		target.Status = runtimeworkspace.WorkspaceTargetStatusRevoked
	case s.runtimes == nil:
		target.Status = runtimeworkspace.WorkspaceTargetStatusOffline
	default:
		connection, online := s.runtimes.Connection(record.RuntimeID)
		target.Online = online && connection != nil && connection.Client != nil
		switch {
		case !target.Online:
			target.Status = runtimeworkspace.WorkspaceTargetStatusOffline
		case !runtimeworkspace.SupportsRemoteWorkspace(connection.Info.Capabilities):
			target.Status = runtimeworkspace.WorkspaceTargetStatusClientUpdateRequired
		default:
			target.Status = runtimeworkspace.WorkspaceTargetStatusOnline
		}
	}
	return target
}
