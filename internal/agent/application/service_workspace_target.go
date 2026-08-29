package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/workdir"
	"github.com/felinics/memoh/internal/workspace"
)

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
	_, err := s.workspaceTargets.ResolveWorkspaceTarget(ctx, strings.TrimSpace(botID), targetID)
	return err
}

func (s *Service) prepareWorkspaceRequest(ctx context.Context, req ChatRequest) (context.Context, ChatRequest, error) {
	requestedTargetID := strings.TrimSpace(req.WorkspaceTargetID)
	explicitSelection := requestedTargetID != ""
	bound, hasWorkdir, err := s.resolveSessionWorkdirBinding(ctx, req.BotID, req.ThreadID)
	if err != nil {
		return ctx, req, err
	}
	if hasWorkdir {
		// The workdir pins the target for the session's whole life. An
		// explicit different target is rejected loudly — silently ignoring
		// it would let the user believe the switch took effect.
		if requestedTargetID != "" && requestedTargetID != bound.TargetID {
			return ctx, req, ErrWorkspaceTargetWorkdirConflict
		}
		requestedTargetID = bound.TargetID
		req.WorkspaceTargetID = bound.TargetID
	}
	if s == nil || s.workspaceTargets == nil {
		if requestedTargetID != "" {
			return ctx, req, errors.New("workspace target resolver not configured")
		}
		return ctx, req, nil
	}
	resolved, err := s.workspaceTargets.ResolveWorkspaceTarget(ctx, req.BotID, requestedTargetID)
	if err != nil {
		return ctx, req, err
	}
	// Reaching a remote computer is a permission boundary no matter how the
	// target was chosen: explicit selection, a workdir binding, or ambient
	// resolution of a remote Primary. Gating on the resolved kind keeps every
	// path through one check. A native workdir adds no capability beyond the
	// default workspace, so plain chat stays ungated there unless the user
	// explicitly selected a computer.
	if explicitSelection || strings.EqualFold(strings.TrimSpace(resolved.Kind), workdir.TargetKindRemote) {
		if err := s.requireWorkspaceRead(ctx, req.BotID, req.UserID); err != nil {
			return ctx, req, err
		}
	}
	req.WorkspaceTargetID = strings.TrimSpace(resolved.TargetID)
	req.WorkspaceTarget = &WorkspaceTarget{
		TargetID: strings.TrimSpace(resolved.TargetID),
		Kind:     strings.TrimSpace(resolved.Kind),
		Name:     strings.TrimSpace(resolved.Name),
	}
	ctx = workspace.WithWorkspaceTarget(ctx, req.WorkspaceTargetID)
	return ctx, req, nil
}

// ErrWorkspaceTargetNeedsActor marks a turn that would reach a connected
// computer without an acting user to authorize it. System-driven turns that
// should reach a computer (schedules, heartbeats) act as the bot owner and
// therefore never hit this; turns with no user identity at all — such as
// bot-to-bot discuss — are refused by design rather than silently landing on
// someone's machine.
var ErrWorkspaceTargetNeedsActor = errors.New("reaching a connected computer requires an acting user with workspace_read")

// requireWorkspaceRead is the single turn-level permission check for reaching
// a computer. Handlers keep their own HTTP-layer equivalent, but every chat
// turn funnels through here regardless of transport.
func (s *Service) requireWorkspaceRead(ctx context.Context, botID, userID string) error {
	if strings.TrimSpace(userID) == "" {
		return ErrWorkspaceTargetNeedsActor
	}
	if s == nil || s.botPermissions == nil {
		return errors.New("workspace target permission checker not configured")
	}
	allowed, err := s.botPermissions.HasBotPermission(ctx, botID, userID, bots.PermissionWorkspaceRead)
	if err != nil {
		return fmt.Errorf("check workspace target permission: %w", err)
	}
	if !allowed {
		return errors.New("workspace_read permission is required to select a computer")
	}
	return nil
}

func (s *Service) resolveWorkspaceTargetSnapshot(ctx context.Context, botID, targetID string) (*WorkspaceTarget, error) {
	if s == nil || s.workspaceTargets == nil {
		if strings.TrimSpace(targetID) == "" {
			return nil, nil
		}
		return nil, errors.New("workspace target resolver not configured")
	}
	resolved, err := s.workspaceTargets.ResolveWorkspaceTarget(ctx, botID, targetID)
	if err != nil {
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
