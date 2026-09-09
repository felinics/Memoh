// Package workspacedeps manages catalog dependencies inside a bot's
// workspace: it runs catalog scripts over the bridge, discovers what is
// installed, probes the target platform, and caches the result per bot and
// workspace target.
//
// Every path helper takes the workspace data root as an argument. Native
// containers pass config.DefaultDataMount ("/data"); remote targets pass the
// target's default working directory. Resolving which root applies to a bot
// is the service layer's job, not this package's.
package workspacedeps

import "path"

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

// LocksDir holds stable per-dependency kernel lock files. Their inodes are
// never replaced or unlinked; the OS releases ownership on process exit.
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

// CurrentDir returns the `current` symlink path inside a dependency home. It
// points at the active entry below VersionsDir.
func CurrentDir(home string) string {
	return path.Join(home, currentLinkName)
}

// lockPath mirrors the kernel wrapper's stable lock file computation,
// "$(dirname "$MEMOH_DEP_HOME")/.locks/$MEMOH_DEP_ID.lock", so the runner can
// acquire the same lock while finalizing an operation.
func lockPath(home, depID string) string {
	return path.Join(path.Dir(home), locksDirName, depID+lockFileSuffix)
}
