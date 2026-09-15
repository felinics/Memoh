// Package workspacedeps manages catalog dependencies inside each bot's isolated
// workspace. Catalog scripts, discovery and cached state are scoped to the bot.
// Path helpers take the data root explicitly so tests can use temporary roots;
// production always uses the container's /data mount.
package workspacedeps

import (
	"path"

	"github.com/felinics/memoh/internal/workspace/controlpath"
)

const (
	shimDirName     = "bin"
	locksDirName    = ".locks"
	stateFileName   = "state.json"
	versionsDirName = "versions"
	currentLinkName = "current"
	lockFileSuffix  = ".lock"
)

// DepsRoot returns the directory holding every managed dependency:
// <dataRoot>/.memoh/deps.
func DepsRoot(dataRoot string) string {
	return path.Join(dataRoot, ".memoh", "deps")
}

// Home returns the root directory of one dependency. It is exported to
// scripts as MEMOH_DEP_HOME.
func Home(dataRoot, depID string) string {
	return path.Join(DepsRoot(dataRoot), depID)
}

// ShimDir returns the directory of generated shims that is injected into
// PATH ahead of the toolkit. It is exported to scripts as MEMOH_DEP_BIN.
func ShimDir(dataRoot string) string {
	return path.Join(DepsRoot(dataRoot), shimDirName)
}

// LocksDir names the legacy persistent lock directory, still used by Darwin.
// Linux kernel locks use its local control directory instead.
func LocksDir(dataRoot string) string {
	return path.Join(DepsRoot(dataRoot), locksDirName)
}

// StatePath returns the state.json path inside a dependency home.
func StatePath(home string) string {
	return path.Join(home, stateFileName)
}

// VersionsDir returns the directory holding one subdirectory per installed
// version inside a dependency home.
func VersionsDir(home string) string {
	return path.Join(home, versionsDirName)
}

// CurrentDir returns the stable `current` symlink to the active payload.
func CurrentDir(home string) string {
	return path.Join(home, currentLinkName)
}

// lockPath names the legacy lock inode; executionLockPath selects the current
// platform's kernel coordination directory.
func lockPath(home, depID string) string {
	return path.Join(path.Dir(home), locksDirName, depID+lockFileSuffix)
}

func executionLockPath(home, depID, osName string) string {
	return controlpath.TransactionLock(home, depID, osName)
}
