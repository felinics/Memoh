// Package shellenv is the single rule for which shell a workspace user works
// in and which command search path that shell gives them. The browser
// terminal and every launcher that must find user-installed commands derive
// from the same launch line here, so a command that resolves in the terminal
// also resolves for a process Memoh starts.
package shellenv

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/workspace/bridge"
	"github.com/felinics/memoh/internal/workspace/vpath"
)

const (
	pathOutEnv = "MEMOH_SHELL_PATH_OUT"
	// probeKillBackstop is how much later than probeTimeout the bridge kills the
	// probe, for workspaces where the shell could not be given its own timeout.
	probeKillBackstop = time.Second
)

// probeTimeout bounds an rc file that never returns. A variable so tests can
// exercise that case quickly; it is applied in whole seconds.
var probeTimeout = 4 * time.Second

// TerminalCommand is the launch line of the interactive browser terminal.
func TerminalCommand() string {
	return shellLine("exec ", "")
}

// shellLine decides bash-vs-sh inside the launched process, so it does not
// depend on a separate, potentially flaky probe exec.
func shellLine(run, args string) string {
	return `if [ -x /bin/bash ]; then ` + run + `/bin/bash` + args +
		`; elif [ -x /usr/bin/bash ]; then ` + run + `/usr/bin/bash` + args +
		`; elif command -v bash >/dev/null 2>&1; then ` + run + `bash` + args +
		`; else ` + run + `/bin/sh` + args + `; fi`
}

// pathProbeCommand runs the terminal's shell interactively so it reads the
// same rc files, then prints the PATH it ended up with.
//
// The shell writes its answer to a file and has no handle on the exec's own
// output: a process an rc file leaves in the background would otherwise keep
// that pipe open and hold every probe until its timeout. Stdin is detached so
// an rc file that reads input sees EOF.
//
// An interactive bash that finds a controlling terminal it is not in the
// foreground of stops itself until it is, which here means forever. Where
// setsid is available the shell therefore runs in a session of its own. That
// session is out of reach of the bridge's process-group kill, so it carries
// its own timeout; the bridge's later kill only backstops workspaces without
// those tools.
func pathProbeCommand(timeout time.Duration) string {
	detached := `setsid -w timeout -k 1 `
	return `memoh_path_out=$(mktemp) || exit 1; memoh_detach=""; ` +
		`if ` + detached + `1 true >/dev/null 2>&1; then memoh_detach="` + detached + strconv.Itoa(int(timeout/time.Second)) + `"; fi; ` +
		shellLine(pathOutEnv+`="$memoh_path_out" $memoh_detach `, ` -ic 'printf "%s" "$PATH" > "$`+pathOutEnv+`"' >/dev/null 2>&1 </dev/null`) +
		`; cat "$memoh_path_out"; rm -f "$memoh_path_out"`
}

// ResolvePath returns the command search path an interactive workspace shell
// ends up with when it starts from env and seedPath. Only PATH is taken from
// the shell: the rest of the environment stays under the caller's control.
func ResolvePath(ctx context.Context, client *bridge.Client, seedPath string, env, unsetEnv []string) (string, error) {
	if client == nil {
		return "", errors.New("workspace bridge client is required")
	}
	killAfter := probeTimeout + probeKillBackstop
	result, err := client.ExecWithOptions(ctx, pathProbeCommand(probeTimeout), vpath.DataMount, int32(killAfter.Seconds()), nil, bridge.ExecOptions{
		Env:      append(append([]string(nil), env...), "PATH="+seedPath),
		UnsetEnv: unsetEnv,
	})
	if err != nil {
		return "", fmt.Errorf("probe workspace shell PATH: %w", err)
	}
	if result.Stdout == "" {
		return "", fmt.Errorf("workspace shell exited with status %d without reporting a PATH", result.ExitCode)
	}
	return mergePaths(result.Stdout, seedPath), nil
}

// mergePaths puts the seed's entries after the shell's, so an rc file that
// overwrites PATH cannot hide the launcher's own directories. Empty and
// relative entries are dropped: both resolve against the working directory,
// which for a launched agent is the user's project, so a file in a checked-out
// repository could shadow a real command.
func mergePaths(shellPath, seedPath string) string {
	seen := map[string]struct{}{}
	entries := []string{}
	for _, entry := range strings.Split(shellPath+":"+seedPath, ":") {
		if _, duplicate := seen[entry]; duplicate || !strings.HasPrefix(entry, "/") {
			continue
		}
		seen[entry] = struct{}{}
		entries = append(entries, entry)
	}
	return strings.Join(entries, ":")
}
