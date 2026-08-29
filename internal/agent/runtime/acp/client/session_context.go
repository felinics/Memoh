package client

import (
	"fmt"
	"path"
	"strings"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

const HermesContainerHome = dataMountPath + "/.memoh-hermes"

type SessionContextInput struct {
	AgentID     string
	BotAgentID  string
	SetupMode   SetupMode
	Backend     string
	ProjectPath string
}

type ResolvedSessionContext struct {
	AgentID       string
	BotAgentID    string
	SetupMode     SetupMode
	Backend       WorkspaceBackend
	WorkspaceRoot string
	ProjectPath   string
	CWD           string
	HermesHome    string
	// CodexDurableDir overrides the shared CODEX_HOME durable directory for
	// one Bot Agent instance, so two Codex instances holding different
	// ChatGPT accounts never clobber each other's auth.json (and each
	// rotation write-back reads its own instance's file). Empty keeps the
	// legacy shared directory.
	CodexDurableDir string
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
		AgentID:       strings.TrimSpace(input.AgentID),
		BotAgentID:    strings.TrimSpace(input.BotAgentID),
		SetupMode:     normalizeSetupMode(input.SetupMode),
		Backend:       backend,
		WorkspaceRoot: resolvedRoot,
		ProjectPath:   projectPath,
		CWD:           projectPath,
	}
	if isHermesAgent(input.AgentID) && ctx.SetupMode != SetupModeSelf {
		ctx.HermesHome = HermesContainerHome
	}
	if isCodexAgent(input.AgentID) && ctx.SetupMode != SetupModeSelf && ctx.BotAgentID != "" {
		instanceDir, err := CodexInstanceDurableDir(ctx.BotAgentID)
		if err != nil {
			return ResolvedSessionContext{}, err
		}
		ctx.CodexDurableDir = instanceDir
	}
	return ctx, nil
}

func isCodexAgent(agentID string) bool {
	return strings.EqualFold(strings.TrimSpace(agentID), "codex")
}

// codexInstanceRelDir is the durable per-instance Codex home relative to the
// data mount; only server-generated UUID instance ids are accepted so the
// path can never escape it.
func codexInstanceRelDir(botAgentID string) (string, error) {
	botAgentID = strings.TrimSpace(botAgentID)
	if _, err := uuid.Parse(botAgentID); err != nil {
		return "", fmt.Errorf("invalid bot agent id for Codex durable directory: %w", err)
	}
	return path.Join(".codex", "agents", botAgentID), nil
}

// CodexInstanceDurableDir returns the absolute per-instance CODEX_HOME
// durable directory under the data mount.
func CodexInstanceDurableDir(botAgentID string) (string, error) {
	rel, err := codexInstanceRelDir(botAgentID)
	if err != nil {
		return "", err
	}
	return path.Join(dataMountPath, rel), nil
}

func resolvedBotAgentID(ctx *ResolvedSessionContext) string {
	if ctx == nil {
		return ""
	}
	return strings.TrimSpace(ctx.BotAgentID)
}

func resolvedCodexDurableDir(ctx *ResolvedSessionContext) string {
	if ctx == nil {
		return ""
	}
	return strings.TrimSpace(ctx.CodexDurableDir)
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
