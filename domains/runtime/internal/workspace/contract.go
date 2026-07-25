package workspace

import (
	"context"
	"fmt"
	"io"
	"strings"

	runtimedomain "github.com/memohai/memoh/domains/runtime"
	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
)

var requiredWorkspaceExecutables = []string{
	runtimedomain.WorkspaceToolkitDir + "/bin/node",
	runtimedomain.WorkspaceToolkitDir + "/bin/python3",
	runtimedomain.WorkspaceToolkitDir + "/bin/uv",
	runtimedomain.WorkspaceToolkitDir + "/bin/codex",
	runtimedomain.WorkspaceToolkitDir + "/bin/codex-acp",
	runtimedomain.WorkspaceToolkitDir + "/bin/claude-agent-acp",
	runtimedomain.WorkspaceToolkitDir + "/bin/hermes-acp",
	runtimedomain.WorkspaceToolkitDir + "/display/bin/a11y-cli",
	runtimedomain.WorkspaceScriptsDir + "/display-prepare.sh",
	runtimedomain.WorkspaceScriptsDir + "/display-apply-style.sh",
	runtimedomain.WorkspaceScriptsDir + "/desktop-style.sh",
}

func validateWorkspaceContract(ctx context.Context, client *bridge.Client) error {
	if client == nil {
		return fmt.Errorf("%w: bridge client is unavailable", runtimedomain.ErrWorkspaceImageIncompatible)
	}
	reader, err := client.ReadRaw(ctx, runtimedomain.WorkspaceContractPath)
	if err != nil {
		return fmt.Errorf("%w: read contract: %w", runtimedomain.ErrWorkspaceImageIncompatible, err)
	}
	defer func() { _ = reader.Close() }()

	payload, err := io.ReadAll(io.LimitReader(reader, 64*1024))
	if err != nil {
		return fmt.Errorf("%w: read contract payload: %w", runtimedomain.ErrWorkspaceImageIncompatible, err)
	}
	if err := runtimedomain.ValidateWorkspaceContractPayload(payload); err != nil {
		return err
	}

	result, err := client.Exec(ctx, workspaceExecutableCheckCommand(), "/", 30)
	if err != nil {
		return fmt.Errorf("%w: validate runtime executables: %w", runtimedomain.ErrWorkspaceImageIncompatible, err)
	}
	if result == nil || result.ExitCode != 0 {
		return fmt.Errorf("%w: one or more runtime executables are missing", runtimedomain.ErrWorkspaceImageIncompatible)
	}
	return nil
}

func workspaceExecutableCheckCommand() string {
	return "test -x " + strings.Join(requiredWorkspaceExecutables, " -a -x ")
}
