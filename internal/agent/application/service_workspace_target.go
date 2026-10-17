package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

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
	resolveCtx, cancel := bridge.AuxiliaryContext(ctx)
	defer cancel()
	target, err := s.workspaceTargets.ResolveWorkspaceTarget(resolveCtx, strings.TrimSpace(botID), targetID)
	if workspaceCapabilityUnavailable(ctx, target, err) {
		// The Computer exists; the turn itself degrades to text-only.
		return nil
	}
	return err
}

func (s *Service) prepareWorkspaceRequest(ctx context.Context, req ChatRequest) (context.Context, ChatRequest, error) {
	ctx = hooks.WithAuxiliaryTurn(ctx)
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
	if prior := req.WorkspaceTarget; prior != nil && bridge.WorkspaceUnavailableFromContext(ctx) &&
		(requestedTargetID == "" || requestedTargetID == strings.TrimSpace(prior.TargetID)) {
		// An earlier stage of this turn resolved the target and found the
		// workspace unavailable; resolving again would only wait for it once
		// more. The identity it recorded is kept.
		req.WorkspaceTargetID = strings.TrimSpace(prior.TargetID)
		return s.withPreparedWorkspaceTarget(ctx, req, *prior), req, nil
	}
	resolved, err := s.resolveTurnWorkspaceTarget(ctx, req.BotID, requestedTargetID)
	if err != nil {
		return ctx, req, err
	}
	target := WorkspaceTarget{
		TargetID: strings.TrimSpace(resolved.TargetID),
		Kind:     strings.TrimSpace(resolved.Kind),
		Name:     strings.TrimSpace(resolved.Name),
	}
	req.WorkspaceTargetID = target.TargetID
	req.WorkspaceTarget = &target
	return s.withPreparedWorkspaceTarget(ctx, req, target), req, nil
}

func (*Service) withPreparedWorkspaceTarget(ctx context.Context, req ChatRequest, target WorkspaceTarget) context.Context {
	ctx = workspace.WithWorkspaceTarget(ctx, target.TargetID)
	return context.WithValue(ctx, preparedWorkspaceTargetKey{}, preparedWorkspaceTarget{
		WorkspaceTarget: target,
		botID:           strings.TrimSpace(req.BotID),
		sessionID:       strings.TrimSpace(req.ThreadID),
	})
}

type preparedWorkspaceTargetKey struct{}

// preparedWorkspaceTarget is the target a turn resolved up front, recorded
// with the bot and session it belongs to: the context value outlives the
// turn through derived contexts, so a run config built for another bot or
// session must not adopt it.
type preparedWorkspaceTarget struct {
	WorkspaceTarget
	botID     string
	sessionID string
}

func preparedWorkspaceTargetFromContext(ctx context.Context, botID, sessionID string) (WorkspaceTarget, bool) {
	prepared, ok := ctx.Value(preparedWorkspaceTargetKey{}).(preparedWorkspaceTarget)
	if !ok || prepared.botID != strings.TrimSpace(botID) || prepared.sessionID != strings.TrimSpace(sessionID) {
		return WorkspaceTarget{}, false
	}
	return prepared.WorkspaceTarget, true
}

// resolveTurnWorkspaceTarget resolves the workspace target of a chat turn
// within the auxiliary timeout. A known target whose runtime is not ready or
// stopped responding is returned without error and the turn's workspace is
// marked unavailable, so the turn continues as a text-only conversation.
// Identity, permission and database failures are returned unchanged.
func (s *Service) resolveTurnWorkspaceTarget(ctx context.Context, botID, targetID string) (workspace.ResolvedWorkspaceTarget, error) {
	resolveCtx, cancel := bridge.AuxiliaryContext(ctx)
	defer cancel()
	target, err := s.workspaceTargets.ResolveWorkspaceTarget(resolveCtx, botID, targetID)
	if workspaceCapabilityUnavailable(ctx, target, err) {
		if s.logger != nil {
			s.logger.WarnContext(ctx, "workspace unavailable, continuing chat turn without it",
				slog.String("bot_id", botID),
				slog.String("workspace_target_id", target.TargetID),
				slog.Any("error", err),
			)
		}
		bridge.MarkWorkspaceUnavailable(ctx)
		return target, nil
	}
	return target, err
}

func workspaceCapabilityUnavailable(ctx context.Context, target workspace.ResolvedWorkspaceTarget, err error) bool {
	return err != nil && ctx.Err() == nil &&
		errors.Is(err, workspace.ErrCapabilityUnavailable) &&
		strings.TrimSpace(target.TargetID) != ""
}

// resolveWorkspaceTargetSnapshot describes the workspace target a stored turn
// records. It resolves within the auxiliary timeout like the turn itself, so
// a workspace that is not ready or stopped responding keeps its identity and
// is marked unavailable for the rest of the turn instead of being waited on
// again by the continuation that follows.
func (s *Service) resolveWorkspaceTargetSnapshot(ctx context.Context, botID, targetID string) (*WorkspaceTarget, error) {
	if s == nil || s.workspaceTargets == nil {
		if strings.TrimSpace(targetID) == "" {
			return nil, nil
		}
		return nil, errors.New("workspace target resolver not configured")
	}
	resolved, err := s.resolveTurnWorkspaceTarget(ctx, botID, targetID)
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

func rejectExternalAgentWorkspaceTarget(req ChatRequest) error {
	if strings.TrimSpace(req.WorkspaceTargetID) == "" {
		return nil
	}
	return ErrExternalAgentWorkspaceTargetUnsupported
}
