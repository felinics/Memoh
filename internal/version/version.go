package version

import (
	"fmt"
	"runtime/debug"
)

var (
	// Version is the current version of the application.
	// It can be overridden by ldflags at build time.
	Version = "dev"
	// CommitHash is the git commit hash at build time.
	// It can be overridden by ldflags at build time.
	CommitHash = ""
	// BuildTime is the time when the application was built.
	// It can be overridden by ldflags at build time.
	BuildTime = ""
)

// EnsureBuildInfo populates CommitHash and BuildTime from Go build info
// when they were not set via ldflags.
func EnsureBuildInfo() {
	if CommitHash == "" {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, setting := range info.Settings {
				if setting.Key == "vcs.revision" {
					CommitHash = setting.Value
				}
				if setting.Key == "vcs.time" {
					BuildTime = setting.Value
				}
			}
		}
	}
}

// ShortCommitHash returns the first 7 characters of the commit hash.
func ShortCommitHash() string {
	EnsureBuildInfo()
	if len(CommitHash) > 7 {
		return CommitHash[:7]
	}
	return CommitHash
}

// GetInfo returns version metadata with a machine-readable build profile.
func GetInfo(buildProfile string) string {
	EnsureBuildInfo()
	res := Version
	if h := ShortCommitHash(); h != "" {
		res += fmt.Sprintf(" (%s)", h)
	}
	res += fmt.Sprintf(" profile=%s", buildProfile)
	return res
}
