//go:build dockertest

package workspacedeps

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

// TestDockerCatalogScripts drives every installable catalog dependency through
// the real workspace image the way the runner would: the wrapped script
// arrives on stdin of `sh -s`, the environment is buildEnv's, and the result
// file is read back afterwards. Per dependency it runs install (latest) →
// entrypoint and layout checks → check_update → update to the very version
// in use (the WD-FS-001 re-install case: `current` must be valid afterwards
// and no .previous-*/.staging-* residue may remain) → remove.
//
// It needs a Docker daemon with the workspace image (memohai/workspace:debian
// by default, MEMOH_DEPS_TEST_IMAGE overrides it) and network access, and it
// takes several minutes per npm-based dependency:
//
//	go test -tags dockertest -run TestDockerCatalogScripts ./internal/workspacedeps/ -v -timeout 40m
//
// NPM_MIRROR, NODEJS_MIRROR, NODEJS_MUSL_MIRROR, and UV_RELEASES_URL are
// passed through when set.
func TestDockerCatalogScripts(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	image := os.Getenv("MEMOH_DEPS_TEST_IMAGE")
	if image == "" {
		image = dockerTestDefaultImage
	}
	platform := dockerImagePlatform(t, image)
	t.Logf("workspace image %s: %s/%s %s", image, platform.OS, platform.Arch, platform.Libc)

	cat, err := catalog.Load()
	if err != nil {
		t.Fatalf("catalog.Load() error = %v", err)
	}
	for _, dep := range cat.List() {
		if !dep.Installable() {
			continue
		}
		t.Run(dep.ID, func(t *testing.T) {
			t.Parallel()
			if !dep.SupportsPlatform(platform.OS, platform.Arch, platform.Libc) {
				t.Skipf("%s does not support %s/%s %s", dep.ID, platform.OS, platform.Arch, platform.Libc)
			}
			d := newDockerDep(t, cat, image, platform, dep)
			d.exercise()
		})
	}
}

const (
	dockerTestDefaultImage = "memohai/workspace:debian"
	// dockerTestDepsRoot is where the per-dependency volume is mounted inside
	// the container. It plays the role of DepsRoot(dataRoot), so the home is
	// /tmp/deps/<id>, the shim dir /tmp/deps/bin, and the lock directory
	// /tmp/deps/.locks exactly as the prelude derives it.
	dockerTestDepsRoot    = "/tmp/deps"
	dockerTestStepTimeout = 25 * time.Minute
	dockerTestShTimeout   = 3 * time.Minute
	dockerTestLogTail     = 40
)

// dockerTestPassthroughEnv lists host variables forwarded to the scripts, the
// same mirrors the Server may export (design §5.4).
var dockerTestPassthroughEnv = []string{"NPM_MIRROR", "NODEJS_MIRROR", "NODEJS_MUSL_MIRROR", "UV_RELEASES_URL"}

// dockerDep is one dependency under test together with its private volume.
type dockerDep struct {
	t        *testing.T
	cat      *catalog.Catalog
	image    string
	volume   string
	platform Platform
	dep      catalog.Dependency
	home     string
	shimDir  string
}

func newDockerDep(t *testing.T, cat *catalog.Catalog, image string, platform Platform, dep catalog.Dependency) *dockerDep {
	t.Helper()
	var nonce [4]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatalf("generate volume nonce: %v", err)
	}
	d := &dockerDep{
		t:        t,
		cat:      cat,
		image:    image,
		volume:   "memoh-deps-test-" + dep.ID + "-" + hex.EncodeToString(nonce[:]),
		platform: platform,
		dep:      dep,
		home:     path.Join(dockerTestDepsRoot, dep.ID),
		shimDir:  path.Join(dockerTestDepsRoot, "bin"),
	}
	ctx, cancel := context.WithTimeout(t.Context(), dockerTestShTimeout)
	defer cancel()
	if out, err := dockerCommand(ctx, "volume", "create", d.volume).CombinedOutput(); err != nil {
		t.Fatalf("docker volume create: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), dockerTestShTimeout)
		defer cancel()
		_ = dockerCommand(ctx, "volume", "rm", "-f", d.volume).Run()
	})
	// Run creates these before any script starts (runner.go).
	if out, err := d.sh(ctx, `mkdir -p -- "$@"`, d.home, VersionsDir(d.home), d.shimDir, path.Dir(lockPath(d.home, dep.ID)), path.Join(dockerTestDepsRoot, ".results")); err != nil {
		t.Fatalf("prepare volume: %v\n%s", err, out)
	}
	return d
}

// exercise runs the full lifecycle and fails the test on the first problem.
func (d *dockerDep) exercise() {
	t := d.t
	t.Helper()

	installed := d.runScript(catalog.ActionInstall, "", "")
	if installed.Version == "" {
		t.Fatalf("%s: install reported no version: %s", d.dep.ID, installed.Raw)
	}
	t.Logf("%s: installed version %s", d.dep.ID, installed.Version)
	d.verifyEntrypoints(installed)
	d.verifyLayout(installed.Version)

	check := d.runScript(catalog.ActionCheckUpdate, "", installed.Version)
	var decoded updateCheck
	if err := json.Unmarshal(check.Raw, &decoded); err != nil {
		t.Fatalf("%s: decode check_update result %s: %v", d.dep.ID, check.Raw, err)
	}
	if decoded.Installed != installed.Version {
		t.Errorf("%s: check_update installed = %q, want %q", d.dep.ID, decoded.Installed, installed.Version)
	}
	if decoded.Latest == "" {
		t.Errorf("%s: check_update reported no latest version: %s", d.dep.ID, check.Raw)
	}
	if want := decoded.Installed != decoded.Latest; decoded.UpdateAvailable != want {
		t.Errorf("%s: check_update update_available = %v with installed %q latest %q", d.dep.ID, decoded.UpdateAvailable, decoded.Installed, decoded.Latest)
	}
	t.Logf("%s: check_update → %s", d.dep.ID, check.Raw)

	// Re-installing the version in use exercises the explicit version path and
	// the WD-FS-001 commit sequence: versions/<ver> is set aside, the staged
	// tree moves in, `current` is switched, and only then is the old tree
	// deleted. Afterwards `current` must resolve to the fresh tree and nothing
	// may be left behind.
	updated := d.runScript(catalog.ActionUpdate, installed.Version, installed.Version)
	if updated.Version != installed.Version {
		t.Errorf("%s: update to %q reported version %q", d.dep.ID, installed.Version, updated.Version)
	}
	d.verifyEntrypoints(updated)
	d.verifyLayout(updated.Version)

	removed := d.runScript(catalog.ActionRemove, "", installed.Version)
	if strings.TrimSpace(string(removed.Raw)) != "{}" {
		t.Errorf("%s: remove result = %q, want {}", d.dep.ID, removed.Raw)
	}
	ctx, cancel := context.WithTimeout(t.Context(), dockerTestShTimeout)
	defer cancel()
	if out, err := d.sh(ctx, `test ! -e "$1"`, d.home); err != nil {
		t.Errorf("%s: home %s still exists after remove: %v %s", d.dep.ID, d.home, err, out)
	}
}

// runScript executes one catalog script the way Run does and returns the
// decoded result file. A non-zero exit fails the test with the log tails.
func (d *dockerDep) runScript(action catalog.Action, version, currentVersion string) Result {
	t := d.t
	t.Helper()
	script, ok := d.cat.Script(d.dep.ID, action)
	if !ok {
		t.Fatalf("%s has no %s script", d.dep.ID, action)
	}
	resultPath := path.Join(dockerTestDepsRoot, ".results", d.dep.ID+"-"+string(action)+".json")
	timeout := d.dep.Timeouts.Duration(action)
	env := buildEnv(RunSpec{
		DepID:          d.dep.ID,
		Action:         action,
		Script:         script,
		Home:           d.home,
		ShimDir:        d.shimDir,
		Version:        version,
		CurrentVersion: currentVersion,
		Platform:       d.platform,
		Timeout:        timeout,
	}, resultPath, timeout)
	args := []string{"run", "--rm", "-i", "-v", d.volume + ":" + dockerTestDepsRoot}
	for _, kv := range env {
		args = append(args, "-e", kv)
	}
	for _, key := range dockerTestPassthroughEnv {
		if value, set := os.LookupEnv(key); set {
			args = append(args, "-e", key+"="+value)
		}
	}
	args = append(args, d.image, "sh", "-s")

	ctx, cancel := context.WithTimeout(t.Context(), dockerTestStepTimeout)
	defer cancel()
	cmd := dockerCommand(ctx, args...)
	cmd.Stdin = strings.NewReader(WrapScript(script))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start).Round(time.Second)

	// Read the result and release the lock the way cleanupRun does.
	raw, readErr := d.sh(ctx, `cat -- "$1" 2>/dev/null; rm -f -- "$1"; rmdir -- "$2" 2>/dev/null; true`, resultPath, lockPath(d.home, d.dep.ID))
	if runErr != nil {
		code := -1
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			code = exitErr.ExitCode()
		}
		t.Fatalf("%s %s exited %d after %s: %v\n--- stdout tail ---\n%s\n--- stderr tail ---\n%s",
			d.dep.ID, action, code, elapsed, runErr, tailLines(stdout.String()), tailLines(stderr.String()))
	}
	if readErr != nil {
		t.Fatalf("%s %s: read result %s: %v\n%s", d.dep.ID, action, resultPath, readErr, raw)
	}
	t.Logf("%s: %s finished in %s", d.dep.ID, action, elapsed)
	return decodeDockerResult(t, raw)
}

// verifyLayout checks the dependency home after a commit: `current` is a
// symlink to versions/<version>, that directory holds the primary command, and
// versions/ contains neither a staging directory nor a set-aside .previous-*
// tree (WD-FS-001 clean-up).
func (d *dockerDep) verifyLayout(version string) {
	t := d.t
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), dockerTestShTimeout)
	defer cancel()
	wantTarget := path.Join(VersionsDir(d.home), version)
	out, err := d.sh(ctx, `readlink -- "$1" && test -d "$1/" && test -x "$1/bin/$2"`, CurrentDir(d.home), d.dep.Provides[0])
	if err != nil {
		t.Errorf("%s: current is not a valid link to an installed tree: %v\n%s", d.dep.ID, err, out)
	} else if got := firstLine(out); got != wantTarget {
		t.Errorf("%s: current -> %q, want %q", d.dep.ID, got, wantTarget)
	}
	listing, err := d.sh(ctx, `ls -A -- "$1"`, VersionsDir(d.home))
	if err != nil {
		t.Errorf("%s: list %s: %v\n%s", d.dep.ID, VersionsDir(d.home), err, listing)
		return
	}
	for _, entry := range strings.Split(listing, "\n") {
		if strings.Contains(entry, ".previous-") || strings.HasPrefix(entry, ".staging-") {
			t.Errorf("%s: versions/ still holds %q after commit", d.dep.ID, entry)
		}
	}
	if !strings.Contains(listing, version) {
		t.Errorf("%s: versions/ = %q lacks %q", d.dep.ID, listing, version)
	}
	t.Logf("%s: current -> %s; versions/ = %s", d.dep.ID, wantTarget, strings.Join(strings.Fields(listing), " "))
}

// verifyEntrypoints checks that every provided command resolves to
// <home>/current/bin/<cmd>, is executable, and answers --version; the primary
// command must report the installed version.
func (d *dockerDep) verifyEntrypoints(res Result) {
	t := d.t
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), dockerTestShTimeout)
	defer cancel()
	for i, command := range d.dep.Provides {
		got := res.Entrypoints[command]
		want := path.Join(CurrentDir(d.home), "bin", command)
		if got != want {
			t.Errorf("%s: entrypoint %s = %q, want %q", d.dep.ID, command, got, want)
			continue
		}
		out, err := d.sh(ctx, `test -x "$1" && exec "$1" --version`, got)
		if err != nil {
			t.Errorf("%s: %s --version failed: %v\n%s", d.dep.ID, got, err, out)
			continue
		}
		if i == 0 && !strings.Contains(out, res.Version) {
			t.Errorf("%s: %s --version = %q, want it to mention %q", d.dep.ID, command, out, res.Version)
		}
		t.Logf("%s: %s --version → %s", d.dep.ID, command, firstLine(out))
	}
}

// sh runs a shell snippet inside a fresh container on the dependency's volume
// and returns its combined output.
func (d *dockerDep) sh(ctx context.Context, script string, args ...string) (string, error) {
	cmdArgs := []string{"run", "--rm", "-v", d.volume + ":" + dockerTestDepsRoot, d.image, "sh", "-c", script, "sh"}
	cmdArgs = append(cmdArgs, args...)
	var out bytes.Buffer
	cmd := dockerCommand(ctx, cmdArgs...)
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

// dockerImagePlatform probes the image with the Server's own platform probe.
// It skips the test when the image is not available locally.
func dockerImagePlatform(t *testing.T, image string) Platform {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), dockerTestShTimeout)
	defer cancel()
	if out, err := dockerCommand(ctx, "image", "inspect", "--format", "{{.Id}}", image).CombinedOutput(); err != nil {
		// Only a missing image is a reason to skip; an unresponsive daemon
		// must fail loudly instead of hiding behind a skipped test.
		if strings.Contains(string(out), "No such image") {
			t.Skipf("workspace image %s not available: %v\n%s", image, err, out)
		}
		t.Fatalf("docker image inspect %s: %v\n%s", image, err, out)
	}
	out, err := dockerCommand(ctx, "run", "--rm", image, "sh", "-c", platformProbeScript).Output()
	if err != nil {
		t.Fatalf("probe platform of %s: %v", image, err)
	}
	platform, err := parsePlatformOutput(string(out))
	if err != nil {
		t.Fatalf("parse platform probe of %s: %v", image, err)
	}
	return platform
}

func dockerCommand(ctx context.Context, args ...string) *exec.Cmd {
	//nolint:gosec // test harness; every argument comes from the catalog or this file
	return exec.CommandContext(ctx, "docker", args...)
}

func decodeDockerResult(t *testing.T, raw string) Result {
	t.Helper()
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Result{}
	}
	var decoded resultFile
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("decode result %q: %v", raw, err)
	}
	return Result{
		Version:     strings.TrimSpace(decoded.Version),
		Entrypoints: decoded.Entrypoints,
		Raw:         json.RawMessage(raw),
	}
}

func tailLines(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > dockerTestLogTail {
		lines = lines[len(lines)-dockerTestLogTail:]
	}
	return strings.Join(lines, "\n")
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
