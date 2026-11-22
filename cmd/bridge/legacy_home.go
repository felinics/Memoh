package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/felinics/memoh/internal/workspace/userenv"
)

// legacyHomeBudget bounds adoption at startup. The server waits for the
// bridge for 45 seconds; a run cut short is completed by the next start.
const legacyHomeBudget = 20 * time.Second

// legacyHomeToAdopt names the home a workspace user lived in before every
// command ran with userenv.Home, from the HOME the container was created
// with (the bridge's own): a container created without HOME=/data had the
// image's home as the user's, and what the user kept there has to move. A
// container created with HOME=/data never used another home, so whatever the
// image's home holds is the image's own and stays. An empty result with ok
// set leaves the choice to the passwd entry.
func legacyHomeToAdopt(containerHome string) (oldHome string, ok bool) {
	if containerHome == userenv.Home {
		return "", false
	}
	return containerHome, true
}

// adoptLegacyHome moves what the workspace user kept under a previous home
// into the workspace user environment's home before the bridge serves any
// command, so no terminal or agent sees the files mid-move. It never stops
// the bridge from starting.
func adoptLegacyHome(ctx context.Context, log *slog.Logger) {
	oldHome, ok := legacyHomeToAdopt(os.Getenv("HOME"))
	if !ok {
		return
	}
	runCtx, cancel := context.WithTimeout(ctx, legacyHomeBudget)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "/bin/sh", "-s", "--", oldHome, userenv.Home) //nolint:gosec // G204: fixed script; oldHome is the container's own HOME, passed as an argument.
	cmd.Dir = userenv.Home
	cmd.Stdin = strings.NewReader(userenv.LegacyHomeScript)
	// The script's cp and du run as children; end them with it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	decisions := userenv.LegacyHomeDecisions(stdout.String())
	attrs := make([]slog.Attr, 0, len(userenv.LegacyHomeActions)+2)
	for _, action := range userenv.LegacyHomeActions {
		if paths := decisions[action]; len(paths) > 0 {
			attrs = append(attrs, slog.Any(action, paths))
		}
	}
	if err != nil {
		attrs = append(attrs, slog.Any("error", err), slog.String("stderr", tail(stderr.String(), 512)))
		log.LogAttrs(ctx, slog.LevelWarn, "legacy home adoption incomplete", attrs...)
		return
	}
	if len(attrs) > 0 {
		log.LogAttrs(ctx, slog.LevelInfo, "legacy home adopted", attrs...)
	}
}

func tail(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[len(s)-limit:]
}
