package workspace

import (
	"strings"
	"testing"
)

func TestWorkspaceExecutableCheckCommandCoversContractRuntime(t *testing.T) {
	t.Parallel()

	command := workspaceExecutableCheckCommand()
	for _, executable := range requiredWorkspaceExecutables {
		if !strings.Contains(command, executable) {
			t.Fatalf("command does not check %s: %s", executable, command)
		}
	}
}
