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

// startAppServer launches `codex app-server` from the resolved launcher path.
func startAppServer(ctx context.Context, client *bridge.Client, workDir, home string, cfg Config, launcher string) (*appServerProcess, error) {
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
	return agentprocess.Start(ctx, client, appServerCommand(launcher), workDir, codexAppServerEnv(home))
}

func codexAppServerEnv(home string) []string {
	return []string{
		"CODEX_HOME=" + home,
		"PATH=" + containerPath,
		"RUST_LOG=error",
	}
}

// appServerCommand builds the shell command line that starts the app-server.
// The launcher path is quoted: managed installs live under per-bot data
// directories whose names are not under Memoh's control.
func appServerCommand(launcher string) string {
	launcher = strings.TrimSpace(launcher)
	if launcher == "" {
		launcher = defaultLauncherPath
	}
	return escapeShellArg(launcher) + " app-server"
}

// escapeShellArg single-quotes value for a POSIX shell when it carries any
// character the shell would otherwise interpret; plain paths pass through.
func escapeShellArg(value string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\n'\"\\$&;|<>*?()[]{}!`") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
