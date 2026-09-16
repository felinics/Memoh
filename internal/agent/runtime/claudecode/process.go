package claudecode

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/agentprocess"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/workspace/bridge"
	"github.com/felinics/memoh/internal/workspace/payloadlease"
	"github.com/felinics/memoh/internal/workspace/vpath"
)

const (
	// containerPath is the PATH the CLI and every command it spawns see. The
	// managed dependency shim directory comes first so a managed
	// overlay of an image runtime (node, python, uv) wins over the toolkit
	// copy.
	containerPath = vpath.DataMount + "/.memoh/deps/bin:/opt/memoh/toolkit/bin:/usr/local/bin:/usr/bin:/bin"
	// defaultLauncherPath is the toolkit path of the CLI. It is used only when
	// no external.LauncherResolver is installed on the Driver. The canonical
	// workspace image does not ship an agent CLI, so this only resolves in
	// custom images that provide one; with a resolver the copy to execute
	// follows source precedence (managed → toolkit → PATH).
	defaultLauncherPath = "/opt/memoh/toolkit/bin/claude"
	configDir           = "/data/.claude"
	defaultProjectPath  = "/data"
)

type cliProcess interface {
	io.Reader
	io.Writer
	CloseStdin()
	Done() <-chan struct{}
	Err() error
	StderrTail() string
	Close() error
}

// startCLI spawns one Claude Code process from the resolved launcher path.
func startCLI(ctx context.Context, client *bridge.Client, workDir string, args, env []string, launcher external.Launcher) (cliProcess, error) {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		workDir = defaultProjectPath
	}
	if err := client.Mkdir(ctx, configDir); err != nil {
		return nil, fmt.Errorf("create claude config directory %s: %w", configDir, err)
	}
	return agentprocess.Start(ctx, client, payloadlease.Command(cliCommand(launcher.Path, args), launcher.LeasePath, launcher.WorkspaceEpoch), workDir, env)
}

// cliCommand builds the bridge shell command line. The launcher path is
// single-quoted as one word (managed copies live under a version directory
// whose path is data, not syntax); args arrive pre-quoted from cliArgs.
func cliCommand(launcher string, args []string) string {
	command := shellQuote(strings.TrimSpace(launcher))
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
		return proc.Err()
	case <-timer.C:
		_ = proc.Close()
		return errors.New("claude did not exit after closing stdin")
	}
}
