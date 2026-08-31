package claudecode

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/agentprocess"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	containerPath      = "/opt/memoh/toolkit/bin:/usr/local/bin:/usr/bin:/bin"
	launcherPath       = "/opt/memoh/toolkit/bin/claude"
	configDir          = "/data/.claude"
	defaultProjectPath = "/data"
)

type cliProcess interface {
	io.Reader
	io.Writer
	CloseStdin()
	Done() <-chan struct{}
	StderrTail() string
	Close() error
}

func startCLI(ctx context.Context, client *bridge.Client, workDir string, args, env []string) (cliProcess, error) {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		workDir = defaultProjectPath
	}
	if err := client.Mkdir(ctx, configDir); err != nil {
		return nil, fmt.Errorf("create claude config directory %s: %w", configDir, err)
	}
	return agentprocess.Start(ctx, client, launcherPath+" "+strings.Join(args, " "), workDir, env)
}
