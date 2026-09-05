package workspacedeps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/workspace/bridge"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

// runFixture bundles a real bridge client with a throwaway data root and a
// dedicated temporary directory, so tests can assert on leftovers.
type runFixture struct {
	client   *bridge.Client
	dataRoot string
	tmpDir   string
	platform Platform
}

func newRunFixture(t *testing.T) *runFixture {
	t.Helper()
	client := newExecTestClient(t)
	platform, err := ProbePlatform(testContext(t), client)
	if err != nil {
		t.Fatalf("ProbePlatform: %v", err)
	}
	tmpDir := t.TempDir()
	platform.TmpDir = tmpDir
	return &runFixture{client: client, dataRoot: t.TempDir(), tmpDir: tmpDir, platform: platform}
}

func (f *runFixture) spec(depID, script string) RunSpec {
	return RunSpec{
		DepID:    depID,
		Action:   catalog.ActionInstall,
		Script:   script,
		Home:     Home(f.dataRoot, depID),
		ShimDir:  ShimDir(f.dataRoot),
		Version:  "1.2.3",
		Platform: f.platform,
		Timeout:  30 * time.Second,
	}
}

func (f *runFixture) lockDir(depID string) string {
	return filepath.Join(LocksDir(f.dataRoot), depID+lockFileSuffix)
}

func (f *runFixture) assertNoLeftovers(t *testing.T, depID string) {
	t.Helper()
	entries, err := os.ReadDir(f.tmpDir)
	if err != nil {
		t.Fatalf("read tmp dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "memoh-dep-") {
			t.Errorf("result file %s was not removed", entry.Name())
		}
	}
	if _, err := os.Stat(f.lockDir(depID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("lock dir still present after run (stat err = %v)", err)
	}
}

func TestRunForwardsLogsAndReadsResult(t *testing.T) {
	f := newRunFixture(t)
	sink := newRecordingSink()
	script := strings.Join([]string{
		`dep_log hi`,
		`printf x | cat`,
		`read v || v=eof`,
		`dep_log "read=$v"`,
		`dep_log "home=$MEMOH_DEP_HOME bin=$MEMOH_DEP_BIN version=$MEMOH_DEP_VERSION os=$MEMOH_DEP_OS"`,
		`dep_result '{"version":"1.2.3","entrypoints":{"foo":"/tmp/x"}}'`,
	}, "\n")

	result, err := Run(testContext(t), f.client, f.spec("demo", script), sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if result.Version != "1.2.3" {
		t.Errorf("Version = %q, want 1.2.3", result.Version)
	}
	if got := result.Entrypoints["foo"]; got != "/tmp/x" {
		t.Errorf("Entrypoints[foo] = %q, want /tmp/x", got)
	}
	if !strings.Contains(string(result.Raw), `"entrypoints"`) {
		t.Errorf("Raw = %s, want the result file verbatim", result.Raw)
	}
	if !sink.has(StreamStderr, "hi") {
		t.Errorf("stderr lines = %q, want %q", sink.get(StreamStderr), "hi")
	}
	// `printf x` has no trailing newline: the partial line must be flushed at
	// exit and `cat` must have read /dev/null rather than the script.
	if !sink.has(StreamStdout, "x") {
		t.Errorf("stdout lines = %q, want %q", sink.get(StreamStdout), "x")
	}
	// `read` saw EOF instead of consuming the remaining script lines.
	if !sink.has(StreamStderr, "read=eof") {
		t.Errorf("stderr lines = %q, want read=eof", sink.get(StreamStderr))
	}
	wantEnv := "home=" + Home(f.dataRoot, "demo") + " bin=" + ShimDir(f.dataRoot) + " version=1.2.3 os=" + f.platform.OS
	if !sink.has(StreamStderr, wantEnv) {
		t.Errorf("stderr lines = %q, want %q", sink.get(StreamStderr), wantEnv)
	}
	for _, dir := range []string{Home(f.dataRoot, "demo"), VersionsDir(Home(f.dataRoot, "demo")), ShimDir(f.dataRoot)} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("expected directory %s to exist (err = %v)", dir, err)
		}
	}
	f.assertNoLeftovers(t, "demo")
}

func TestRunWithoutResultFileAndNilSink(t *testing.T) {
	f := newRunFixture(t)
	spec := f.spec("plain", "dep_log removing\nrm -rf \"$MEMOH_DEP_HOME\"\n")
	spec.Action = catalog.ActionRemove

	result, err := Run(testContext(t), f.client, spec, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Version != "" || result.Entrypoints != nil || len(result.Raw) != 0 {
		t.Errorf("Result = %+v, want empty", result)
	}
	f.assertNoLeftovers(t, "plain")
}

func TestRunNonZeroExitReturnsExitError(t *testing.T) {
	f := newRunFixture(t)
	sink := newRecordingSink()

	_, err := Run(testContext(t), f.client, f.spec("fail", "dep_log boom\nexit 3\n"), sink)
	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Run error = %v, want *ExitError", err)
	}
	if exitErr.Code != 3 {
		t.Errorf("Code = %d, want 3", exitErr.Code)
	}
	if !strings.Contains(exitErr.StderrTail, "boom") {
		t.Errorf("StderrTail = %q, want it to contain boom", exitErr.StderrTail)
	}
	if !strings.Contains(err.Error(), "status 3") {
		t.Errorf("Error() = %q, want the exit status", err.Error())
	}
	f.assertNoLeftovers(t, "fail")
}

func TestRunReturnsErrLockedAndKeepsForeignLock(t *testing.T) {
	f := newRunFixture(t)
	lock := f.lockDir("busy")
	if err := os.MkdirAll(lock, 0o750); err != nil {
		t.Fatalf("mkdir lock: %v", err)
	}

	_, err := Run(testContext(t), f.client, f.spec("busy", "dep_log should-not-run\n"), nil)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("Run error = %v, want ErrLocked", err)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Errorf("foreign lock must survive a locked run: %v", err)
	}
	entries, err := os.ReadDir(f.tmpDir)
	if err != nil {
		t.Fatalf("read tmp dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("tmp dir has leftovers after locked run: %v", entries)
	}
}

func TestRunReclaimsStaleLock(t *testing.T) {
	f := newRunFixture(t)
	lock := f.lockDir("stale")
	if err := os.MkdirAll(lock, 0o750); err != nil {
		t.Fatalf("mkdir lock: %v", err)
	}
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatalf("chtimes lock: %v", err)
	}
	sink := newRecordingSink()

	spec := f.spec("stale", "dep_log reclaimed\n")
	spec.Timeout = 10 * time.Second // stale threshold = 10s + 5min, far below 24h
	if _, err := Run(testContext(t), f.client, spec, sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !sink.has(StreamStderr, "reclaimed") {
		t.Errorf("stderr lines = %q, want reclaimed", sink.get(StreamStderr))
	}
	f.assertNoLeftovers(t, "stale")
}

var shellLineNumber = regexp.MustCompile(`sh: (?:line )?(\d+):`)

func TestRunRewritesShellLineNumbers(t *testing.T) {
	f := newRunFixture(t)
	sink := newRecordingSink()

	// Line 2 of the body is a syntax error; the shell reports it relative to
	// stdin, which includes the prelude, and the runner subtracts that.
	_, err := Run(testContext(t), f.client, f.spec("syntax", "dep_log first\nif then fi\n"), sink)
	if err == nil {
		t.Fatal("Run succeeded, want a syntax error exit")
	}
	var reported []int
	for _, line := range sink.get(StreamStderr) {
		if m := shellLineNumber.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			reported = append(reported, n)
		}
	}
	if len(reported) == 0 {
		t.Skipf("local sh does not report line numbers: %q", sink.get(StreamStderr))
	}
	for _, n := range reported {
		if n < 1 || n > 3 {
			t.Errorf("reported line %d, want a body-relative number near 2 (prelude is %d lines)", n, PreludeLines())
		}
	}
}

func TestRunDepSwitchReplacesCurrentAndKeepsOldVersion(t *testing.T) {
	f := newRunFixture(t)
	home := Home(f.dataRoot, "switch")
	script := strings.Join([]string{
		`mkdir -p "$MEMOH_DEP_HOME/versions/1.0.0" "$MEMOH_DEP_HOME/versions/2.0.0"`,
		`dep_switch "$MEMOH_DEP_HOME/versions/1.0.0"`,
		`dep_switch "$MEMOH_DEP_HOME/versions/2.0.0"`,
	}, "\n")

	if _, err := Run(testContext(t), f.client, f.spec("switch", script), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	target, err := os.Readlink(CurrentDir(home))
	if err != nil {
		t.Fatalf("readlink current: %v", err)
	}
	if want := filepath.Join(VersionsDir(home), "2.0.0"); target != want {
		t.Errorf("current -> %q, want %q", target, want)
	}
	if _, err := os.Stat(filepath.Join(VersionsDir(home), "1.0.0")); err != nil {
		t.Errorf("previous version directory must survive: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(home, "current.tmp")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("current.tmp must not remain (err = %v)", err)
	}
}

func TestRunTimeoutKillsScript(t *testing.T) {
	f := newRunFixture(t)
	spec := f.spec("slow", "sleep 30\n")
	spec.Timeout = time.Second

	start := time.Now()
	_, err := Run(testContext(t), f.client, spec, nil)
	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Run error = %v, want *ExitError", err)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("timeout took %s, want the bridge to kill the script promptly", elapsed)
	}
	f.assertNoLeftovers(t, "slow")
}

func TestRunCancelledContextReportsContextError(t *testing.T) {
	f := newRunFixture(t)
	ctx, cancel := context.WithCancel(testContext(t))
	sink := LogFunc(func(_, line string) {
		if line == "started" {
			cancel()
		}
	})

	_, err := Run(ctx, f.client, f.spec("cancel", "dep_log started\nsleep 30\n"), sink)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	// The script may still be running inside the workspace, so the lock must
	// stay until the stale rule reclaims it; only the result file goes.
	if _, err := os.Stat(f.lockDir("cancel")); err != nil {
		t.Errorf("lock must survive a cancelled run: %v", err)
	}
	entries, err := os.ReadDir(f.tmpDir)
	if err != nil {
		t.Fatalf("read tmp dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("tmp dir has leftovers after cancelled run: %v", entries)
	}
}

func TestRunRejectsIncompleteSpec(t *testing.T) {
	client := newExecTestClient(t)
	if _, err := Run(testContext(t), client, RunSpec{DepID: "x"}, nil); err == nil {
		t.Error("Run accepted a spec without home/shim/script")
	}
	if _, err := Run(testContext(t), nil, RunSpec{}, nil); err == nil {
		t.Error("Run accepted a nil client")
	}
}

func TestRewriteShellLine(t *testing.T) {
	offset := PreludeLines()
	cases := map[string]string{
		"sh: line " + strconv.Itoa(offset+2) + ": syntax error near unexpected token `then'": "sh: line 2: syntax error near unexpected token `then'",
		"sh: " + strconv.Itoa(offset+7) + ": foo: not found":                                 "sh: 7: foo: not found",
		"/bin/sh: line " + strconv.Itoa(offset+1) + ": boom":                                 "/bin/sh: line 1: boom",
		// Errors inside the prelude itself keep their number.
		"sh: line 3: unexpected": "sh: line 3: unexpected",
		"plain output":           "plain output",
	}
	for in, want := range cases {
		if got := rewriteShellLine(in); got != want {
			t.Errorf("rewriteShellLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLineSplitterCarriesPartialLines(t *testing.T) {
	var lines []string
	s := newLineSplitter(func(line string) { lines = append(lines, line) })
	s.write([]byte("ab"))
	s.write([]byte("c\r\nde"))
	s.write([]byte("\nf"))
	s.flush()
	want := []string{"abc", "de", "f"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Errorf("lines = %q, want %q", lines, want)
	}
}

func TestBuildEnvIncludesDesignVariables(t *testing.T) {
	spec := RunSpec{
		DepID:          "codex",
		Action:         catalog.ActionUpdate,
		Home:           "/data/.memoh/deps/codex",
		ShimDir:        "/data/.memoh/deps/bin",
		Version:        "0.151.0",
		CurrentVersion: "0.147.0",
		Candidate:      "/usr/bin/codex",
		Platform:       Platform{OS: "linux", Arch: "amd64", Libc: "glibc", TmpDir: "/tmp"},
		ExtraEnv:       []string{"NPM_MIRROR=https://mirror"},
	}
	env := strings.Join(buildEnv(spec, "/tmp/memoh-dep-codex-abc.json", 10*time.Minute), "\n")
	for _, want := range []string{
		"MEMOH_DEP_ID=codex",
		"MEMOH_DEP_ACTION=update",
		"MEMOH_DEP_HOME=/data/.memoh/deps/codex",
		"MEMOH_DEP_BIN=/data/.memoh/deps/bin",
		"MEMOH_DEP_VERSION=0.151.0",
		"MEMOH_DEP_CURRENT_VERSION=0.147.0",
		"MEMOH_DEP_RESULT=/tmp/memoh-dep-codex-abc.json",
		"MEMOH_DEP_CANDIDATE=/usr/bin/codex",
		"MEMOH_DEP_LOCK_STALE_SECONDS=900",
		"MEMOH_DEP_OS=linux",
		"MEMOH_DEP_ARCH=amd64",
		"MEMOH_DEP_LIBC=glibc",
		"DEBIAN_FRONTEND=noninteractive",
		"CI=1",
		"NPM_MIRROR=https://mirror",
	} {
		if !strings.Contains(env, want+"\n") && !strings.HasSuffix(env, want) {
			t.Errorf("env missing %q:\n%s", want, env)
		}
	}
}
