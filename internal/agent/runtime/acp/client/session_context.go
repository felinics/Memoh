package client

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

const HermesContainerHome = dataMountPath + "/.memoh-hermes"

type SessionContextInput struct {
	AgentID        string
	SetupMode      SetupMode
	Backend        string
	OS             string
	DefaultWorkDir string
	ProjectPath    string
}

type ResolvedSessionContext struct {
	AgentID       string
	SetupMode     SetupMode
	Backend       WorkspaceBackend
	WorkspaceRoot string
	ProjectPath   string
	CWD           string
	HermesHome    string
}

func ResolveSessionContext(input SessionContextInput) (ResolvedSessionContext, error) {
	var backend WorkspaceBackend
	switch strings.ToLower(strings.TrimSpace(input.Backend)) {
	case "", bridge.WorkspaceBackendContainer:
		backend = WorkspaceBackendContainer
	case bridge.WorkspaceBackendRemote:
		backend = WorkspaceBackendRemote
	default:
		return ResolvedSessionContext{}, fmt.Errorf("unsupported workspace backend %q", input.Backend)
	}
	resolvedRoot := dataMountPath
	projectPath := ""
	var err error
	if backend == WorkspaceBackendRemote {
		osName := strings.ToLower(strings.TrimSpace(input.OS))
		if osName != "darwin" && osName != "linux" {
			return ResolvedSessionContext{}, fmt.Errorf("unsupported remote ACP operating system %q", input.OS)
		}
		remoteHome := path.Clean(strings.TrimSpace(input.DefaultWorkDir))
		if remoteHome == "." || !path.IsAbs(remoteHome) {
			return ResolvedSessionContext{}, errorsRemoteHomeRequired()
		}
		resolvedRoot = "/"
		projectPath, err = resolveRemoteProjectPath(remoteHome, input.ProjectPath)
	} else {
		projectPath, err = ResolvePathUnderVirtualRoot(resolvedRoot, input.ProjectPath)
	}
	if err != nil {
		return ResolvedSessionContext{}, err
	}

	ctx := ResolvedSessionContext{
		AgentID:       strings.TrimSpace(input.AgentID),
		SetupMode:     normalizeSetupMode(input.SetupMode),
		Backend:       backend,
		WorkspaceRoot: resolvedRoot,
		ProjectPath:   projectPath,
		CWD:           projectPath,
	}
	if isHermesAgent(input.AgentID) && ctx.SetupMode != SetupModeSelf {
		ctx.HermesHome = HermesContainerHome
	}
	return ctx, nil
}

func resolveRemoteProjectPath(home, raw string) (string, error) {
	home = path.Clean(strings.TrimSpace(home))
	if home == "." || !path.IsAbs(home) {
		return "", errorsRemoteHomeRequired()
	}
	target := strings.TrimSpace(raw)
	switch {
	case target == "", target == dataMountPath, target == "~":
		target = home
	case strings.HasPrefix(target, dataMountPath+"/"):
		target = path.Join(home, strings.TrimPrefix(target, dataMountPath+"/"))
	case strings.HasPrefix(target, "~/"):
		target = path.Join(home, strings.TrimPrefix(target, "~/"))
	case path.IsAbs(target):
		target = path.Clean(target)
	default:
		target = path.Join(home, target)
	}
	if !path.IsAbs(target) {
		return "", errors.New("remote ACP project path must be absolute")
	}
	return path.Clean(target), nil
}

func errorsRemoteHomeRequired() error {
	return errors.New("remote ACP workspace home must be an absolute path")
}

func resolveWorkspacePaths(info bridge.WorkspaceInfo, rawProjectPath string) (string, string, WorkspaceBackend, error) {
	ctx, err := ResolveSessionContext(SessionContextInput{
		Backend:        info.Backend,
		OS:             info.OS,
		DefaultWorkDir: info.DefaultWorkDir,
		ProjectPath:    rawProjectPath,
	})
	if err != nil {
		return "", "", WorkspaceBackendContainer, err
	}
	return ctx.WorkspaceRoot, ctx.ProjectPath, ctx.Backend, nil
}
