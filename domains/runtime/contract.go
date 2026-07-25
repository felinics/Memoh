package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	WorkspaceContractPath           = "/opt/memoh/workspace-contract.json"
	CurrentWorkspaceContractVersion = 1
	WorkspaceToolkitDir             = "/opt/memoh/toolkit"
	WorkspaceScriptsDir             = "/opt/memoh/scripts"
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

// ValidateWorkspaceContractPayload checks the workspace image contract JSON
// schema and stable path/version constraints.
func ValidateWorkspaceContractPayload(payload []byte) error {
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
