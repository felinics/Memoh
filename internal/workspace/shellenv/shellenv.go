// Package shellenv is the single rule for which shell a workspace user works
// in and which command search path that shell gives them. The browser
// terminal and every launcher that must find user-installed commands derive
// from the same launch line here, so a command that resolves in the terminal
// also resolves for a process Memoh starts. The command lines themselves live
// in shellprobe, which the bridge shares.
package shellenv

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/felinics/memoh/internal/workspace/bridge"
	"github.com/felinics/memoh/internal/workspace/shellenv/shellprobe"
	"github.com/felinics/memoh/internal/workspace/vpath"
)

// probeKillBackstop is how much later than probeTimeout the bridge kills the
// probe, for workspaces where the shell could not be given its own timeout.
const probeKillBackstop = time.Second

// probeTimeout bounds an rc file that never returns. A variable so tests can
// exercise that case quickly; it is applied in whole seconds.
var probeTimeout = shellprobe.DefaultTimeout

// TerminalCommand is the launch line of the interactive browser terminal.
func TerminalCommand() string {
	return shellprobe.TerminalCommand()
}

// ResolvePath returns the command search path an interactive workspace shell
// ends up with when it starts from env and seedPath. Only PATH is taken from
// the shell: the rest of the environment stays under the caller's control.
func ResolvePath(ctx context.Context, client *bridge.Client, seedPath string, env, unsetEnv []string) (string, error) {
	if client == nil {
		return "", errors.New("workspace bridge client is required")
	}
	killAfter := probeTimeout + probeKillBackstop
	result, err := client.ExecWithOptions(ctx, shellprobe.PathProbeCommand(probeTimeout), vpath.DataMount, int32(killAfter.Seconds()), nil, bridge.ExecOptions{
		Env:      append(append([]string(nil), env...), "PATH="+seedPath),
		UnsetEnv: unsetEnv,
	})
	if err != nil {
		return "", fmt.Errorf("probe workspace shell PATH: %w", err)
	}
	if result.Stdout == "" {
		return "", fmt.Errorf("workspace shell exited with status %d without reporting a PATH", result.ExitCode)
	}
	return shellprobe.MergePaths(result.Stdout, seedPath), nil
}
