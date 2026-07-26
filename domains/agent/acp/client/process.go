package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/memohai/memoh/domains/runtime/bridge/bridgepb"
	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
)

const (
	stderrTailLimit      = 8 * 1024
	defaultContainerPath = "/opt/memoh/toolkit/bin:/usr/local/bin:/usr/bin:/bin"
	containerToolkitBin  = "/opt/memoh/toolkit/bin"
	noProjectWorkDirPart = "/.memoh/acp-work/no-project/"
)

var (
	commandResolveWindow = 5 * time.Second
	commandResolveDelay  = 200 * time.Millisecond
)

type WorkspaceBackend string

const (
	WorkspaceBackendContainer WorkspaceBackend = "container"
)

type SetupMode string

const (
	SetupModeAPIKey SetupMode = "api_key"
	SetupModeOAuth  SetupMode = "oauth"
	SetupModeSelf   SetupMode = "self"
)

type processOptions struct {
	Backend   WorkspaceBackend
	AgentID   string
	SetupMode SetupMode
	Env       []string
	CleanEnv  bool
	UnsetEnv  []string
	// HermesHome is the bot-scoped HERMES_HOME resolved by SessionPool and
	// reused by Runner so config writes and process startup share one path.
	HermesHome string
	NoTimeout  bool
}

type bridgeProcess struct {
	stream    *bridge.ExecStream
	stdin     *io.PipeWriter
	stdout    *io.PipeReader
	tail      *stderrTail
	stdinDone chan struct{}
	recvDone  chan struct{}
	env       []string
	cleanup   func(context.Context) error

	closeOnce sync.Once
	closeErr  error
	cleanMu   sync.Mutex
	cleaned   bool
}

func startBridgeProcess(ctx context.Context, client *bridge.Client, command string, args []string, workDir string, timeout time.Duration, opts processOptions) (*bridgeProcess, error) {
	if client == nil {
		return nil, errors.New("workspace bridge client is required")
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, errors.New("ACP command is required")
	}
	if strings.Contains(filepath.ToSlash(workDir), noProjectWorkDirPart) {
		if err := client.Mkdir(ctx, workDir); err != nil {
			return nil, fmt.Errorf("prepare ACP cwd: %w", err)
		}
	}
	timeoutSeconds := int32(timeout.Seconds())
	if opts.NoTimeout {
		timeoutSeconds = -1
	} else if timeoutSeconds <= 0 {
		timeoutSeconds = int32(DefaultRunTimeout.Seconds())
	}

	env, cleanup, err := prepareProcessEnv(ctx, client, workDir, opts)
	if err != nil {
		return nil, err
	}

	resolvedCommand, err := resolveCommand(ctx, client, command, workDir, env, opts)
	if err != nil {
		if cleanup != nil {
			_ = cleanup(context.Background())
		}
		return nil, err
	}

	shellCommand := buildShellCommand(resolvedCommand, args)
	execStream, err := client.ExecStreamWithOptions(ctx, shellCommand, workDir, timeoutSeconds, bridge.ExecOptions{
		Env:      env,
		CleanEnv: opts.CleanEnv,
		UnsetEnv: opts.UnsetEnv,
	})
	if err != nil {
		if cleanup != nil {
			_ = cleanup(context.Background())
		}
		return nil, err
	}

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	proc := &bridgeProcess{
		stream:    execStream,
		stdin:     stdinW,
		stdout:    stdoutR,
		tail:      &stderrTail{},
		stdinDone: make(chan struct{}),
		recvDone:  make(chan struct{}),
		env:       append([]string(nil), env...),
		cleanup:   cleanup,
	}

	go func() {
		defer close(proc.stdinDone)
		defer func() { _ = stdinR.Close() }()
		buf := make([]byte, 32*1024)
		for {
			n, readErr := stdinR.Read(buf)
			if n > 0 {
				if sendErr := execStream.SendStdin(buf[:n]); sendErr != nil {
					_ = stdoutW.CloseWithError(sendErr)
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}()

	go func() {
		defer close(proc.recvDone)
		for {
			output, recvErr := execStream.Recv()
			if recvErr != nil {
				if !errors.Is(recvErr, io.EOF) {
					_ = stdoutW.CloseWithError(recvErr)
				} else {
					_ = stdoutW.Close()
				}
				return
			}
			switch output.GetStream() {
			case bridgepb.ExecOutput_STDOUT:
				if _, err := stdoutW.Write(output.GetData()); err != nil {
					_ = stdoutW.CloseWithError(err)
					return
				}
			case bridgepb.ExecOutput_STDERR:
				proc.tail.append(output.GetData())
			case bridgepb.ExecOutput_EXIT:
				_ = stdoutW.Close()
				return
			}
		}
	}()

	return proc, nil
}

func prepareProcessEnv(ctx context.Context, client *bridge.Client, workDir string, opts processOptions) ([]string, func(context.Context) error, error) {
	mode := normalizeSetupMode(opts.SetupMode)

	env := withoutEnvKeys(opts.Env, "HOME", "PATH", "CODEX_HOME")
	switch mode {
	case SetupModeAPIKey, SetupModeOAuth:
		if isHermesAgent(opts.AgentID) {
			hermesHome := strings.TrimSpace(opts.HermesHome)
			if hermesHome == "" {
				return nil, nil, errors.New("hermes managed setup in an isolated workspace requires HERMES_HOME isolation")
			}
			env = withoutEnvKeys(withoutBlockedEnvNames(opts.Env, HermesManagedUnsetEnvKeys()), "HOME", "PATH", "CODEX_HOME")
			env = append(env, "HOME="+dataMountPath, "PATH="+defaultContainerPath, "HERMES_HOME="+hermesHome)
			if err := client.Mkdir(ctx, hermesHome); err != nil {
				return nil, nil, fmt.Errorf("prepare Hermes HOME: %w", err)
			}
			return env, nil, nil
		}
		homeDir := dataMountPath
		tempHomeDir := "/tmp/memoh-acp/" + uuid.NewString()
		if !isCodexAgent(opts.AgentID) {
			homeDir = tempHomeDir
		}
		env = append(env, "HOME="+homeDir, "PATH="+defaultContainerPath)

		if err := client.Mkdir(ctx, homeDir); err != nil {
			return nil, nil, fmt.Errorf("prepare ACP HOME: %w", err)
		}
		// Managed sessions isolate via a fresh HOME and start with the managed
		// ask rule in effect.
		if isClaudeCodeAgent(opts.AgentID) {
			if err := WriteClaudeManagedSettings(ctx, client, homeDir); err != nil {
				return nil, nil, fmt.Errorf("prepare Claude managed settings: %w", err)
			}
		}

		// Startup failures may supply an independent context, while lifecycle
		// shutdown passes its deadline through to the temporary HOME cleanup.
		cleanup := func(parent context.Context) error {
			if parent == nil {
				parent = context.Background()
			}
			cleanupCtx, cancel := context.WithTimeout(parent, 5*time.Second)
			defer cancel()
			if homeDir == tempHomeDir {
				_, err := client.ExecWithOptions(cleanupCtx, "rm -rf "+escapeShellArg(homeDir), workDir, 5, nil, bridge.ExecOptions{
					Env:      env,
					CleanEnv: opts.CleanEnv,
					UnsetEnv: opts.UnsetEnv,
				})
				return err
			}
			return nil
		}
		return env, cleanup, nil
	case SetupModeSelf:
		env = withoutEnvKeys(opts.Env, "HOME", "PATH", "CODEX_HOME")
		env = append(env, "PATH="+defaultContainerPath)
		if isCodexAgent(opts.AgentID) {
			env = append(env, "HOME="+dataMountPath)
		}
		if isHermesAgent(opts.AgentID) {
			home := dataMountPath + "/.hermes"
			env = append(env, "HOME="+dataMountPath, "HERMES_HOME="+home)
			if err := client.Mkdir(ctx, home); err != nil {
				return nil, nil, fmt.Errorf("prepare Hermes self HOME: %w", err)
			}
		}
		return env, nil, nil
	default:
		return nil, nil, fmt.Errorf("unsupported ACP setup mode %q", mode)
	}
}

func normalizeSetupMode(mode SetupMode) SetupMode {
	switch SetupMode(strings.ToLower(strings.TrimSpace(string(mode)))) {
	case SetupModeOAuth:
		return SetupModeOAuth
	case SetupModeSelf:
		return SetupModeSelf
	default:
		return SetupModeAPIKey
	}
}

func resolveCommand(ctx context.Context, client *bridge.Client, command, workDir string, env []string, opts processOptions) (string, error) {
	command = strings.TrimSpace(command)
	resolved, lastResult, err := resolveCommandOnce(ctx, client, command, workDir, env, opts)
	if err != nil || resolved != "" {
		if resolved != "" || err != nil {
			return resolved, err
		}
		return "", commandNotAvailableError(command, lastResult, opts.Backend)
	}

	deadline := time.Now().Add(commandResolveWindow)
	for time.Now().Before(deadline) {
		timer := time.NewTimer(commandResolveDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}

		resolved, result, err := resolveCommandOnce(ctx, client, command, workDir, env, opts)
		if result != nil {
			lastResult = result
		}
		if err != nil {
			return "", err
		}
		if resolved != "" {
			return resolved, nil
		}
	}
	return "", commandNotAvailableError(command, lastResult, opts.Backend)
}

func resolveCommandOnce(ctx context.Context, client *bridge.Client, command, workDir string, env []string, opts processOptions) (string, *bridge.ExecResult, error) {
	command = strings.TrimSpace(command)
	if !isPlainCommand(command) {
		return command, nil, nil
	}

	if strings.Contains(command, "/") {
		result, err := checkCommand(ctx, client, "test -x "+escapeShellArg(command), workDir, env, opts)
		if err != nil {
			return "", nil, fmt.Errorf("check ACP command %q: %w", command, err)
		}
		if result.ExitCode == 0 {
			return command, result, nil
		}
		return "", result, nil
	}

	result, err := checkCommand(ctx, client, "command -v "+escapeShellArg(command)+" >/dev/null 2>&1", workDir, env, opts)
	if err != nil {
		return "", nil, fmt.Errorf("check ACP command %q: %w", command, err)
	}
	if result.ExitCode == 0 {
		return command, result, nil
	}
	toolkitCommand := containerToolkitBin + "/" + command
	toolkitResult, err := checkCommand(ctx, client, "test -x "+escapeShellArg(toolkitCommand), workDir, env, opts)
	if err != nil {
		return "", nil, fmt.Errorf("check ACP command %q: %w", command, err)
	}
	if toolkitResult.ExitCode == 0 {
		return toolkitCommand, toolkitResult, nil
	}

	return "", toolkitResult, nil
}

func checkCommand(ctx context.Context, client *bridge.Client, check, workDir string, env []string, opts processOptions) (*bridge.ExecResult, error) {
	return client.ExecWithOptions(ctx, check, workDir, 10, nil, bridge.ExecOptions{
		Env:      env,
		CleanEnv: opts.CleanEnv,
		UnsetEnv: opts.UnsetEnv,
	})
}

func commandNotAvailableError(command string, result *bridge.ExecResult, _ WorkspaceBackend) error {
	detail := ""
	if result != nil {
		detail = strings.TrimSpace(result.Stderr)
		if detail == "" {
			detail = strings.TrimSpace(result.Stdout)
		}
	}
	if detail != "" {
		detail = ": " + detail
	}
	return fmt.Errorf("ACP command %q is not available in the workspace PATH or %s%s. Install it in the workspace or rebuild the Memoh workspace runtime with %s available", command, containerToolkitBin, detail, containerToolkitBin)
}

func isPlainCommand(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	return !strings.ContainsAny(command, " \t\n'\"\\$&;|<>*?()[]{}!`")
}

func HermesManagedUnsetEnvKeys() []string {
	return []string{
		"HERMES_HOME",
		"HERMES_*",
		hermesManagedCustomProviderEnvKey,
		"OPENAI_API_KEY",
		"OPENAI_BASE_URL",
		"OPENAI_API_BASE",
		"OPENROUTER_API_KEY",
		"OPENROUTER_BASE_URL",
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_API_BASE",
		"GOOGLE_API_KEY",
		"GOOGLE_BASE_URL",
		"GOOGLE_API_BASE",
		"GEMINI_API_KEY",
		"GEMINI_BASE_URL",
		"GEMINI_API_BASE",
	}
}

func (p *bridgeProcess) Read(b []byte) (int, error) {
	return p.stdout.Read(b)
}

func (p *bridgeProcess) Write(b []byte) (int, error) {
	return p.stdin.Write(b)
}

func (p *bridgeProcess) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return p.CloseContext(ctx)
}

// CloseContext stops the bridge stream and waits for both owned I/O pumps.
// A timed-out call can be retried with a fresh context; shutdown initiation is
// idempotent while the pump completion channels remain available for joining.
func (p *bridgeProcess) CloseContext(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p.closeOnce.Do(func() {
		var errs []error
		if p.stdin != nil {
			if err := p.stdin.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close ACP stdin: %w", err))
			}
		}
		if p.stdout != nil {
			if err := p.stdout.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close ACP stdout: %w", err))
			}
		}
		if p.stream != nil {
			if err := p.stream.Close(); err != nil && !errors.Is(err, context.Canceled) {
				errs = append(errs, fmt.Errorf("close ACP exec stream: %w", err))
			}
		}
		p.closeErr = errors.Join(errs...)
	})
	if err := waitForProcessPumps(ctx, p.stdinDone, p.recvDone); err != nil {
		return errors.Join(p.closeErr, err)
	}
	p.cleanMu.Lock()
	var cleanupErr error
	if !p.cleaned {
		if p.cleanup != nil {
			cleanupErr = p.cleanup(ctx)
		}
		if cleanupErr == nil {
			p.cleaned = true
		}
	}
	p.cleanMu.Unlock()
	if cleanupErr != nil {
		cleanupErr = fmt.Errorf("clean up ACP process environment: %w", cleanupErr)
	}
	return errors.Join(p.closeErr, cleanupErr)
}

func waitForProcessPumps(ctx context.Context, pumps ...<-chan struct{}) error {
	for _, done := range pumps {
		if done == nil {
			continue
		}
		select {
		case <-done:
		case <-ctx.Done():
			return fmt.Errorf("wait for ACP process I/O pumps: %w", ctx.Err())
		}
	}
	return nil
}

func withoutEnvKeys(env []string, keys ...string) []string {
	if len(env) == 0 {
		return nil
	}
	blocked := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		blocked[key] = struct{}{}
	}
	out := make([]string, 0, len(env))
	for _, item := range env {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			out = append(out, item)
			continue
		}
		if _, skip := blocked[key]; skip {
			continue
		}
		out = append(out, item)
	}
	return out
}

func withoutBlockedEnvNames(env []string, blocked []string) []string {
	out := make([]string, 0, len(env))
	for _, item := range env {
		name, _, ok := strings.Cut(item, "=")
		if !ok {
			name = item
		}
		if envNameBlocked(name, blocked) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func envNameBlocked(name string, keys []string) bool {
	// Keep wildcard semantics aligned with bridge/server.filterUnsetEnv. The client
	// side filters ACP terminal p.Env before sending it; the bridge side filters
	// inherited os.Environ before launching the process.
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if strings.HasSuffix(key, "*") {
			if prefix := strings.TrimSuffix(key, "*"); prefix != "" && strings.HasPrefix(name, prefix) {
				return true
			}
			continue
		}
		if name == key {
			return true
		}
	}
	return false
}

func (p *bridgeProcess) errorWithStderr(err error) error {
	if err == nil {
		err = io.EOF
	}
	if strings.TrimSpace(p.tail.String()) == "" {
		select {
		case <-p.recvDone:
		case <-time.After(250 * time.Millisecond):
		}
	}
	stderr := strings.TrimSpace(p.tail.String())
	if stderr == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, stderr)
}

type stderrTail struct {
	mu  sync.Mutex
	buf string
}

func (t *stderrTail) append(data []byte) {
	if t == nil || len(data) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf += string(data)
	if len(t.buf) > stderrTailLimit {
		t.buf = t.buf[len(t.buf)-stderrTailLimit:]
	}
}

func (t *stderrTail) String() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf
}

func escapeShellArg(value string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\n'\"\\$&;|<>*?()[]{}!`") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func buildShellCommand(command string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, escapeShellArg(strings.TrimSpace(command)))
	for _, arg := range args {
		parts = append(parts, escapeShellArg(arg))
	}
	return strings.Join(parts, " ")
}
