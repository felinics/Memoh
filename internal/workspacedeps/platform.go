package workspacedeps

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

// Platform is the probed identity of a workspace target (design §12.4). It
// is exported to scripts as MEMOH_DEP_OS, MEMOH_DEP_ARCH, and MEMOH_DEP_LIBC
// and matched against catalog platform entries.
type Platform struct {
	// OS is the lower-cased kernel name: "linux" or "darwin".
	OS string
	// Arch is normalised to Go's vocabulary: "amd64", "arm64", or the raw
	// `uname -m` value when unknown.
	Arch string
	// Libc is "glibc" or "musl" on linux and empty elsewhere.
	Libc string
	// TmpDir is the target's ${TMPDIR:-/tmp} with any trailing slash removed.
	TmpDir string
}

// platformProbeScript gathers everything in one round trip. Output lines are:
// kernel name, machine, zero or more musl loader paths, temporary directory.
const platformProbeScript = `uname -s; uname -m; ls /lib/ld-musl-*.so.1 2>/dev/null; printf '%s\n' "${TMPDIR:-/tmp}"`

const platformProbeTimeoutSeconds = 15

// ProbePlatform runs the probe inside the workspace and normalises its
// output. It never trusts a platform the image declares about itself; the
// probe is the only source (WD-PLAT-004).
func ProbePlatform(ctx context.Context, client *bridge.Client) (Platform, error) {
	if client == nil {
		return Platform{}, errors.New("workspacedeps: bridge client is nil")
	}
	result, err := client.ExecWithOptions(ctx, platformProbeScript, "", platformProbeTimeoutSeconds, nil, bridge.ExecOptions{})
	if err != nil {
		return Platform{}, fmt.Errorf("workspacedeps: probe platform: %w", err)
	}
	if result.ExitCode != 0 {
		return Platform{}, fmt.Errorf("workspacedeps: probe platform exited %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	platform, err := parsePlatformOutput(result.Stdout)
	if err != nil {
		return Platform{}, fmt.Errorf("workspacedeps: probe platform: %w", err)
	}
	return platform, nil
}

// parsePlatformOutput decodes the stdout of platformProbeScript.
func parsePlatformOutput(stdout string) (Platform, error) {
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	if len(lines) < 3 || lines[0] == "" || lines[1] == "" {
		return Platform{}, fmt.Errorf("unexpected probe output %q", stdout)
	}
	platform := Platform{
		OS:     strings.ToLower(lines[0]),
		Arch:   normalizeArch(lines[1]),
		TmpDir: strings.TrimRight(lines[len(lines)-1], "/"),
	}
	if platform.TmpDir == "" {
		platform.TmpDir = defaultTmpDir
	}
	if platform.OS == "linux" {
		platform.Libc = "glibc"
		for _, line := range lines[2 : len(lines)-1] {
			if strings.Contains(line, "ld-musl-") {
				platform.Libc = "musl"
				break
			}
		}
	}
	return platform, nil
}

func normalizeArch(machine string) string {
	switch strings.ToLower(machine) {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	default:
		return strings.ToLower(machine)
	}
}

// env returns the MEMOH_DEP_OS/ARCH/LIBC entries for the script environment.
func (p Platform) env() []string {
	return []string{
		"MEMOH_DEP_OS=" + p.OS,
		"MEMOH_DEP_ARCH=" + p.Arch,
		"MEMOH_DEP_LIBC=" + p.Libc,
	}
}
