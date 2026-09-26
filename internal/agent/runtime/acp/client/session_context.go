package client

import (
	"fmt"
	"strings"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

type SessionContextInput struct {
	Backend     string
	ProjectPath string
}

type ResolvedSessionContext struct {
	Backend       WorkspaceBackend
	WorkspaceRoot string
	ProjectPath   string
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
		Backend:       backend,
		WorkspaceRoot: resolvedRoot,
		ProjectPath:   projectPath,
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
