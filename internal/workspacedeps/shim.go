package workspacedeps

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

// toolkitCABundle is the CA bundle the workspace image ships for the agent
// CLIs. Agent shims export it as SSL_CERT_FILE when nothing else set one,
// mirroring docker/toolkit/bin/claude.
const toolkitCABundle = "/opt/memoh/toolkit/certs/ca-certificates.crt"

const shimChmodTimeoutSeconds = 30

// ShimScript returns the contents of a PATH shim that execs entrypoint with
// the caller's arguments. Agent shims additionally point SSL_CERT_FILE at the
// toolkit CA bundle when it exists and the variable is unset.
func ShimScript(entrypoint string, agent bool) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	if agent {
		b.WriteString(`if [ -z "${SSL_CERT_FILE:-}" ] && [ -f ` + toolkitCABundle + ` ]; then
  export SSL_CERT_FILE=` + toolkitCABundle + `
fi
`)
	}
	b.WriteString("exec " + shellQuote(entrypoint) + ` "$@"` + "\n")
	return b.String()
}

// WriteShims writes one shim per entrypoint into shimDir and marks them
// executable. Keys are the command names (the file names inside shimDir),
// values the absolute entrypoint paths reported by the install script.
func WriteShims(ctx context.Context, client *bridge.Client, shimDir string, entrypoints map[string]string, agent bool) error {
	if client == nil {
		return errors.New("workspacedeps: bridge client is nil")
	}
	if strings.TrimSpace(shimDir) == "" {
		return errors.New("workspacedeps: shim directory is empty")
	}
	names := make([]string, 0, len(entrypoints))
	for name, entrypoint := range entrypoints {
		if !isPlainFileName(name) {
			return fmt.Errorf("workspacedeps: shim name %q must be a plain file name", name)
		}
		if strings.TrimSpace(entrypoint) == "" {
			return fmt.Errorf("workspacedeps: shim %q has an empty entrypoint", name)
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	if err := client.Mkdir(ctx, shimDir); err != nil {
		return fmt.Errorf("workspacedeps: create shim directory %s: %w", shimDir, err)
	}
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		target := path.Join(shimDir, name)
		if err := client.WriteFile(ctx, target, []byte(ShimScript(entrypoints[name], agent))); err != nil {
			return fmt.Errorf("workspacedeps: write shim %s: %w", target, err)
		}
		quoted = append(quoted, shellQuote(target))
	}
	// The bridge writes files with 0600 through a temp file, so the exec bit
	// has to be set explicitly. One chmod covers every shim; the paths are
	// absolute, so no `--` is needed (BSD chmod rejects it anyway).
	command := "chmod 0755 " + strings.Join(quoted, " ")
	result, err := client.ExecWithOptions(ctx, command, "", shimChmodTimeoutSeconds, nil, bridge.ExecOptions{})
	if err != nil {
		return fmt.Errorf("workspacedeps: chmod shims in %s: %w", shimDir, err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("workspacedeps: chmod shims in %s exited %d: %s", shimDir, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return nil
}

// isPlainFileName rejects names that would escape the target directory.
func isPlainFileName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\x00") && path.Base(name) == name
}
