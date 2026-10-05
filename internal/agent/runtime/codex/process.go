package codex

import (
	"context"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/agentprocess"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/workspace/bridge"
	"github.com/felinics/memoh/internal/workspace/vpath"
)

// containerPath is the PATH the app-server and every command it spawns see.
// The managed dependency shim directory comes first so a managed
// overlay of an image runtime (node, python, uv) wins over the toolkit copy.
const containerPath = vpath.DataMount + "/.memoh/deps/bin:/opt/memoh/toolkit/bin:/usr/local/bin:/usr/bin:/bin"

type appServerProcess = agentprocess.Process

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
	proc, err := agentprocess.Start(ctx, client, appServerCommand(launcher), workDir, codexAppServerEnv(home))
	if err != nil {
		return nil, errs.WrapDependency(err, "")
	}
	return proc, nil
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
	return escapeShellArg(launcher) + " app-server -c features.goals=true"
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
