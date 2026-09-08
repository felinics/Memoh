package workspacedeps

import (
	"context"
	"errors"
	"os"
	"os/exec"
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
		Receipt:  &OperationReceipt{},
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
	info, err := os.Stat(f.lockDir(depID))
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("stable kernel lock file must remain: %v", err)
	}
	result, err := f.client.Exec(testContext(t), lockProbeHelpers+"\nif memoh_lock_active "+shellQuote(f.lockDir(depID))+"; then exit 1; fi", "", 5)
	if err != nil || result.ExitCode != 0 {
		t.Errorf("kernel lock remained held after exit: %v", err)
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

func TestRunRefusesLegacyDirectoryLock(t *testing.T) {
	f := newRunFixture(t)
	lock := f.lockDir("busy")
	if err := os.MkdirAll(lock, 0o750); err != nil {
		t.Fatalf("mkdir lock: %v", err)
	}

	_, err := Run(testContext(t), f.client, f.spec("busy", "dep_log should-not-run\n"), nil)
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 73 {
		t.Fatalf("legacy directory lock must require operator recovery: %v", err)
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

func TestRunReusesUnlockedKernelFileRegardlessOfAge(t *testing.T) {
	f := newRunFixture(t)
	lock := f.lockDir("stale")
	if err := os.MkdirAll(filepath.Dir(lock), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(lock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(testContext(t), f.client, f.spec("stale", "true\n"), nil); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(lock)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("runner replaced the kernel lock inode: %v", err)
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

func TestRunTimeoutKillsScriptAndReleasesKernelLock(t *testing.T) {
	f := newRunFixture(t)
	fifo := filepath.Join(t.TempDir(), "never-release")
	if err := exec.CommandContext(testContext(t), "mkfifo", fifo).Run(); err != nil { //nolint:gosec // G204: fixed command creates a synthetic FIFO under t.TempDir.
		t.Fatal(err)
	}
	spec := f.spec("slow", "read release < "+shellQuote(fifo)+"\nexit 99\n")
	spec.Timeout = time.Second
	_, err := Run(testContext(t), f.client, spec, nil)
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 137 {
		t.Fatalf("blocked process must be killed by the bridge: %v", err)
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

	fifo := filepath.Join(t.TempDir(), "release")
	if err := exec.CommandContext(testContext(t), "mkfifo", fifo).Run(); err != nil { //nolint:gosec // G204: fixed command creates a synthetic FIFO under t.TempDir.
		t.Fatal(err)
	}
	body := "dep_result '{\"version\":\"1.0.0\"}'\ndep_log started\nread release < " + shellQuote(fifo) + "\n"
	result, err := Run(ctx, f.client, f.spec("cancel", body), sink)
	// Positive-timeout bridge execs outlive the stream. Release the actual
	// child explicitly after cancellation instead of leaving a timed sleep.
	file, openErr := os.OpenFile(fifo, os.O_WRONLY, 0) //nolint:gosec // G304: synthetic FIFO created under t.TempDir.
	if openErr != nil {
		t.Fatal(openErr)
	}
	_, _ = file.WriteString("done\n")
	_ = file.Close()
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrOperationUncertain) {
		t.Fatalf("Run error = %v, want uncertain outcome wrapping context.Canceled", err)
	}
	// The script may still be running inside the workspace, so the lock must
	// stay until the owner's liveness is checked, together with its result.
	if _, err := os.Stat(f.lockDir("cancel")); err != nil {
		t.Errorf("lock must survive a cancelled run: %v", err)
	}
	if result.Receipt == nil {
		t.Fatal("uncertain operation lost its receipt identity")
	}
	retained, err := os.ReadFile(filepath.Join(result.Receipt.Directory, "result.json"))
	if err != nil || string(retained) != `{"version":"1.0.0"}` {
		t.Fatalf("retained operation result = %q, %v", retained, err)
	}
	// Blocking acquisition is an explicit process-completion barrier: no
	// polling or sleep guesses whether the detached child finished.
	lockCommand := "flock " + shellQuote(f.lockDir("cancel")) + " true"
	if f.platform.OS == "darwin" {
		lockCommand = "lockf -k -t 5 " + shellQuote(f.lockDir("cancel")) + " true"
	}
	barrier, err := f.client.Exec(testContext(t), lockCommand, "", 5)
	if err != nil || barrier.ExitCode != 0 {
		t.Fatalf("detached child failed to release its kernel lock: %v", err)
	}
	receipt, err := readOperationReceipt(testContext(t), f.client, Home(f.dataRoot, "cancel"), "cancel")
	if err != nil || receipt == nil || !receipt.Completed || receipt.ExitCode != 0 || receipt.Result.Version != "1.0.0" {
		t.Fatalf("successful detached operation cannot be recovered: %+v, %v", receipt, err)
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
		"MEMOH_DEP_TIMEOUT_SECONDS=600",
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

func TestShortProbeCannotReclaimLiveLongInstall(t *testing.T) {
	f := newRunFixture(t)
	fifo := filepath.Join(t.TempDir(), "release")
	if err := exec.CommandContext(testContext(t), "mkfifo", fifo).Run(); err != nil { //nolint:gosec // G204: fixed command creates a synthetic FIFO under t.TempDir.
		t.Fatal(err)
	}
	ready := make(chan struct{}, 1)
	done := make(chan error, 1)
	spec := f.spec("long-install", "dep_log ready\nread release < "+shellQuote(fifo)+"\n")
	spec.Timeout = 20 * time.Minute
	ctx := testContext(t)
	oldReceipt, err := prepareReceipt(ctx, f.client, spec)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, err := Run(ctx, f.client, spec, LogFunc(func(_ string, line string) {
			if line == "ready" {
				ready <- struct{}{}
			}
		}))
		done <- err
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("install exited before synchronization: %v", err)
	case <-testContext(t).Done():
		t.Fatal("install did not reach the synchronization point")
	}
	// A previous Server may finish bookkeeping while the next operation
	// already holds the kernel lock. Its receipt cleanup must not unlock it.
	if err := CleanupReceipt(ctx, f.client, oldReceipt); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-6 * time.Minute)
	if err := os.Chtimes(f.lockDir(spec.DepID), old, old); err != nil {
		t.Fatal(err)
	}
	contender := f.spec(spec.DepID, "exit 99\n")
	contender.Action = catalog.ActionVersion
	contender.Timeout = 30 * time.Second
	_, blocked := Run(testContext(t), f.client, contender, nil)
	// Release the real owner before asserting, including failure paths.
	file, err := os.OpenFile(fifo, os.O_WRONLY, 0) //nolint:gosec // G304: synthetic FIFO created under t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("done\n")
	_ = file.Close()
	if err := <-done; err != nil {
		t.Fatalf("owner install: %v", err)
	}
	if !errors.Is(blocked, ErrLocked) {
		t.Fatalf("short contender stole live install lock: %v", blocked)
	}
	f.assertNoLeftovers(t, spec.DepID)
}

func TestScriptExit75IsNotLockContention(t *testing.T) {
	f := newRunFixture(t)
	_, err := Run(testContext(t), f.client, f.spec("exit75", "exit 75\n"), nil)
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 75 || errors.Is(err, ErrLocked) {
		t.Fatalf("script exit 75 lost its actual failure: %v", err)
	}
	f.assertNoLeftovers(t, "exit75")
}

func TestMirrorEnvironmentIsExplicitlyAllowlisted(t *testing.T) {
	t.Setenv("NODEJS_MIRROR", "https://node.example")
	t.Setenv("UV_RELEASES_URL", "https://uv.example")
	t.Setenv("NPM_MIRROR", "https://npm.example")
	t.Setenv("UV_PYTHON_INSTALL_MIRROR", "https://python.example")
	t.Setenv("UNRELATED_SERVER_SECRET", "must-not-forward")
	env := buildEnv(RunSpec{DepID: "tool", ExtraEnv: []string{
		"NPM_MIRROR=https://override.example", "MEMOH_DEP_HOME=/attacker", "UNRELATED_SERVER_SECRET=override",
	}}, "/tmp/result", time.Minute)
	joined := strings.Join(env, "\n")
	for _, want := range []string{"NODEJS_MIRROR=https://node.example", "UV_RELEASES_URL=https://uv.example", "NPM_MIRROR=https://override.example", "UV_PYTHON_INSTALL_MIRROR=https://python.example"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing configured mirror %s", want)
		}
	}
	if strings.Contains(joined, "SECRET") || strings.Contains(joined, "/attacker") || strings.Contains(joined, "NPM_MIRROR=https://npm.example") {
		t.Fatalf("environment escaped allowlist or override precedence: %s", joined)
	}
}

func TestRunReceiptSurvivesRemovingDependencyHome(t *testing.T) {
	f := newRunFixture(t)
	spec := f.spec("remove-receipt", "rm -rf \"$MEMOH_DEP_HOME\"\ndep_result '{}'\n")
	spec.Action = catalog.ActionRemove
	spec.Receipt = &OperationReceipt{
		ID:                 strings.Repeat("c", 32),
		StartedAt:          time.Date(2026, 9, 8, 0, 0, 0, 123, time.UTC),
		DefinitionRevision: strings.Repeat("a", 64),
		ManifestDigest:     "sha256:" + strings.Repeat("b", 64),
		Previous:           &State{Version: "1.0.0", Entrypoints: map[string]string{"tool": "/old/tool"}},
	}
	result, err := Run(testContext(t), f.client, spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(spec.Home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove did not delete its home: %v", err)
	}
	receipt, err := readOperationReceipt(testContext(t), f.client, spec.Home, spec.DepID)
	if err != nil || receipt == nil {
		t.Fatalf("remove lost its durable receipt: %+v, %v", receipt, err)
	}
	if !receipt.Completed || receipt.ExitCode != 0 || receipt.Action != catalog.ActionRemove || receipt.ID != spec.Receipt.ID || !receipt.StartedAt.Equal(spec.Receipt.StartedAt) || receipt.Previous.Version != "1.0.0" {
		t.Fatalf("frozen metadata or result changed: %+v", receipt)
	}
	if err := CleanupReceipt(testContext(t), f.client, result.Receipt); err != nil {
		t.Fatal(err)
	}
	acknowledged, err := readOperationReceipt(testContext(t), f.client, spec.Home, spec.DepID)
	if err != nil || acknowledged != nil {
		t.Fatalf("acknowledged receipt still discoverable: %+v, %v", acknowledged, err)
	}
}

func TestReadOnlyCheckCannotReplaceMutationReceipt(t *testing.T) {
	f := newRunFixture(t)
	spec := f.spec("receipt-check", "dep_result '{\"version\":\"1.0.0\"}'\n")
	installed, err := Run(testContext(t), f.client, spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	check := f.spec(spec.DepID, "dep_result '{\"version\":\"2.0.0\"}'\n")
	check.Action = catalog.ActionCheckUpdate
	checked, err := Run(testContext(t), f.client, check, nil)
	if err != nil || checked.Receipt != nil || checked.Version != "2.0.0" {
		t.Fatalf("read-only check result: %+v, %v", checked, err)
	}
	current, err := ReadOperationReceipt(testContext(t), f.client, spec.Home, spec.DepID)
	if err != nil || current == nil || current.ID != installed.Receipt.ID || current.Result.Version != "1.0.0" {
		t.Fatalf("read-only check overwrote the pending mutation: %+v, %v", current, err)
	}
}

func TestDuplicateOperationCannotOverwriteItsReceipt(t *testing.T) {
	f := newRunFixture(t)
	spec := f.spec("duplicate", "dep_result '{\"version\":\"1.0.0\"}'\n")
	spec.Receipt.ID = strings.Repeat("a", 32)
	first, err := Run(testContext(t), f.client, spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec.Script = "exit 7\n"
	if _, err := Run(testContext(t), f.client, spec, nil); err == nil {
		t.Fatal("duplicate accepted operation replaced its receipt")
	}
	current, err := ReadOperationReceipt(testContext(t), f.client, spec.Home, spec.DepID)
	if err != nil || current == nil || current.ID != first.Receipt.ID || !current.Completed || current.ExitCode != 0 || current.Result.Version != "1.0.0" {
		t.Fatalf("duplicate operation changed the accepted result: %+v, %v", current, err)
	}
}

func TestCancelledClaimCannotExecuteAfterNewOperation(t *testing.T) {
	f := newRunFixture(t)
	ctx := testContext(t)
	effect := filepath.Join(t.TempDir(), "execution")
	old := f.spec("claim-fence", "printf stale > "+shellQuote(effect)+"\n")
	old.Receipt.ID = strings.Repeat("a", 32)
	root := operationRoot(old.Home, old.DepID)
	marker := filepath.Join(root, ".cancelled-"+old.Receipt.ID)
	// The old Server has claimed its ID but has not started Run. The reaper
	// fences that ID under the kernel lock before making room for a new claim.
	fenceScript := "mkdir -p " + shellQuote(root) + "\n: > " + shellQuote(marker) + "\n"
	fenced, err := f.client.ExecWithOptions(ctx, scriptExecCommand, defaultWorkDir, 5, []byte(fenceScript), bridge.ExecOptions{Env: []string{
		"MEMOH_DEP_HOME=" + old.Home, "MEMOH_DEP_ID=" + old.DepID,
	}})
	if err != nil || fenced.ExitCode != 0 {
		t.Fatalf("reaper did not establish its fence: %v", err)
	}
	current := f.spec(old.DepID, "printf new > "+shellQuote(effect)+"\ndep_result '{\"version\":\"2.0.0\"}'\n")
	current.Receipt.ID = strings.Repeat("b", 32)
	installed, err := Run(ctx, f.client, current, nil)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := range 2 {
		stale, err := Run(ctx, f.client, old, nil)
		var exitErr *ExitError
		if !errors.As(err, &exitErr) || exitErr.Code != 76 {
			t.Fatalf("cancelled claim resumed on attempt %d: %v", attempt, err)
		}
		if err := CleanupReceipt(ctx, f.client, stale.Receipt); err != nil {
			t.Fatal(err)
		}
	}
	observed, err := ReadOperationReceipt(ctx, f.client, old.Home, old.DepID)
	if err != nil || observed == nil || observed.ID != installed.Receipt.ID || observed.Result.Version != "2.0.0" {
		t.Fatalf("cancelled claim replaced current publication: %+v, %v", observed, err)
	}
	body, err := os.ReadFile(effect) //nolint:gosec // G304: synthetic output under t.TempDir records whether the recipe ran.
	if err != nil || string(body) != "new" {
		t.Fatalf("cancelled recipe executed: %q, %v", body, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("receipt cleanup removed the permanent cancellation fence: %v", err)
	}
}
