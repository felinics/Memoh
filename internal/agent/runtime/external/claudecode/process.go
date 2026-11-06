package claudecode

import (
	"context"
	"io"
	"path"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/external/agentproc"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	// defaultLauncherPath is the toolkit path of the CLI. It is used only when
	// no external.LauncherResolver is installed on the Driver. The canonical
	// workspace image does not ship an agent CLI, so this only resolves in
	// custom images that provide one; with a resolver the copy to execute
	// follows source precedence (managed → toolkit → PATH).
	defaultLauncherPath = "/opt/memoh/toolkit/bin/claude"
	// homeRoot holds each Agent's own config directory: its login, sessions
	// and settings. It is not under /data/.claude/agents, which a Claude Code
	// run from the workspace terminal reads as subagent definitions.
	homeRoot = "/data/.claude-code/agents"
	// credentialsFile is where Claude Code keeps its login in a config
	// directory on Linux.
	credentialsFile    = ".credentials.json"
	defaultProjectPath = "/data"
)

func claudeHome(botAgentID string) string {
	return path.Join(homeRoot, botAgentID)
}

type cliProcess interface {
	io.Reader
	io.Writer
	CloseStdin()
	Kill()
	Done() <-chan struct{}
	Err() error
	StderrTail() string
	Close() error
}

// startCLI spawns one Claude Code process of the Agent from the resolved
// launcher path, in the Agent's config directory.
func startCLI(ctx context.Context, client *bridge.Client, botAgentID, workDir string, args, env []string, launcher string) (cliProcess, error) {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		workDir = defaultProjectPath
	}
	home := claudeHome(botAgentID)
	if err := client.Mkdir(ctx, home); err != nil {
		return nil, errs.WrapDependency(err, "create claude config directory "+home)
	}
	proc, err := agentproc.Spawn(ctx, client, cliCommand(launcher, args), workDir, env)
	if err != nil {
		return nil, errs.WrapDependency(err, "")
	}
	return proc, nil
}

// cliCommand builds the bridge shell command line. The launcher path is
// single-quoted as one word (managed copies live under a version directory
// whose path is data, not syntax); args arrive pre-quoted from cliArgs.
func cliCommand(launcher string, args []string) string {
	command := agentproc.ShellQuote(strings.TrimSpace(launcher))
	if len(args) == 0 {
		return command
	}
	return command + " " + strings.Join(args, " ")
}

// drainCLI closes input and waits for the CLI's durable writes to finish.
// A result event can precede transcript flush, especially after /compact.
func drainCLI(proc cliProcess, timeout time.Duration) error {
	proc.CloseStdin()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-proc.Done():
		return errs.WrapDependency(proc.Err(), "")
	case <-timer.C:
		_ = proc.Close()
		return errs.NewDependency("claude did not exit after closing stdin")
	}
}
