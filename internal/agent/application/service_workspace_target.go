package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/hooks"
	"github.com/felinics/memoh/internal/workdir"
	"github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

var ErrExternalAgentWorkspaceTargetUnsupported = errors.New("workspace_target_id is not supported for external agent sessions")

// ValidateWorkspaceTarget validates a user-selected Computer without changing
// the Bot's Primary target. It is used by handlers before creating a session.
func (s *Service) ValidateWorkspaceTarget(ctx context.Context, botID, targetID string) error {
	targetID = strings.TrimSpace(targetID)
	if targetID == "" {
		return nil
	}
	if s == nil || s.workspaceTargets == nil {
		return errors.New("workspace target resolver not configured")
	}
	target, err := s.workspaceTargets.ResolveWorkspaceTarget(ctx, strings.TrimSpace(botID), targetID)
	if errors.Is(err, workspace.ErrCapabilityUnavailable) && target.TargetID != "" {
		return nil
	}
	return err
}

func (s *Service) prepareWorkspaceRequest(ctx context.Context, req ChatRequest) (context.Context, ChatRequest, error) {
	ctx = hooks.WithLoadState(ctx)
	requestedTargetID := strings.TrimSpace(req.WorkspaceTargetID)
	bound, hasWorkdir, err := s.resolveSessionWorkdirBinding(ctx, req.BotID, req.ThreadID)
	if err != nil {
		return ctx, req, err
	}
	enforceSelection := requestedTargetID != ""
	if hasWorkdir {
		// The workdir pins the target for the session's whole life. An
		// explicit different target is rejected loudly — silently ignoring
		// it would let the user believe the switch took effect.
		if requestedTargetID != "" && requestedTargetID != bound.TargetID {
			return ctx, req, ErrWorkspaceTargetWorkdirConflict
		}
		requestedTargetID = bound.TargetID
		req.WorkspaceTargetID = bound.TargetID
		// Reaching a remote computer is a permission boundary whether the
		// target comes from the request or from the workdir binding. A
		// native workdir adds no capability beyond the default workspace,
		// so it does not demand workspace_read just to chat.
		if bound.Kind == workdir.TargetKindRemote {
			enforceSelection = true
		}
	}
	if enforceSelection {
		if strings.TrimSpace(req.UserID) == "" {
			return ctx, req, errors.New("user id is required to select a computer")
		}
		if s == nil || s.botPermissions == nil {
			return ctx, req, errors.New("workspace target permission checker not configured")
		}
		allowed, err := s.botPermissions.HasBotPermission(ctx, req.BotID, req.UserID, bots.PermissionWorkspaceRead)
		if err != nil {
			return ctx, req, fmt.Errorf("check workspace target permission: %w", err)
		}
		if !allowed {
			return ctx, req, errors.New("workspace_read permission is required to select a computer")
		}
	}
	if s == nil || s.workspaceTargets == nil {
		if requestedTargetID != "" {
			return ctx, req, errors.New("workspace target resolver not configured")
		}
		return ctx, req, nil
	}
	resolved, err := s.resolveChatWorkspaceTarget(ctx, req.BotID, requestedTargetID)
	if err != nil {
		if !errors.Is(err, workspace.ErrCapabilityUnavailable) || resolved.TargetID == "" {
			return ctx, req, err
		}
		ctx = bridge.WithWorkspaceUnavailable(ctx)
	}
	req.WorkspaceTargetID = strings.TrimSpace(resolved.TargetID)
	req.WorkspaceTarget = &WorkspaceTarget{
		TargetID: strings.TrimSpace(resolved.TargetID),
		Kind:     strings.TrimSpace(resolved.Kind),
		Name:     strings.TrimSpace(resolved.Name),
	}
	ctx = workspace.WithWorkspaceTarget(ctx, req.WorkspaceTargetID)
	ctx = context.WithValue(ctx, preparedWorkspaceTargetKey{}, resolved)
	return ctx, req, nil
}

func (s *Service) resolveWorkspaceTargetSnapshot(ctx context.Context, botID, targetID string) (*WorkspaceTarget, error) {
	if s == nil || s.workspaceTargets == nil {
		if strings.TrimSpace(targetID) == "" {
			return nil, nil
		}
		return nil, errors.New("workspace target resolver not configured")
	}
	resolved, err := s.workspaceTargets.ResolveWorkspaceTarget(ctx, botID, targetID)
	if err != nil && (!errors.Is(err, workspace.ErrCapabilityUnavailable) || resolved.TargetID == "") {
		return nil, err
	}
	return &WorkspaceTarget{
		TargetID: strings.TrimSpace(resolved.TargetID),
		Kind:     strings.TrimSpace(resolved.Kind),
		Name:     strings.TrimSpace(resolved.Name),
	}, nil
}

func workspaceTargetFromRunConfig(cfg native.RunConfig) *WorkspaceTarget {
	if strings.TrimSpace(cfg.Identity.WorkspaceTargetID) == "" {
		return nil
	}
	return &WorkspaceTarget{
		TargetID: strings.TrimSpace(cfg.Identity.WorkspaceTargetID),
		Kind:     strings.TrimSpace(cfg.Identity.WorkspaceTargetKind),
		Name:     strings.TrimSpace(cfg.Identity.WorkspaceTargetName),
	}
}

func rejectExternalAgentWorkspaceTarget(req ChatRequest) error {
	if strings.TrimSpace(req.WorkspaceTargetID) == "" {
		return nil
	}
	return ErrExternalAgentWorkspaceTargetUnsupported
}

type preparedWorkspaceTargetKey struct{}

// Target resolution may return a lazy bridge client before it connects. Probe
// it within the auxiliary budget so a dead bridge cannot register live tools.
func (s *Service) resolveChatWorkspaceTarget(ctx context.Context, botID, targetID string) (workspace.ResolvedWorkspaceTarget, error) {
	resolveCtx, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
	defer cancel()
	target, err := s.workspaceTargets.ResolveWorkspaceTarget(resolveCtx, botID, targetID)
	if err == nil && target.Client != nil {
		_, probeErr := target.Client.Stat(resolveCtx, "/")
		if errors.Is(probeErr, bridge.ErrUnavailable) || errors.Is(probeErr, context.DeadlineExceeded) {
			err = fmt.Errorf("%w: %w", workspace.ErrCapabilityUnavailable, probeErr)
		}
	}
	return target, err
}
