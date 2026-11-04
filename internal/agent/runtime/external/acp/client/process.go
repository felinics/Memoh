package client

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/external/agentproc"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/workspace/bridge"
	"github.com/felinics/memoh/internal/workspace/shellenv"
)

const (
	// defaultContainerPath is the PATH ACP agents start from; the user's shell
	// configuration extends it (see resolveContainerPath). The managed
	// dependency shim directory comes first so a managed overlay
	// of an image runtime (node, python, uv) wins over the toolkit copy.
	defaultContainerPath = dataMountPath + "/.memoh/deps/bin:" + containerToolkitBin + ":/usr/local/bin:/usr/bin:/bin"
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

// ProcessOptions describes one ACP agent process: who it runs for and the
// environment it starts from.
type ProcessOptions struct {
	BotID    string
	AgentID  string
	Env      []string
	UnsetEnv []string
	Logger   *slog.Logger
	// CleanEnv starts the agent on Env alone: an agent whose whole
	// environment Memoh composes and that keeps its state where Env points
	// it. Without it the agent inherits the workspace environment minus
	// UnsetEnv, with HOME at the bot's data and the user's shell PATH.
	CleanEnv bool
}

// Process is one user-supplied ACP agent running in a bot's workspace.
type Process struct {
	*agentproc.Child
	// ToolEnv and UnsetEnv are the environment the agent's terminals run with.
	ToolEnv  []string
	UnsetEnv []string
}

// npmCacheDir keeps the npm cache of agents run through npx out of the bot's
// data, where HOME would put it.
const npmCacheDir = "/tmp/memoh-acp-cache/npm"

// processEnv is the environment an agent and its terminals run with, and the
// inherited names it replaces. The PATH probe runs the user's shell
// configuration, which keys its PATH additions off HOME, so an agent
// installed from the workspace terminal is found; a failed probe falls back
// to the default PATH.
func processEnv(ctx context.Context, client *bridge.Client, opts ProcessOptions) ([]string, []string) {
	owned := []string{"HOME", "PATH", "NPM_CONFIG_CACHE"}
	env := withoutEnvKeys(withoutBlockedEnvNames(opts.Env, opts.UnsetEnv), owned...)
	env = append(env, "HOME="+dataMountPath, "NPM_CONFIG_CACHE="+npmCacheDir)
	unset := mergeEnvNames(opts.UnsetEnv, owned)
	containerPath, err := shellenv.ResolvePath(ctx, client, defaultContainerPath, env, unset)
	if err != nil {
		if opts.Logger != nil {
			opts.Logger.WarnContext(ctx, "workspace shell PATH probe failed; ACP agent runs with the default PATH",
				slog.String("agent_id", opts.AgentID), slog.String("bot_id", opts.BotID), slog.Any("error", err))
		}
		containerPath = defaultContainerPath
	}
	return append(env, "PATH="+containerPath), unset
}

func mergeEnvNames(existing, additional []string) []string {
	out := make([]string, 0, len(existing)+len(additional))
	for _, value := range slices.Concat(existing, additional) {
		if value = strings.TrimSpace(value); value != "" && !slices.Contains(out, value) {
			out = append(out, value)
		}
	}
	return out
}

// StartProcess resolves command on the workspace PATH and launches it in
// workDir.
func StartProcess(ctx context.Context, client *bridge.Client, command string, args []string, workDir string, opts ProcessOptions) (*Process, error) {
	if client == nil {
		return nil, errs.New("workspace bridge client is required")
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, errs.New("ACP command is required")
	}
	if strings.Contains(filepath.ToSlash(workDir), noProjectWorkDirPart) {
		if err := client.Mkdir(ctx, workDir); err != nil {
			return nil, errs.WrapDependency(err, "prepare ACP cwd")
		}
	}
	env, unset := opts.Env, opts.UnsetEnv
	if !opts.CleanEnv {
		env, unset = processEnv(ctx, client, opts)
	}
	resolveOpts := opts
	resolveOpts.UnsetEnv = unset
	resolved, err := resolveCommand(ctx, client, command, workDir, env, resolveOpts)
	if err != nil {
		return nil, err
	}
	// The process outlives the request that happened to start it.
	child, err := agentproc.SpawnWith(context.WithoutCancel(ctx), client, buildShellCommand(resolved, args), workDir, bridge.ExecOptions{
		Env:      env,
		UnsetEnv: unset,
		CleanEnv: opts.CleanEnv,
	})
	if err != nil {
		return nil, errs.WrapDependency(err, "start "+buildShellCommand(command, args))
	}
	return &Process{Child: child, ToolEnv: env, UnsetEnv: unset}, nil
}

// handshakeTimeout bounds the ACP initialize exchange on a fresh process.
const handshakeTimeout = 60 * time.Second

// Launch starts an agent process and completes the ACP handshake on it,
// offering caps. A failed handshake carries the agent's stderr: an agent that
// could not start usually said why there.
func Launch(ctx context.Context, client *bridge.Client, command string, args []string, workDir string, opts ProcessOptions, caps ClientCapabilities) (*Process, *Conn, error) {
	proc, err := StartProcess(ctx, client, command, args, workDir, opts)
	if err != nil {
		return nil, nil, err
	}
	handshakeCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	conn, err := ConnectWith(handshakeCtx, proc, opts.Logger, caps)
	if err != nil {
		err = withStderr(err, proc.StderrTail)
		_ = proc.Close()
		return nil, nil, err
	}
	return proc, conn, nil
}

func resolveCommand(ctx context.Context, client *bridge.Client, command, workDir string, env []string, opts ProcessOptions) (string, error) {
	command = strings.TrimSpace(command)
	resolved, lastResult, err := resolveCommandOnce(ctx, client, command, workDir, env, opts)
	if err != nil || resolved != "" {
		return resolved, err
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
	return "", commandNotAvailableError(command, lastResult)
}

func resolveCommandOnce(ctx context.Context, client *bridge.Client, command, workDir string, env []string, opts ProcessOptions) (string, *bridge.ExecResult, error) {
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

func checkCommand(ctx context.Context, client *bridge.Client, check, workDir string, env []string, opts ProcessOptions) (*bridge.ExecResult, error) {
	result, err := client.ExecWithOptions(ctx, check, workDir, 10, nil, bridge.ExecOptions{
		Env:      env,
		UnsetEnv: opts.UnsetEnv,
	})
	if err != nil {
		return nil, errs.WrapDependency(err, "")
	}
	return result, nil
}

// CommandNotFoundError reports a launch command that resolves neither on the
// workspace PATH nor in the toolkit. It is its own type because retrying cannot
// fix it: public surfaces tell the user which command to install or correct.
type CommandNotFoundError struct {
	Command string
	detail  string
}

func (e *CommandNotFoundError) Error() string {
	return fmt.Sprintf("ACP command %q is not available in the workspace PATH or %s%s. Install it in the workspace or rebuild the Memoh workspace runtime with %s available", e.Command, containerToolkitBin, e.detail, containerToolkitBin)
}

func commandNotAvailableError(command string, result *bridge.ExecResult) error {
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
	return &CommandNotFoundError{Command: command, detail: detail}
}

func isPlainCommand(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	return !strings.ContainsAny(command, " \t\n'\"\\$&;|<>*?()[]{}!`")
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
