package workspacedeps

import "strings"

// toolkitBinDir is the workspace image's toolkit bin directory. It is
// appended to PATH when present and consulted by discovery as the fallback
// location for image-provided commands. Remote targets do not have it.
const toolkitBinDir = "/opt/memoh/toolkit/bin"

// exitCodeLocked is the status returned by the OS locker on contention.
// The runner also checks that the prelude never reported acquisition.
const (
	exitCodeLocked     = 75
	lockAcquiredMarker = "__MEMOH_DEP_LOCK_ACQUIRED__"
)

// lockProbeHelpers asks the kernel whether the stable lock file is held.
// Lock files are never unlinked: doing so would create a second lock inode.
// Legacy directory locks are refused rather than reclaimed behind a live run.
const lockProbeHelpers = `memoh_lock_active() {
  [ -d "$1" ] && return 0
  [ -f "$1" ] || return 1
  case "$(uname -s)" in
    Darwin) ! lockf -k -t 0 "$1" true >/dev/null 2>&1 ;;
    *) ! flock -n "$1" true >/dev/null 2>&1 ;;
  esac
}
`

// scriptExecWrapper acquires an OS advisory lock before sh reads the script.
// flock and lockf retain the same inode until the entire command exits, and
// the kernel releases it on crash. There is no user-space stale-lock deletion.
const scriptExecWrapper = `memoh_lock="$(dirname "$MEMOH_DEP_HOME")/.locks/$MEMOH_DEP_ID.lock"
mkdir -p "$(dirname "$memoh_lock")"
if [ -d "$memoh_lock" ]; then
  printf '%s\n' 'legacy dependency lock directory requires operator recovery' >&2
  exit 73
fi
case "${MEMOH_DEP_OS:-$(uname -s)}" in
  darwin|Darwin)
    # FD mode keeps the lock in the shell and its descendants. Command mode
    # unlocks when the lockf supervisor exits, even if its child is still alive.
    exec 9>> "$memoh_lock"
    lockf -k -t 0 9 || exit "$?"
    exec sh -s ;;
  *) exec flock -n -E 75 "$memoh_lock" sh -s ;;
esac`

// prelude is the POSIX sh text the runner feeds to `sh -s` ahead of every
// catalog script. It must stay POSIX and must pass
// `shellcheck -s sh`; TestPreludeShellcheck enforces the latter.
//
// The text ends with the opening of memoh_dep_main so the script body is
// parsed as a function and later invoked with stdin redirected from
// /dev/null. Anything in the body that reads stdin (`read`, npm prompts,
// apt) therefore sees EOF instead of eating the rest of the script.
const prelude = `# ---- memoh dependency runner prelude (injected) ----
set -eu
export DEBIAN_FRONTEND=noninteractive CI=1
export PATH="$MEMOH_DEP_BIN:$PATH"
if [ -d ` + toolkitBinDir + ` ]; then
  export PATH="$PATH:` + toolkitBinDir + `"
fi

# Public recipe helpers are intentionally usable from arbitrary script bodies.
# shellcheck disable=SC2329
dep_log()    { printf '%s\n' "$*" >&2; }
# shellcheck disable=SC2329
dep_result() { printf '%s' "$1" > "$MEMOH_DEP_RESULT"; }
# shellcheck disable=SC2329
dep_switch() {
  case "$MEMOH_DEP_OS" in
    darwin)
      # BSD mv has no -T. ln -sfh is unlink+create, close enough to atomic for
      # a user-confirmed foreground operation on a remote target.
      ln -sfh "$1" "$MEMOH_DEP_HOME/current" ;;
    *)
      ln -sfn "$1" "$MEMOH_DEP_HOME/current.tmp"
      mv -Tf "$MEMOH_DEP_HOME/current.tmp" "$MEMOH_DEP_HOME/current" ;;
  esac
}

# Reaching the prelude proves the kernel lock was acquired, even when the
# recipe itself later exits 75.
printf '%s\n' '` + lockAcquiredMarker + `' >&2
if [ -n "${MEMOH_DEP_OPERATION_DIR:-}" ]; then
  memoh_operation_parent="$(dirname "$MEMOH_DEP_OPERATION_DIR")"
  # A reaper can cancel a claimed operation before this process ever starts.
  # Check its durable fence while holding the same kernel lock as the reaper.
  if [ -f "$memoh_operation_parent/.cancelled-${MEMOH_DEP_OPERATION_DIR##*/}" ]; then
    printf '%s\n' 'dependency operation was cancelled before execution' >&2
    exit 76
  fi
  printf '%s\n' "${MEMOH_DEP_OPERATION_DIR##*/}" > "$memoh_operation_parent/current.$$"
  mv -f "$memoh_operation_parent/current.$$" "$memoh_operation_parent/current"
fi

memoh_dep_main() {
`

// preludeEpilogue closes the function opened by prelude and runs it with
// stdin detached from the script source.
const preludeEpilogue = `}
set +e
(set -e; memoh_dep_main < /dev/null)
memoh_exit=$?
set -e
if [ -n "${MEMOH_DEP_OPERATION_DIR:-}" ]; then
  printf '%s\n' "$memoh_exit" > "$MEMOH_DEP_OPERATION_DIR/exit-code.tmp"
  mv -f "$MEMOH_DEP_OPERATION_DIR/exit-code.tmp" "$MEMOH_DEP_OPERATION_DIR/exit-code"
fi
exit "$memoh_exit"
`

// preludeLines is the number of stdin lines that precede the first line of
// the script body. Shells report syntax and runtime errors with line numbers
// counted from the start of stdin, so the runner subtracts this offset before
// forwarding stderr to the user.
var preludeLines = strings.Count(prelude, "\n")

// PreludeLines returns the line offset the prelude adds in front of a script
// body. Line k of the body is line PreludeLines()+k of what the shell reads.
func PreludeLines() int {
	return preludeLines
}

// WrapScript returns the full stdin text for `sh -s`: the prelude, the body
// as the contents of memoh_dep_main, and the call that runs it with stdin
// redirected from /dev/null.
func WrapScript(body string) string {
	var b strings.Builder
	b.Grow(len(prelude) + len(body) + len(preludeEpilogue) + 1)
	b.WriteString(prelude)
	b.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString(preludeEpilogue)
	return b.String()
}

// shellQuote returns s as a single-quoted POSIX sh word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
