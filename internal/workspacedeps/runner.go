package workspacedeps

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

// Log stream names passed to LogSink.Log.
const (
	StreamStdout = "stdout"
	StreamStderr = "stderr"
)

// LogSink receives script output line by line. Both streams are logs only;
// the runner never parses them for results (WD-EXEC-002).
type LogSink interface {
	Log(stream, line string)
}

// LogFunc adapts a function to LogSink.
type LogFunc func(stream, line string)

// Log implements LogSink.
func (f LogFunc) Log(stream, line string) {
	f(stream, line)
}

// RunSpec describes one script execution.
type RunSpec struct {
	DepID  string
	Action catalog.Action
	// Script is the catalog script body without the prelude.
	Script  string
	Home    string
	ShimDir string
	// Version is exported as MEMOH_DEP_VERSION: the target version, always
	// the Server pin for agent dependencies and empty for "latest".
	Version string
	// CurrentVersion is exported as MEMOH_DEP_CURRENT_VERSION during update
	// and check_update.
	CurrentVersion string
	// Candidate is exported as MEMOH_DEP_CANDIDATE and only used by the
	// version action: the absolute path of the executable to probe.
	Candidate string
	Platform  Platform
	Timeout   time.Duration
	// ExtraEnv entries (NPM_MIRROR=... and friends) are passed through as-is.
	ExtraEnv []string
	// WorkDir defaults to "/".
	WorkDir string
}

// Result is what a successful run produced. Version and Entrypoints are
// decoded from the result file for install, update, and version; Raw keeps
// the file verbatim so callers can decode action-specific shapes such as the
// check_update payload. All fields are empty when the script wrote no result
// file, which is the normal case for remove.
type Result struct {
	ExitCode    int
	Version     string
	Entrypoints map[string]string
	Raw         json.RawMessage
}

// ErrLocked is returned when another run currently holds the dependency's
// workspace lock (prelude exit status 75).
var ErrLocked = errors.New("dependency operation already in progress")

// ExitError reports a script that exited with a non-zero status.
type ExitError struct {
	Code int
	// StderrTail holds the last few kilobytes of stderr for diagnostics.
	StderrTail string
}

func (e *ExitError) Error() string {
	tail := strings.TrimSpace(e.StderrTail)
	if tail == "" {
		return "dependency script exited with status " + strconv.Itoa(e.Code)
	}
	return "dependency script exited with status " + strconv.Itoa(e.Code) + ": " + tail
}

const (
	// defaultRunTimeout applies when RunSpec.Timeout is unset; callers are
	// expected to pass the catalog timeout for the action.
	defaultRunTimeout = time.Duration(catalog.DefaultInstallTimeout) * time.Second
	// lockStaleGrace is added to the script timeout to derive
	// MEMOH_DEP_LOCK_STALE_SECONDS: a lock older than that cannot belong to a
	// run that is still within its own timeout.
	lockStaleGrace  = 5 * time.Minute
	stderrTailLimit = 4096
	cleanupTimeout  = 15 * time.Second
	defaultTmpDir   = "/tmp"
	defaultWorkDir  = "/"
)

// shellLineRef matches the `sh: line N:` (bash) and `sh: N:` (dash, ash)
// prefixes that shells put in front of syntax and runtime errors.
var shellLineRef = regexp.MustCompile(`^((?:\S*/)?sh: (?:line )?)(\d+)(:)`)

// Run executes spec.Script inside the workspace through client. The script is
// fed over stdin behind the prelude (design §5.1, §5.3), its output is
// forwarded to sink line by line, and on success the structured result file
// is read back and deleted. A nil sink discards the logs.
//
// A non-zero exit returns *ExitError, except status 75 which returns
// ErrLocked. Stream failures and context cancellation return the underlying
// error. The result file is removed in every case; the lock directory only
// once the process has exited, because a cancelled stream does not stop the
// script inside the workspace and the lock must keep protecting it until the
// prelude's stale rule (timeout plus five minutes) reclaims it.
func Run(ctx context.Context, client *bridge.Client, spec RunSpec, sink LogSink) (Result, error) {
	if client == nil {
		return Result{}, errors.New("workspacedeps: bridge client is nil")
	}
	if err := spec.validate(); err != nil {
		return Result{}, err
	}
	if sink == nil {
		sink = LogFunc(func(string, string) {})
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = defaultRunTimeout
	}
	workDir := spec.WorkDir
	if workDir == "" {
		workDir = defaultWorkDir
	}

	lock := lockPath(spec.Home, spec.DepID)
	// Version probes are read-only and may target copies that were never
	// installed by us; creating their home would litter the workspace. The
	// prelude still creates the locks directory it needs.
	if spec.Action != catalog.ActionVersion {
		for _, dir := range []string{spec.Home, VersionsDir(spec.Home), spec.ShimDir, path.Dir(lock)} {
			if err := client.Mkdir(ctx, dir); err != nil {
				return Result{}, fmt.Errorf("workspacedeps: create %s: %w", dir, err)
			}
		}
	}

	resultPath, err := newResultPath(spec.Platform.TmpDir, spec.DepID)
	if err != nil {
		return Result{}, err
	}
	// The lock is only removed once the process has demonstrably exited. If
	// the stream breaks first the script may still be running inside the
	// workspace, and if the prelude reported the lock as taken it belongs to
	// someone else; in both cases the directory stays and the stale-lock rule
	// in the prelude reclaims it later.
	removeLock := false
	defer func() { cleanupRun(ctx, client, resultPath, lock, removeLock) }()

	env := buildEnv(spec, resultPath, timeout)
	stream, err := client.ExecStreamWithOptions(ctx, "exec sh -s", workDir, int32(timeout/time.Second), bridge.ExecOptions{Env: env})
	if err != nil {
		return Result{}, fmt.Errorf("workspacedeps: start script for %s: %w", spec.DepID, err)
	}
	defer func() { _ = stream.Close() }()

	if err := stream.SendStdin([]byte(WrapScript(spec.Script))); err != nil && !errors.Is(err, io.EOF) {
		return Result{}, fmt.Errorf("workspacedeps: send script for %s: %w", spec.DepID, err)
	}
	if err := stream.CloseSend(); err != nil && !errors.Is(err, io.EOF) {
		return Result{}, fmt.Errorf("workspacedeps: close script stdin for %s: %w", spec.DepID, err)
	}

	exitCode, err := forwardOutput(stream, sink)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Result{}, fmt.Errorf("workspacedeps: script for %s interrupted: %w", spec.DepID, ctxErr)
		}
		return Result{}, fmt.Errorf("workspacedeps: script stream for %s: %w", spec.DepID, err)
	}
	switch {
	case exitCode.code == exitCodeLocked:
		return Result{ExitCode: exitCode.code}, ErrLocked
	case exitCode.code != 0:
		removeLock = true
		return Result{ExitCode: exitCode.code}, &ExitError{Code: exitCode.code, StderrTail: exitCode.stderrTail}
	}
	removeLock = true

	result, err := readResult(ctx, client, resultPath)
	if err != nil {
		return Result{}, fmt.Errorf("workspacedeps: read result for %s: %w", spec.DepID, err)
	}
	return result, nil
}

func (s RunSpec) validate() error {
	switch {
	case strings.TrimSpace(s.DepID) == "":
		return errors.New("workspacedeps: run spec has no dependency id")
	case strings.TrimSpace(s.Home) == "":
		return errors.New("workspacedeps: run spec has no home directory")
	case strings.TrimSpace(s.ShimDir) == "":
		return errors.New("workspacedeps: run spec has no shim directory")
	case strings.TrimSpace(s.Script) == "":
		return errors.New("workspacedeps: run spec has no script")
	}
	return nil
}

// newResultPath picks a unique result file below the target's temporary
// directory. It lives outside MEMOH_DEP_HOME on purpose: that tree only holds
// data (WD-FS-002) and temporary files must not end up in snapshots.
func newResultPath(tmpDir, depID string) (string, error) {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("workspacedeps: generate result nonce: %w", err)
	}
	if strings.TrimSpace(tmpDir) == "" {
		tmpDir = defaultTmpDir
	}
	return path.Join(tmpDir, "memoh-dep-"+depID+"-"+hex.EncodeToString(nonce[:])+".json"), nil
}

// buildEnv assembles the script environment from design §5.4.
func buildEnv(spec RunSpec, resultPath string, timeout time.Duration) []string {
	staleSeconds := int64((timeout + lockStaleGrace) / time.Second)
	env := []string{
		"MEMOH_DEP_ID=" + spec.DepID,
		"MEMOH_DEP_ACTION=" + string(spec.Action),
		"MEMOH_DEP_HOME=" + spec.Home,
		"MEMOH_DEP_BIN=" + spec.ShimDir,
		"MEMOH_DEP_VERSION=" + spec.Version,
		"MEMOH_DEP_CURRENT_VERSION=" + spec.CurrentVersion,
		"MEMOH_DEP_RESULT=" + resultPath,
		"MEMOH_DEP_CANDIDATE=" + spec.Candidate,
		"MEMOH_DEP_LOCK_STALE_SECONDS=" + strconv.FormatInt(staleSeconds, 10),
		"DEBIAN_FRONTEND=noninteractive",
		"CI=1",
	}
	env = append(env, spec.Platform.env()...)
	return append(env, spec.ExtraEnv...)
}

// exitStatus is what forwardOutput learns from the EXIT message.
type exitStatus struct {
	code       int
	stderrTail string
}

// forwardOutput pumps the exec stream to sink until EXIT. Output arrives in
// arbitrary chunks, so partial lines are carried across messages and only
// complete lines reach the sink; whatever is left is flushed at exit.
func forwardOutput(stream *bridge.ExecStream, sink LogSink) (exitStatus, error) {
	stdout := newLineSplitter(func(line string) { sink.Log(StreamStdout, line) })
	var tail tailBuffer
	stderr := newLineSplitter(func(line string) {
		tail.write(line)
		sink.Log(StreamStderr, rewriteShellLine(line))
	})
	for {
		msg, err := stream.Recv()
		if err != nil {
			return exitStatus{}, err
		}
		switch msg.GetStream() {
		case pb.ExecOutput_STDOUT:
			stdout.write(msg.GetData())
		case pb.ExecOutput_STDERR:
			stderr.write(msg.GetData())
		case pb.ExecOutput_EXIT:
			stdout.flush()
			stderr.flush()
			return exitStatus{code: int(msg.GetExitCode()), stderrTail: tail.String()}, nil
		}
	}
}

// rewriteShellLine subtracts the prelude height from shell error line
// numbers so users see positions relative to the catalog script.
func rewriteShellLine(line string) string {
	match := shellLineRef.FindStringSubmatchIndex(line)
	if match == nil {
		return line
	}
	n, err := strconv.Atoi(line[match[4]:match[5]])
	if err != nil {
		return line
	}
	adjusted := n - preludeLines
	if adjusted < 1 {
		return line
	}
	return line[:match[4]] + strconv.Itoa(adjusted) + line[match[5]:]
}

// lineSplitter emits complete lines from a byte stream.
type lineSplitter struct {
	emit    func(string)
	partial []byte
}

func newLineSplitter(emit func(string)) *lineSplitter {
	return &lineSplitter{emit: emit}
}

func (l *lineSplitter) write(data []byte) {
	l.partial = append(l.partial, data...)
	for {
		idx := bytes.IndexByte(l.partial, '\n')
		if idx < 0 {
			return
		}
		l.emit(strings.TrimSuffix(string(l.partial[:idx]), "\r"))
		l.partial = l.partial[idx+1:]
	}
}

func (l *lineSplitter) flush() {
	if len(l.partial) == 0 {
		return
	}
	l.emit(strings.TrimSuffix(string(l.partial), "\r"))
	l.partial = nil
}

// tailBuffer keeps the last stderrTailLimit bytes of the lines written to it.
type tailBuffer struct {
	buf []byte
}

func (t *tailBuffer) write(line string) {
	t.buf = append(t.buf, line...)
	t.buf = append(t.buf, '\n')
	if len(t.buf) > stderrTailLimit {
		t.buf = t.buf[len(t.buf)-stderrTailLimit:]
	}
}

func (t *tailBuffer) String() string {
	return string(t.buf)
}

// resultFile is the shape shared by install, update, and version results.
type resultFile struct {
	Version     string            `json:"version"`
	Entrypoints map[string]string `json:"entrypoints"`
}

// readResult reads and decodes the result file. A missing or empty file is
// not an error: remove writes none.
func readResult(ctx context.Context, client *bridge.Client, resultPath string) (Result, error) {
	reader, err := client.ReadRaw(ctx, resultPath)
	if err != nil {
		if errors.Is(err, bridge.ErrNotFound) {
			return Result{}, nil
		}
		return Result{}, err
	}
	defer func() { _ = reader.Close() }()
	raw, err := io.ReadAll(reader)
	if err != nil {
		if errors.Is(err, bridge.ErrNotFound) {
			return Result{}, nil
		}
		return Result{}, err
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return Result{}, nil
	}
	var decoded resultFile
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return Result{}, fmt.Errorf("decode %s: %w", resultPath, err)
	}
	return Result{
		Version:     strings.TrimSpace(decoded.Version),
		Entrypoints: decoded.Entrypoints,
		Raw:         json.RawMessage(raw),
	}, nil
}

// cleanupRun removes the result file and, when removeLock is set, the lock
// directory. It runs on a detached context so a cancelled run still cleans
// up, and failures are ignored: leftovers are harmless and reclaimed later.
func cleanupRun(ctx context.Context, client *bridge.Client, resultPath, lock string, removeLock bool) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	command := "rm -f -- " + shellQuote(resultPath)
	if removeLock {
		command += "; rmdir -- " + shellQuote(lock) + " 2>/dev/null"
	}
	command += "; true"
	_, _ = client.ExecWithOptions(cleanupCtx, command, "", int32(cleanupTimeout/time.Second), nil, bridge.ExecOptions{})
}
