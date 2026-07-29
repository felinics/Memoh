package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/memohai/memoh/internal/agent/runtime/native"
	"github.com/memohai/memoh/internal/bots"
	"github.com/memohai/memoh/internal/workspace"
)

var ErrWorkspaceTargetACPUnsupported = errors.New("workspace_target_id is not supported for ACP sessions")

type workspaceTargetDescriptorContextKey struct{}

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
	_, err := s.resolveWorkspaceTargetDescriptor(ctx, strings.TrimSpace(botID), targetID)
	return err
}

func (s *Service) prepareWorkspaceRequest(ctx context.Context, req ChatRequest) (context.Context, ChatRequest, error) {
	requestedTargetID := strings.TrimSpace(req.WorkspaceTargetID)
	if requestedTargetID != "" {
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
		return s.attachWorkspaceContext(ctx, req)
	}
	resolved, err := s.resolveWorkspaceTargetDescriptor(ctx, req.BotID, requestedTargetID)
	if err != nil {
		return ctx, req, err
	}
	req.WorkspaceTargetID = strings.TrimSpace(resolved.TargetID)
	req.WorkspaceTarget = &WorkspaceTarget{
		TargetID: strings.TrimSpace(resolved.TargetID),
		Kind:     strings.TrimSpace(resolved.Kind),
		Name:     strings.TrimSpace(resolved.Name),
	}
	ctx = workspace.WithWorkspaceTarget(ctx, req.WorkspaceTargetID)
	ctx = context.WithValue(ctx, workspaceTargetDescriptorContextKey{}, resolved)
	return s.attachWorkspaceContext(ctx, req)
}

func (s *Service) resolveWorkspaceTargetSnapshot(ctx context.Context, botID, targetID string) (*WorkspaceTarget, error) {
	if s == nil || s.workspaceTargets == nil {
		if strings.TrimSpace(targetID) == "" {
			return nil, nil
		}
		return nil, errors.New("workspace target resolver not configured")
	}
	resolved, err := s.resolveWorkspaceTargetDescriptor(ctx, botID, targetID)
	if err != nil {
		return nil, err
	}
	return &WorkspaceTarget{
		TargetID: strings.TrimSpace(resolved.TargetID),
		Kind:     strings.TrimSpace(resolved.Kind),
		Name:     strings.TrimSpace(resolved.Name),
	}, nil
}

func (s *Service) resolveWorkspaceTargetDescriptor(ctx context.Context, botID, targetID string) (workspace.WorkspaceTargetDescriptor, error) {
	if ctx != nil {
		if cached, ok := ctx.Value(workspaceTargetDescriptorContextKey{}).(workspace.WorkspaceTargetDescriptor); ok {
			requested := strings.TrimSpace(targetID)
			if requested == "" {
				requested = workspace.WorkspaceTargetFromContext(ctx)
			}
			if requested == "" || strings.EqualFold(strings.TrimSpace(cached.TargetID), requested) {
				return cached, nil
			}
		}
	}
	if resolver, ok := s.workspaceTargets.(workspaceTargetDescriptorResolver); ok {
		return resolver.ResolveWorkspaceTargetDescriptor(ctx, botID, targetID)
	}
	resolved, err := s.workspaceTargets.ResolveWorkspaceTarget(ctx, botID, targetID)
	if err != nil {
		return workspace.WorkspaceTargetDescriptor{}, err
	}
	return workspace.WorkspaceTargetDescriptor{
		TargetID: resolved.TargetID,
		Kind:     resolved.Kind,
		Name:     resolved.Name,
		Primary:  resolved.Primary,
		Info:     resolved.Info,
		Approval: resolved.Approval,
	}, nil
}

func (s *Service) attachWorkspaceContext(ctx context.Context, req ChatRequest) (context.Context, ChatRequest, error) {
	if s == nil || s.workspaceContext == nil {
		return ctx, req, nil
	}
	next, _, err := s.workspaceContext.Attach(ctx, req.BotID)
	if err != nil {
		return ctx, req, fmt.Errorf("load workspace context: %w", err)
	}
	return next, req, nil
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

func rejectACPWorkspaceTarget(req ChatRequest) error {
	if strings.TrimSpace(req.WorkspaceTargetID) == "" {
		return nil
	}
	return ErrWorkspaceTargetACPUnsupported
}
