package workspacedeps

import "strings"

// toolkitBinDir is the workspace image's toolkit bin directory. It is
// appended to PATH when present and consulted by discovery as the fallback
// location for image-provided commands. Remote targets do not have it.
const toolkitBinDir = "/opt/memoh/toolkit/bin"

// exitCodeLocked is the exit status the prelude uses when another run holds
// the dependency lock. The runner maps it to ErrLocked.
const exitCodeLocked = 75

// prelude is the POSIX sh text the runner feeds to `sh -s` ahead of every
// catalog script (design §5.3). It must stay POSIX (WD-CAT-003) and must pass
// `shellcheck -s sh`; TestPreludeShellcheck enforces the latter.
//
// The text ends with the opening of memoh_dep_main so the script body is
// parsed as a function and later invoked with stdin redirected from
// /dev/null. Anything in the body that reads stdin (`read`, npm prompts,
// apt) therefore sees EOF instead of eating the rest of the script.
const prelude = `# ---- memoh dependency runner prelude (injected; design §5.3) ----
set -eu
export DEBIAN_FRONTEND=noninteractive CI=1
export PATH="$MEMOH_DEP_BIN:$PATH"
if [ -d ` + toolkitBinDir + ` ]; then
  export PATH="$PATH:` + toolkitBinDir + `"
fi

dep_log()    { printf '%s\n' "$*" >&2; }
dep_result() { printf '%s' "$1" > "$MEMOH_DEP_RESULT"; }
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

# Per-dependency lock against concurrent Server instances (design §8.4). A
# lock older than MEMOH_DEP_LOCK_STALE_SECONDS is a leftover from a killed run
# and is reclaimed once; exit 75 tells the runner the dependency is busy.
memoh_dep_lock="$(dirname "$MEMOH_DEP_HOME")/.locks/$MEMOH_DEP_ID.lock"
mkdir -p "$(dirname "$memoh_dep_lock")"
if ! mkdir "$memoh_dep_lock" 2>/dev/null; then
  memoh_dep_stale_minutes=$((MEMOH_DEP_LOCK_STALE_SECONDS / 60))
  if [ "$memoh_dep_stale_minutes" -lt 1 ]; then memoh_dep_stale_minutes=1; fi
  if [ -n "$(find "$memoh_dep_lock" -maxdepth 0 -mmin +"$memoh_dep_stale_minutes" 2>/dev/null)" ]; then
    rmdir "$memoh_dep_lock" 2>/dev/null || true
    mkdir "$memoh_dep_lock" 2>/dev/null || exit 75
  else
    exit 75
  fi
fi
# The runner removes the lock once the process has exited. No EXIT trap: bash
# reports a syntax error's status as 0 when one is installed.

memoh_dep_main() {
`

// preludeEpilogue closes the function opened by prelude and runs it with
// stdin detached from the script source.
const preludeEpilogue = "}\nmemoh_dep_main < /dev/null\n"

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
