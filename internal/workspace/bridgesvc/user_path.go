package bridgesvc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/felinics/memoh/internal/workspace/shellenv/shellprobe"
)

// userShellPathTTL bounds how long a probed PATH is reused when none of the
// rc files the shell reads directly has changed. Files those source in turn
// (an nvm or cargo env script) are not watched, so a change there shows up
// within this time.
const userShellPathTTL = 30 * time.Second

// userShellPathRetry is how long a probe that failed is not repeated.
const userShellPathRetry = 10 * time.Second

// userShellPaths caches the PATH the workspace user's interactive shell ends
// up with. A variable so tests can substitute the probe.
var userShellPaths = &shellPathCache{probe: probeShellPath, entries: map[string]shellPathEntry{}}

type shellPathEntry struct {
	path        string
	fingerprint string
	expires     time.Time
}

type shellPathCache struct {
	mu      sync.Mutex
	probe   func(env []string, home string) (string, bool)
	entries map[string]shellPathEntry
}

// withUserShellPath gives env the PATH the workspace user's terminal has:
// the shell's own result (its rc files' additions first) followed by the
// command's PATH and then the workspace's own, so a command an image puts on
// its PATH stays reachable even for a command started on a clean environment.
func withUserShellPath(env []string) []string {
	seed := shellprobe.MergePaths(lastEnvValue(env, "PATH"), os.Getenv("PATH"))
	home := lastEnvValue(env, "HOME")
	probeEnv := setEnvValue(append([]string(nil), env...), "PATH", seed)
	path := seed
	if shellPath, ok := userShellPaths.lookup(probeEnv, home, seed); ok {
		path = shellprobe.MergePaths(shellPath, seed)
	}
	return setEnvValue(env, "PATH", path)
}

func (c *shellPathCache) lookup(env []string, home, seed string) (string, bool) {
	key := home + "\x00" + seed
	fingerprint := rcFingerprint(home)
	now := time.Now()
	c.mu.Lock()
	entry, ok := c.entries[key]
	c.mu.Unlock()
	if ok && entry.fingerprint == fingerprint && now.Before(entry.expires) {
		return entry.path, entry.path != ""
	}
	path, probed := c.probe(env, home)
	ttl := userShellPathTTL
	if !probed {
		path, ttl = "", userShellPathRetry
	}
	c.mu.Lock()
	c.entries[key] = shellPathEntry{path: path, fingerprint: fingerprint, expires: now.Add(ttl)}
	c.mu.Unlock()
	return path, path != ""
}

// rcFingerprint identifies the state of the rc files an interactive,
// non-login shell reads, the kind the browser terminal starts.
func rcFingerprint(home string) string {
	files := []string{"/etc/bash.bashrc"}
	if home != "" {
		files = append(files, filepath.Join(home, ".bashrc"))
	}
	var b strings.Builder
	for _, file := range files {
		b.WriteString(file)
		if info, err := os.Stat(file); err == nil { //nolint:gosec // G703: stat only, of the shell's own rc files.
			b.WriteString(":" + strconv.FormatInt(info.Size(), 10) + ":" + strconv.FormatInt(info.ModTime().UnixNano(), 10))
		}
		b.WriteString(";")
	}
	return b.String()
}

// probeShellPath runs the shell probe the server's launchers use
// (shellprobe.PathProbeCommand) inside the workspace.
func probeShellPath(env []string, home string) (string, bool) {
	timeout := shellprobe.DefaultTimeout
	ctx, cancel := context.WithTimeout(context.Background(), timeout+time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", shellprobe.PathProbeCommand(timeout)) //nolint:gosec // G204: fixed probe command.
	cmd.Env = env
	if home != "" {
		cmd.Dir = home
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	out, err := cmd.Output()
	path := strings.TrimSpace(string(out))
	if err != nil || path == "" {
		return "", false
	}
	return path, true
}

func lastEnvValue(env []string, name string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if key, value, ok := strings.Cut(env[i], "="); ok && key == name {
			return value
		}
	}
	return ""
}

// setEnvValue replaces every entry for name with a single one at the end.
func setEnvValue(env []string, name, value string) []string {
	out := env[:0]
	for _, entry := range env {
		if key, _, ok := strings.Cut(entry, "="); ok && key == name {
			continue
		}
		out = append(out, entry)
	}
	return append(out, name+"="+value)
}
