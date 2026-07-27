package workspace

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestValidateWorkspaceContractPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*WorkspaceContract)
	}{
		{name: "valid"},
		{name: "version", mutate: func(contract *WorkspaceContract) {
			contract.ContractVersion++
		}},
		{name: "operating system", mutate: func(contract *WorkspaceContract) {
			contract.Platform.OS = "windows"
		}},
		{name: "libc", mutate: func(contract *WorkspaceContract) {
			contract.Platform.Libc = "musl"
		}},
		{name: "toolkit path", mutate: func(contract *WorkspaceContract) {
			contract.Paths.Toolkit = "/custom/toolkit"
		}},
		{name: "scripts path", mutate: func(contract *WorkspaceContract) {
			contract.Paths.Scripts = "/custom/scripts"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			contract := validWorkspaceContract()
			if tt.mutate != nil {
				tt.mutate(&contract)
			}
			payload, err := json.Marshal(contract)
			if err != nil {
				t.Fatal(err)
			}
			err = validateWorkspaceContractPayload(payload)
			if tt.mutate == nil {
				if err != nil {
					t.Fatalf("validateWorkspaceContractPayload() error = %v", err)
				}
				return
			}
			if !errors.Is(err, ErrWorkspaceImageIncompatible) {
				t.Fatalf("error = %v, want ErrWorkspaceImageIncompatible", err)
			}
		})
	}
}

func TestValidateWorkspaceContractPayloadRejectsInvalidJSON(t *testing.T) {
	t.Parallel()

	err := validateWorkspaceContractPayload([]byte("{"))
	if !errors.Is(err, ErrWorkspaceImageIncompatible) {
		t.Fatalf("error = %v, want ErrWorkspaceImageIncompatible", err)
	}
}

func TestWorkspaceModeIsExecutable(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{
		"-rwxr-xr-x": true,
		"-rwsr-xr-x": true,
		"-rw-r--r-x": true,
		"-rw-r--r--": false,
		"drwxr-xr-x": true,
		"invalid":    false,
	}
	for mode, want := range tests {
		if got := workspaceModeIsExecutable(mode); got != want {
			t.Errorf("workspaceModeIsExecutable(%q) = %t, want %t", mode, got, want)
		}
	}
}

func validWorkspaceContract() WorkspaceContract {
	return WorkspaceContract{
		ContractVersion: CurrentWorkspaceContractVersion,
		Platform: WorkspaceContractPlatform{
			OS:   "linux",
			Libc: "glibc",
		},
		Paths: WorkspaceContractPaths{
			Toolkit: WorkspaceToolkitDir,
			Scripts: WorkspaceScriptsDir,
		},
	}
}
