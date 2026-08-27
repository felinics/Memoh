package client

import (
	"fmt"
	"strings"

	"github.com/memohai/memoh/internal/runtimeauth"
	"github.com/memohai/memoh/internal/workspace/bridge"
)

const HermesContainerHome = dataMountPath + "/.memoh-hermes"

type SessionContextInput struct {
	RuntimeID   string
	AgentID     string
	SetupMode   SetupMode
	Backend     string
	ProjectPath string
}

type ResolvedSessionContext struct {
	RuntimeID     string
	AgentID       string
	SetupMode     SetupMode
	Backend       WorkspaceBackend
	WorkspaceRoot string
	ProjectPath   string
	CWD           string
	AuthRoot      string
	CodexHome     string
	ClaudeHome    string
	HermesHome    string
}

func ResolveSessionContext(input SessionContextInput) (ResolvedSessionContext, error) {
	var backend WorkspaceBackend
	switch strings.ToLower(strings.TrimSpace(input.Backend)) {
	case "", bridge.WorkspaceBackendContainer:
		backend = WorkspaceBackendContainer
	default:
		return ResolvedSessionContext{}, fmt.Errorf("unsupported workspace backend %q", input.Backend)
	}
	resolvedRoot := dataMountPath
	projectPath, err := ResolvePathUnderVirtualRoot(resolvedRoot, input.ProjectPath)
	if err != nil {
		return ResolvedSessionContext{}, err
	}

	ctx := ResolvedSessionContext{
		RuntimeID:     strings.TrimSpace(input.RuntimeID),
		AgentID:       strings.TrimSpace(input.AgentID),
		SetupMode:     normalizeSetupMode(input.SetupMode),
		Backend:       backend,
		WorkspaceRoot: resolvedRoot,
		ProjectPath:   projectPath,
		CWD:           projectPath,
	}
	if ctx.SetupMode != SetupModeSelf && ctx.RuntimeID != "" {
		authRoot, err := runtimeauth.RootFor(ctx.RuntimeID)
		if err != nil {
			return ResolvedSessionContext{}, err
		}
		ctx.AuthRoot = authRoot
		ctx.CodexHome, _ = runtimeauth.Child(authRoot, "codex")
		ctx.ClaudeHome, _ = runtimeauth.Child(authRoot, "claude-home")
		ctx.HermesHome, _ = runtimeauth.Child(authRoot, "hermes")
	} else if isHermesAgent(input.AgentID) && ctx.SetupMode != SetupModeSelf {
		// Compatibility callers that prepare a Bot workspace outside a live
		// runtime retain the historical location. Live runtimes always pass an
		// ID and use the isolated /tmp/memoh-auth root above.
		ctx.HermesHome = HermesContainerHome
	}
	return ctx, nil
}

func resolveWorkspacePaths(info bridge.WorkspaceInfo, rawProjectPath string) (string, string, WorkspaceBackend, error) {
	ctx, err := ResolveSessionContext(SessionContextInput{
		Backend:     info.Backend,
		ProjectPath: rawProjectPath,
	})
	if err != nil {
		return "", "", WorkspaceBackendContainer, err
	}
	return ctx.WorkspaceRoot, ctx.ProjectPath, ctx.Backend, nil
}

func resolvedHermesHome(ctx *ResolvedSessionContext) string {
	if ctx == nil {
		return ""
	}
	return strings.TrimSpace(ctx.HermesHome)
}

func resolvedAuthRoot(ctx *ResolvedSessionContext) string {
	if ctx == nil {
		return ""
	}
	return strings.TrimSpace(ctx.AuthRoot)
}

func resolvedCodexHome(ctx *ResolvedSessionContext) string {
	if ctx == nil {
		return ""
	}
	return strings.TrimSpace(ctx.CodexHome)
}

func resolvedClaudeHome(ctx *ResolvedSessionContext) string {
	if ctx == nil {
		return ""
	}
	return strings.TrimSpace(ctx.ClaudeHome)
}
