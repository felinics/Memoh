package codex

import (
	"context"
	"fmt"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/agentprocess"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

const containerPath = "/opt/memoh/toolkit/bin:/usr/local/bin:/usr/bin:/bin"

type appServerProcess = agentprocess.Process

func startAppServer(ctx context.Context, client *bridge.Client, workDir, home string, cfg Config) (*appServerProcess, error) {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		workDir = defaultProjectPath
	}
	if err := client.Mkdir(ctx, home); err != nil {
		return nil, fmt.Errorf("create codex home %s: %w", home, err)
	}
	if err := materializeCodexConfig(ctx, client, home, cfg); err != nil {
		return nil, err
	}
	return agentprocess.Start(ctx, client, launcherPath+" app-server", workDir, codexAppServerEnv(home))
}

func codexAppServerEnv(home string) []string {
	return []string{
		"CODEX_HOME=" + home,
		"PATH=" + containerPath,
		"RUST_LOG=error",
	}
}
