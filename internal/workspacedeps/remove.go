package workspacedeps

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

// removalScript includes workspace-owned copies in the prepared script, so
// removing an updated tool cannot silently expose the image's older copy.
// Keep cleanup inside the runner's lock and completion receipt. A catalog
// script's exit/return must not skip cleanup, and failure must stop it.
func removalScript(dep catalog.Dependency, script, toolkitRoot string) string {
	var b strings.Builder
	b.WriteString("(\n" + script + "\n)\n")
	b.WriteString("rm -rf -- \"$MEMOH_DEP_HOME\"\n")
	// Remote targets own their software layout through the reviewed recipe.
	// Only a native workspace has an isolated, writable image filesystem.
	fmt.Fprintf(&b, "if [ \"${MEMOH_DEP_WORKSPACE_TARGET:-}\" = %s ]; then\n", shellQuote(TargetNative))
	// Never follow a replaced ancestor into a different tree. rm removes
	// individual symlinks themselves, not their targets.
	fmt.Fprintf(&b, "  [ ! -L %s ] && [ ! -L %s ] || exit 1\n", shellQuote(toolkitRoot), shellQuote(path.Join(toolkitRoot, "bin")))
	commands := slices.Clone(dep.Provides)
	var trees []string
	// These paths belong to the canonical image assembly in docker/toolkit.
	// Do not infer recursive deletion paths from catalog names or PATH.
	switch dep.ID {
	case "node":
		commands = append(commands, "node", "npm", "npx")
		trees = []string{"node-glibc", "node-musl"}
	case "python":
		commands = append(commands, "python", "python3", "pip", "pip3")
		trees = []string{"python-gnu", "python-musl", "python-gnu-x86_64", "python-gnu-aarch64", "python-musl-x86_64", "python-musl-aarch64"}
	case "uv":
		commands = append(commands, "uv", "uvx")
		trees = []string{"uv"}
	}
	slices.Sort(commands)
	for _, command := range slices.Compact(commands) {
		if isPlainFileName(command) {
			fmt.Fprintf(&b, "  rm -f -- %s\n", shellQuote(path.Join(toolkitRoot, "bin", command)))
		}
	}
	for _, tree := range trees {
		fmt.Fprintf(&b, "  rm -rf -- %s\n", shellQuote(path.Join(toolkitRoot, tree)))
	}
	if dep.ID == "python" && toolkitRoot == path.Dir(toolkitBinDir) {
		b.WriteString(removeSystemPython)
	}
	b.WriteString("fi\n")
	return b.String()
}

// The Debian desktop image also includes distro Python. Let its package
// manager remove the interpreter and resolve dependents instead of unlinking
// package-owned files behind its back. Never autoremove unrelated packages.
// This runs only inside the native workspace branch above, and is included
// in the script available through the optional script viewer.
const removeSystemPython = `  if [ "${MEMOH_DEP_OS:-}" = linux ] && command -v dpkg-query >/dev/null 2>&1 && command -v apt-get >/dev/null 2>&1; then
    memoh_python_packages=$(dpkg-query -W -f='${binary:Package}\t${Status}\n' 'python3*' 2>/dev/null | awk '$2 == "install" && $3 == "ok" && $4 == "installed" && $1 ~ /^python3([.][0-9]+)?(-minimal)?(:[a-z0-9]+)?$/ { print $1 }')
    if [ -n "$memoh_python_packages" ]; then
      # Package names are restricted to interpreter packages by the pattern.
      # shellcheck disable=SC2086
      apt-get remove -y --no-auto-remove $memoh_python_packages
    fi
  fi
`

func actionScript(dep catalog.Dependency, action catalog.Action, script string) string {
	if action == catalog.ActionRemove {
		return removalScript(dep, script, path.Dir(toolkitBinDir))
	}
	return script
}
