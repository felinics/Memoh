// Package shellprobe holds the command lines behind shellenv: which shell the
// workspace user works in, and how to ask that shell for the command search
// path its rc files give it. It has no dependencies, so both the server (via
// shellenv) and the bridge inside the workspace build the same lines.
package shellprobe

import (
	"strconv"
	"strings"
	"time"
)

const pathOutEnv = "MEMOH_SHELL_PATH_OUT"

// DefaultTimeout bounds an rc file that never returns. It is applied in whole
// seconds; the caller kills the probe a little later as a backstop.
const DefaultTimeout = 4 * time.Second

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

// PathProbeCommand is a command line that runs the terminal's shell
// interactively so it reads the same rc files, then prints the PATH it ended
// up with.
//
// The shell writes its answer to a file and has no handle on the exec's own
// output: a process an rc file leaves in the background would otherwise keep
// that pipe open and hold every probe until its timeout. Stdin is detached so
// an rc file that reads input sees EOF.
//
// An interactive bash that finds a controlling terminal it is not in the
// foreground of stops itself until it is, which here means forever. Where
// setsid is available the shell therefore runs in a session of its own. That
// session is out of reach of the caller's process-group kill, so it carries
// its own timeout; the caller's later kill only backstops workspaces without
// those tools.
func PathProbeCommand(timeout time.Duration) string {
	detached := `setsid -w timeout -k 1 `
	return `memoh_path_out=$(mktemp) || exit 1; memoh_detach=""; ` +
		`if ` + detached + `1 true >/dev/null 2>&1; then memoh_detach="` + detached + strconv.Itoa(int(timeout/time.Second)) + `"; fi; ` +
		shellLine(pathOutEnv+`="$memoh_path_out" $memoh_detach `, ` -ic 'printf "%s" "$PATH" > "$`+pathOutEnv+`"' >/dev/null 2>&1 </dev/null`) +
		`; cat "$memoh_path_out"; rm -f "$memoh_path_out"`
}

// MergePaths puts the seed's entries after the shell's, so an rc file that
// overwrites PATH cannot hide the launcher's own directories. Empty and
// relative entries are dropped: both resolve against the working directory,
// which for a launched agent is the user's project, so a file in a checked-out
// repository could shadow a real command.
func MergePaths(shellPath, seedPath string) string {
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
