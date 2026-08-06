package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/memohai/memoh/internal/workspace/bridge"
)

const (
	WorkspaceContractPath           = "/opt/memoh/workspace-contract.json"
	CurrentWorkspaceContractVersion = 2
	WorkspaceToolkitDir             = "/opt/memoh/toolkit"
	WorkspaceScriptsDir             = "/opt/memoh/scripts"
	WorkspaceInitPath               = "/usr/bin/tini"
	WorkspaceBridgePath             = "/opt/memoh/bridge"
)

var ErrWorkspaceImageIncompatible = errors.New("workspace image is incompatible")

type WorkspaceContract struct {
	ContractVersion int                       `json:"contract_version"`
	Platform        WorkspaceContractPlatform `json:"platform"`
	Paths           WorkspaceContractPaths    `json:"paths"`
}

type WorkspaceContractPlatform struct {
	OS   string `json:"os"`
	Libc string `json:"libc"`
}

type WorkspaceContractPaths struct {
	Toolkit string `json:"toolkit"`
	Scripts string `json:"scripts"`
}

var requiredWorkspaceExecutables = []string{
	WorkspaceInitPath,
	WorkspaceToolkitDir + "/bin/node",
	WorkspaceToolkitDir + "/bin/python3",
	WorkspaceToolkitDir + "/bin/uv",
	WorkspaceToolkitDir + "/bin/codex",
	WorkspaceToolkitDir + "/bin/codex-acp",
	WorkspaceToolkitDir + "/bin/claude-agent-acp",
	WorkspaceToolkitDir + "/bin/hermes-acp",
	WorkspaceToolkitDir + "/display/bin/a11y-cli",
	WorkspaceScriptsDir + "/display-prepare.sh",
	WorkspaceScriptsDir + "/display-apply-style.sh",
	WorkspaceScriptsDir + "/desktop-style.sh",
}

func validateWorkspaceContract(ctx context.Context, client *bridge.Client) error {
	if client == nil {
		return fmt.Errorf("%w: bridge client is unavailable", ErrWorkspaceImageIncompatible)
	}
	reader, err := client.ReadRaw(ctx, WorkspaceContractPath)
	if err != nil {
		return fmt.Errorf("%w: read contract: %w", ErrWorkspaceImageIncompatible, err)
	}
	defer func() { _ = reader.Close() }()

	payload, err := io.ReadAll(io.LimitReader(reader, 64*1024))
	if err != nil {
		return fmt.Errorf("%w: read contract payload: %w", ErrWorkspaceImageIncompatible, err)
	}
	if err := validateWorkspaceContractPayload(payload); err != nil {
		return err
	}

	for _, executable := range requiredWorkspaceExecutables {
		entry, err := client.Stat(ctx, executable)
		if err != nil {
			return fmt.Errorf("%w: stat runtime executable %s: %w", ErrWorkspaceImageIncompatible, executable, err)
		}
		if entry.GetIsDir() || !workspaceModeIsExecutable(entry.GetMode()) {
			return fmt.Errorf("%w: runtime executable is not executable: %s", ErrWorkspaceImageIncompatible, executable)
		}
	}
	return nil
}

func validateWorkspaceContractPayload(payload []byte) error {
	var contract WorkspaceContract
	if err := json.Unmarshal(payload, &contract); err != nil {
		return fmt.Errorf("%w: decode contract: %w", ErrWorkspaceImageIncompatible, err)
	}
	if contract.ContractVersion != CurrentWorkspaceContractVersion {
		return fmt.Errorf(
			"%w: contract version %d, want %d",
			ErrWorkspaceImageIncompatible,
			contract.ContractVersion,
			CurrentWorkspaceContractVersion,
		)
	}
	if !strings.EqualFold(strings.TrimSpace(contract.Platform.OS), "linux") {
		return fmt.Errorf("%w: unsupported operating system %q", ErrWorkspaceImageIncompatible, contract.Platform.OS)
	}
	if !strings.EqualFold(strings.TrimSpace(contract.Platform.Libc), "glibc") {
		return fmt.Errorf("%w: unsupported libc %q", ErrWorkspaceImageIncompatible, contract.Platform.Libc)
	}
	if strings.TrimSpace(contract.Paths.Toolkit) != WorkspaceToolkitDir {
		return fmt.Errorf("%w: toolkit path %q", ErrWorkspaceImageIncompatible, contract.Paths.Toolkit)
	}
	if strings.TrimSpace(contract.Paths.Scripts) != WorkspaceScriptsDir {
		return fmt.Errorf("%w: scripts path %q", ErrWorkspaceImageIncompatible, contract.Paths.Scripts)
	}
	return nil
}

func workspaceModeIsExecutable(mode string) bool {
	mode = strings.TrimSpace(mode)
	if len(mode) != 10 {
		return false
	}
	for _, index := range []int{3, 6, 9} {
		switch mode[index] {
		case 'x', 's', 't':
			return true
		}
	}
	return false
}
