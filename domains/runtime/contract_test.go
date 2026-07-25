package runtime

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
			err = ValidateWorkspaceContractPayload(payload)
			if tt.mutate == nil {
				if err != nil {
					t.Fatalf("ValidateWorkspaceContractPayload() error = %v", err)
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

	err := ValidateWorkspaceContractPayload([]byte("{"))
	if !errors.Is(err, ErrWorkspaceImageIncompatible) {
		t.Fatalf("error = %v, want ErrWorkspaceImageIncompatible", err)
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
