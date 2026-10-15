package tools

import (
	"context"
	"log/slog"
	"strings"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/workdir"
	"github.com/felinics/memoh/internal/workspace"
)

type NativeToolSessionReader interface {
	Get(context.Context, string) (thread.Thread, error)
}

type NativeToolWorkdirResolver interface {
	ResolveForSession(context.Context, string, string) (workdir.Resolved, error)
}

func (s *NativeToolSource) resolveSession(ctx context.Context, session mcp.ToolSessionContext) (SessionContext, error) {
	toolSession := sessionFromMCP(session)
	if toolSession.SessionID == "" || s == nil || s.sessions == nil {
		return toolSession, nil
	}
	stored, err := s.sessions.Get(ctx, toolSession.SessionID)
	if err != nil {
		s.logger.ErrorContext(ctx, "resolve MCP session workdir failed", slog.Any("error", err), slog.String("session_id", toolSession.SessionID))
		return SessionContext{}, apperror.New(apperror.CodeWorkspaceLoadFailed, nil)
	}
	if !strings.EqualFold(strings.TrimSpace(stored.BotID), strings.TrimSpace(toolSession.BotID)) {
		return SessionContext{}, apperror.New(apperror.CodeSessionNotFound, nil)
	}
	if strings.TrimSpace(stored.WorkdirID) == "" {
		if stored.SessionMode != thread.TypeSubagent {
			return toolSession, nil
		}
		project, _ := stored.RuntimeMetadata["project_path"].(string)
		if strings.TrimSpace(project) == "" {
			return SessionContext{}, apperror.New(apperror.CodeWorkspaceLoadFailed, nil)
		}
		toolSession.WorkdirPath = project
		toolSession.WorkspaceTargetID = workspace.WorkspaceTargetNative
		toolSession.WorkspaceTargetKind = workspace.WorkspaceTargetNative
		return toolSession, nil
	}
	if s.workdirs == nil {
		return SessionContext{}, apperror.New(apperror.CodeWorkspaceLoadFailed, nil)
	}
	bound, err := s.workdirs.ResolveForSession(ctx, toolSession.BotID, stored.WorkdirID)
	if err != nil {
		s.logger.ErrorContext(ctx, "resolve MCP workdir binding failed", slog.Any("error", err), slog.String("session_id", toolSession.SessionID))
		return SessionContext{}, apperror.New(apperror.CodeWorkspaceLoadFailed, nil)
	}
	if strings.TrimSpace(bound.WorkDir) == "" || strings.TrimSpace(bound.TargetID) == "" {
		return SessionContext{}, apperror.New(apperror.CodeWorkspaceLoadFailed, nil)
	}
	toolSession.WorkdirPath = bound.WorkDir
	toolSession.WorkspaceTargetID = bound.TargetID
	toolSession.WorkspaceTargetKind = bound.Kind
	return toolSession, nil
}
