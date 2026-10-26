package codex

import (
	"context"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/external/agentproc"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

type appServerProcess = agentproc.Child

// startAppServer launches `codex app-server` from the resolved launcher path.
func startAppServer(ctx context.Context, client *bridge.Client, workDir, home string, cfg Config, launcher string) (*appServerProcess, error) {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		workDir = defaultProjectPath
	}
	if err := client.Mkdir(ctx, home); err != nil {
		return nil, errs.WrapDependency(err, "create codex home "+home)
	}
	if err := materializeCodexConfig(ctx, client, home, cfg); err != nil {
		return nil, err
	}
	proc, err := agentproc.Spawn(ctx, client, appServerCommand(launcher), workDir, appServerEnv(home))
	if err != nil {
		return nil, errs.WrapDependency(err, "")
	}
	return proc, nil
}

// appServerEnv carries no provider configuration or credential: those reach
// Codex through its home.
func appServerEnv(home string) []string {
	return agentproc.BaseEnv("CODEX_HOME="+home, "RUST_LOG=error")
}

// appServerCommand builds the shell command line that starts the app-server.
func appServerCommand(launcher string) string {
	launcher = strings.TrimSpace(launcher)
	if launcher == "" {
		launcher = defaultLauncherPath
	}
	return agentproc.ShellQuote(launcher) + " app-server -c features.goals=true"
}
