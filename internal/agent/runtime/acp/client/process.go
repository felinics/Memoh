package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	acpprofile "github.com/memohai/memoh/internal/agent/runtime/acp/profile"
	"github.com/memohai/memoh/internal/workspace/bridge"
	pb "github.com/memohai/memoh/internal/workspace/bridgepb"
)

const (
	stderrTailLimit      = 8 * 1024
	defaultContainerPath = "/opt/memoh/toolkit/bin:/usr/local/bin:/usr/bin:/bin"
	containerToolkitBin  = "/opt/memoh/toolkit/bin"
	noProjectWorkDirPart = "/.memoh/acp-work/no-project/"
)

// requiresPinnedToolkitAdapter reports whether the agent's profile declares
// database-resumable session state. Durable JSONL resume is audited against
// the pinned toolkit adapters, so a resumable profile must never silently
// execute a same-named PATH binary whose transcript layout was never audited.
// Deriving this from the profile keeps the launcher free of agent-name
// conditionals: a future resumable agent is pinned the moment its profile
// declares session roots.
func requiresPinnedToolkitAdapter(agentID string) bool {
	profile, ok := acpprofile.Lookup(acpprofile.NormalizeAgentID(agentID))
	if !ok {
		return false
	}
	return len(profile.RuntimeStorage.SessionRoots) > 0
}

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
	Backend          WorkspaceBackend
	BotID            string
	AgentID          string
	SetupMode        SetupMode
	Resume           *SessionStateSnapshot
	Env              []string
	CleanEnv         bool
	UnsetEnv         []string
	NoTimeout        bool
	Logger           *slog.Logger
	RuntimeSyncGuard RuntimeSyncGuard
}

type bridgeProcess struct {
	stream       *bridge.ExecStream
	stdin        *io.PipeWriter
	stdout       *io.PipeReader
	tail         *stderrTail
	done         chan struct{}
	lifecycleCtx context.Context
	env          []string
	toolEnv      []string
	unsetEnv     []string
	lease        *runtimeLease
	logger       *slog.Logger

	stateMu      sync.Mutex
	activated    bool
	closeOnce    sync.Once
	finalizeOnce sync.Once
	finalizeDone chan struct{}
	finalizeErr  error
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

	lease, err := prepareRuntimeLease(ctx, client, opts)
	if err != nil {
		return nil, err
	}
	if opts.Resume != nil {
		// Materialize the database checkpoint before the adapter (and any
		// child app-server it launches) can scan its process-local home.
		if err := lease.restoreSessionState(ctx, opts.Resume); err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = lease.finalize(cleanupCtx, false)
			return nil, fmt.Errorf("restore ACP session state: %w", err)
		}
	}
	env := lease.agentEnv
	runtimeOpts := opts
	runtimeOpts.UnsetEnv = lease.unsetEnv

	resolvedCommand, err := resolveCommand(ctx, client, command, workDir, env, runtimeOpts)
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = lease.finalize(cleanupCtx, false)
		return nil, err
	}

	shellCommand := buildShellCommand(resolvedCommand, args)
	execStream, err := client.ExecStreamWithOptions(ctx, shellCommand, workDir, timeoutSeconds, bridge.ExecOptions{
		Env:      env,
		CleanEnv: runtimeOpts.CleanEnv,
		UnsetEnv: runtimeOpts.UnsetEnv,
	})
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = lease.finalize(cleanupCtx, false)
		return nil, err
	}

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	proc := &bridgeProcess{
		stream:       execStream,
		stdin:        stdinW,
		stdout:       stdoutR,
		tail:         &stderrTail{},
		done:         make(chan struct{}),
		lifecycleCtx: ctx,
		env:          append([]string(nil), env...),
		toolEnv:      append([]string(nil), lease.toolEnv...),
		unsetEnv:     append([]string(nil), lease.unsetEnv...),
		lease:        lease,
		logger:       opts.Logger,
		finalizeDone: make(chan struct{}),
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
	go func(parent context.Context) {
		<-proc.done
		proc.finalizeAfterExit(parent)
	}(ctx)

	return proc, nil
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
		return "", commandNotAvailableError(command, lastResult, requiresPinnedToolkitAdapter(opts.AgentID))
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
	return "", commandNotAvailableError(command, lastResult, requiresPinnedToolkitAdapter(opts.AgentID))
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

	// Durable JSONL resume is audited against the pinned adapters shipped in
	// the Memoh toolkit. A same-named binary earlier on PATH may use a different
	// transcript layout or omit the Claude flush/receipt contract, so built-in
	// resumable profiles must never silently execute it.
	if requiresPinnedToolkitAdapter(opts.AgentID) {
		toolkitCommand := containerToolkitBin + "/" + command
		toolkitResult, err := checkCommand(ctx, client, "test -x "+escapeShellArg(toolkitCommand), workDir, env, opts)
		if err != nil {
			return "", nil, fmt.Errorf("check pinned ACP command %q: %w", toolkitCommand, err)
		}
		if toolkitResult.ExitCode == 0 {
			return toolkitCommand, toolkitResult, nil
		}
		return "", toolkitResult, nil
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

func commandNotAvailableError(command string, result *bridge.ExecResult, pinned bool) error {
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
	if pinned {
		// Resumable agents deliberately never fall back to PATH, so pointing
		// at PATH here would send operators of custom images down the wrong
		// road: the only fix is shipping the audited toolkit adapter payload.
		return fmt.Errorf("ACP command %q requires the pinned adapter at %s/%s, which is missing from this workspace image%s. Resumable agents never use a PATH-installed copy; rebuild the workspace image with the Memoh toolkit adapter payload", command, containerToolkitBin, command, detail)
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
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
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
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-p.done:
	case <-timer.C:
		return errors.New("ACP process did not exit before the cleanup deadline")
	}
	<-p.finalizeDone
	return p.finalizeErr
}

// Activate marks a fully initialized ACP process as eligible to synchronize
// durable artifacts. Startup failures call Close before activation and only
// remove their process-local directory.
func (p *bridgeProcess) Activate() {
	if p == nil {
		return
	}
	p.stateMu.Lock()
	if !p.activated {
		p.activated = true
		go p.syncLoop(runtimeSyncInterval)
	}
	p.stateMu.Unlock()
}

func (p *bridgeProcess) syncLoop(interval time.Duration) {
	if p == nil || p.lease == nil || p.lifecycleCtx == nil {
		return
	}
	if interval <= 0 {
		interval = runtimeSyncInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-p.lifecycleCtx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(p.lifecycleCtx, 10*time.Second)
			err := p.lease.syncLiveState(ctx)
			cancel()
			if errors.Is(err, ErrRuntimeSyncGenerationStale) {
				if p.logger != nil {
					p.logger.Info("stopping stale ACP runtime synchronization",
						slog.String("agent_id", p.lease.agentID),
						slog.String("bot_id", p.lease.botID),
						slog.Any("error", err))
				}
				return
			}
			if err != nil && p.logger != nil {
				p.logger.Warn("failed to refresh ACP runtime lease",
					slog.String("agent_id", p.lease.agentID),
					slog.String("bot_id", p.lease.botID),
					slog.Any("error", err))
			}
		}
	}
}

func (p *bridgeProcess) SyncPromptState(ctx context.Context) error {
	if p == nil || p.lease == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return p.lease.syncLiveState(ctx)
}

func (p *bridgeProcess) finalizeAfterExit(parent context.Context) {
	if p == nil {
		return
	}
	p.finalizeOnce.Do(func() {
		defer close(p.finalizeDone)
		p.stateMu.Lock()
		commit := p.activated
		p.stateMu.Unlock()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 15*time.Second)
		defer cancel()
		p.finalizeErr = p.lease.finalize(ctx, commit)
		if p.finalizeErr != nil && p.logger != nil {
			p.logger.Warn("failed to finalize ACP runtime state",
				slog.String("agent_id", p.lease.agentID),
				slog.String("bot_id", p.lease.botID),
				slog.Any("error", p.finalizeErr))
		}
	})
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
