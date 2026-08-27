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

	"github.com/memohai/memoh/internal/runtimeauth"
	"github.com/memohai/memoh/internal/workspace/bridge"
	pb "github.com/memohai/memoh/internal/workspace/bridgepb"
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
	RuntimeID string
	AgentID   string
	SetupMode SetupMode
	Env       []string
	CleanEnv  bool
	UnsetEnv  []string
	// HermesHome is the bot-scoped HERMES_HOME resolved by SessionPool and
	// reused by Runner so config writes and process startup share one path.
	HermesHome        string
	AuthRoot          string
	CodexHome         string
	ClaudeHome        string
	BeforeAuthCleanup func(context.Context)
	NoTimeout         bool
}

type bridgeProcess struct {
	stream      *bridge.ExecStream
	stdin       *io.PipeWriter
	stdout      *io.PipeReader
	tail        *stderrTail
	done        chan struct{}
	env         []string
	cleanup     func()
	once        sync.Once
	cleanupOnce sync.Once
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
			cleanup()
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
			cleanup()
		}
		return nil, err
	}

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	proc := &bridgeProcess{
		stream:  execStream,
		stdin:   stdinW,
		stdout:  stdoutR,
		tail:    &stderrTail{},
		done:    make(chan struct{}),
		env:     append([]string(nil), env...),
		cleanup: cleanup,
	}

	go func() {
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
		defer close(proc.done)
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
			case pb.ExecOutput_STDOUT:
				if _, err := stdoutW.Write(output.GetData()); err != nil {
					_ = stdoutW.CloseWithError(err)
					return
				}
			case pb.ExecOutput_STDERR:
				proc.tail.append(output.GetData())
			case pb.ExecOutput_EXIT:
				_ = stdoutW.Close()
				return
			}
		}
	}()

	return proc, nil
}

func prepareProcessEnv(ctx context.Context, client *bridge.Client, workDir string, opts processOptions) ([]string, func(), error) {
	mode := normalizeSetupMode(opts.SetupMode)

	env := withoutEnvKeys(opts.Env, "HOME", "PATH", "CODEX_HOME")
	switch mode {
	case SetupModeAPIKey, SetupModeOAuth:
		authRoot := strings.TrimSpace(opts.AuthRoot)
		isolatedAuth := runtimeauth.IsRuntimeRoot(authRoot)
		if strings.TrimSpace(opts.RuntimeID) != "" && !isolatedAuth {
			return nil, nil, errors.New("managed Agent runtime requires an isolated auth root")
		}
		if isolatedAuth {
			if err := preparePrivateDir(ctx, client, authRoot); err != nil {
				return nil, nil, fmt.Errorf("prepare runtime auth root: %w", err)
			}
		}
		if isHermesAgent(opts.AgentID) {
			hermesHome := strings.TrimSpace(opts.HermesHome)
			if hermesHome == "" {
				return nil, nil, errors.New("hermes managed setup in an isolated workspace requires HERMES_HOME isolation")
			}
			env = withoutEnvKeys(withoutBlockedEnvNames(opts.Env, HermesManagedUnsetEnvKeys()), "HOME", "PATH", "CODEX_HOME")
			env = append(env, "HOME="+dataMountPath, "PATH="+defaultContainerPath, "HERMES_HOME="+hermesHome)
			if isolatedAuth {
				if err := preparePrivateDir(ctx, client, hermesHome); err != nil {
					return nil, nil, fmt.Errorf("prepare Hermes HOME: %w", err)
				}
			} else if err := client.Mkdir(ctx, hermesHome); err != nil {
				return nil, nil, fmt.Errorf("prepare Hermes HOME: %w", err)
			}
			if isolatedAuth {
				return env, runtimeAuthCleanup(client, authRoot, opts.BeforeAuthCleanup), nil
			}
			return env, nil, nil
		}
		homeDir := strings.TrimSpace(opts.ClaudeHome)
		legacyTempHome := ""
		if isCodexAgent(opts.AgentID) {
			homeDir = dataMountPath
			codexHome := strings.TrimSpace(opts.CodexHome)
			if codexHome != "" {
				env = append(env, "CODEX_HOME="+codexHome)
			}
		} else if homeDir == "" {
			legacyTempHome = "/tmp/memoh-auth/rt_" + uuid.NewString()
			homeDir = legacyTempHome
		}
		if homeDir == "" {
			return nil, nil, errors.New("managed Agent runtime requires HOME isolation")
		}
		env = append(env, "HOME="+homeDir, "PATH="+defaultContainerPath)

		if isolatedAuth && !isCodexAgent(opts.AgentID) {
			if err := preparePrivateDir(ctx, client, homeDir); err != nil {
				return nil, nil, fmt.Errorf("prepare Agent HOME: %w", err)
			}
		} else if err := client.Mkdir(ctx, homeDir); err != nil {
			return nil, nil, fmt.Errorf("prepare ACP HOME: %w", err)
		}
		// Managed sessions isolate via a fresh HOME and start with the managed
		// ask rule in effect.
		if isClaudeCodeAgent(opts.AgentID) {
			if err := WriteClaudeManagedSettings(ctx, client, homeDir); err != nil {
				return nil, nil, fmt.Errorf("prepare Claude managed settings: %w", err)
			}
		}

		// Cleanup intentionally derives a fresh background ctx with its own
		// short deadline: the parent ctx is usually already cancelled by the
		// time we tear down the ACP HOME, but we still want to issue rm -rf.
		if isolatedAuth {
			return env, runtimeAuthCleanup(client, authRoot, opts.BeforeAuthCleanup), nil
		}
		cleanup := func() { //nolint:contextcheck // compatibility cleanup outlives its request.
			if legacyTempHome == "" {
				return
			}
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = client.ExecWithOptions(cleanupCtx, "rm -rf "+escapeShellArg(legacyTempHome), workDir, 5, nil, bridge.ExecOptions{Env: env, CleanEnv: opts.CleanEnv, UnsetEnv: opts.UnsetEnv})
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

//nolint:contextcheck // cleanup intentionally creates a detached bounded context after runtime shutdown.
func runtimeAuthCleanup(client *bridge.Client, root string, before func(context.Context)) func() {
	return func() { //nolint:contextcheck // cleanup must outlive the request that owned the runtime.
		if client == nil || !runtimeauth.IsRuntimeRoot(root) {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if before != nil {
			before(ctx)
		}
		_ = client.DeleteFile(ctx, root, true)
	}
}

func preparePrivateDir(ctx context.Context, client *bridge.Client, dir string) error {
	if client == nil || strings.TrimSpace(dir) == "" {
		return errors.New("private runtime directory is required")
	}
	if err := client.Mkdir(ctx, dir); err != nil {
		return err
	}
	result, err := client.ExecWithOptions(ctx, "chmod 700 "+escapeShellArg(dir), dataMountPath, 5, nil, bridge.ExecOptions{})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("chmod private runtime directory: exit %d", result.ExitCode)
	}
	return nil
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
	p.once.Do(func() {
		if p.stdin != nil {
			_ = p.stdin.Close()
		}
		if p.stdout != nil {
			_ = p.stdout.Close()
		}
		if p.stream != nil {
			_ = p.stream.Close()
		}
	})
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
	}
	p.cleanupOnce.Do(func() {
		if p.cleanup != nil {
			p.cleanup()
		}
	})
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
	// Keep wildcard semantics aligned with bridgesvc.filterUnsetEnv. The client
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
		case <-p.done:
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
