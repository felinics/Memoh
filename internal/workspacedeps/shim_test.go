package workspacedeps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

func TestLayoutPaths(t *testing.T) {
	home := Home("/data", "codex")
	cases := map[string]string{
		DepsRoot("/data"):           "/data/.memoh/deps",
		home:                        "/data/.memoh/deps/codex",
		ShimDir("/data"):            "/data/.memoh/deps/bin",
		LocksDir("/data"):           "/data/.memoh/deps/.locks",
		StatePath(home):             "/data/.memoh/deps/codex/state.json",
		VersionsDir(home):           "/data/.memoh/deps/codex/versions",
		CurrentDir(home):            "/data/.memoh/deps/codex/current",
		lockPath(home, "codex"):     "/data/.memoh/deps/.locks/codex.lock",
		Home("/Users/me/ws/", "uv"): "/Users/me/ws/.memoh/deps/uv",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestShimScript(t *testing.T) {
	plain := ShimScript("/data/.memoh/deps/uv/current/bin/uv", false)
	if !strings.HasPrefix(plain, "#!/bin/sh\n") || !strings.HasSuffix(plain, "exec '/data/.memoh/deps/uv/current/bin/uv' \"$@\"\n") {
		t.Errorf("plain shim = %q", plain)
	}
	if strings.Contains(plain, "SSL_CERT_FILE") {
		t.Error("plain shim must not touch SSL_CERT_FILE")
	}
	agent := ShimScript("/data/.memoh/deps/claude-code/current/bin/claude", true)
	for _, want := range []string{
		`if [ -z "${SSL_CERT_FILE:-}" ] && [ -f /opt/memoh/toolkit/certs/ca-certificates.crt ]; then`,
		"export SSL_CERT_FILE=/opt/memoh/toolkit/certs/ca-certificates.crt",
		`exec '/data/.memoh/deps/claude-code/current/bin/claude' "$@"`,
	} {
		if !strings.Contains(agent, want) {
			t.Errorf("agent shim missing %q:\n%s", want, agent)
		}
	}
}

func TestWriteShimsCreatesExecutableWrappers(t *testing.T) {
	client := newExecTestClient(t)
	ctx := testContext(t)
	dataRoot := t.TempDir()
	binDir := t.TempDir()
	target := writeExecutable(t, binDir, "real-tool", "printf 'real:%s\\n' \"$@\"\n")
	shimDir := ShimDir(dataRoot)

	err := WriteShims(ctx, client, shimDir, map[string]string{"tool": target, "tool-alias": target}, true)
	if err != nil {
		t.Fatalf("WriteShims: %v", err)
	}
	for _, name := range []string{"tool", "tool-alias"} {
		info, err := os.Stat(filepath.Join(shimDir, name))
		if err != nil {
			t.Fatalf("stat shim %s: %v", name, err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Errorf("shim %s mode = %o, want 0755", name, info.Mode().Perm())
		}
	}
	result, err := client.ExecWithOptions(ctx, shellQuote(filepath.Join(shimDir, "tool"))+" one 'two words'", "", 30, nil, bridge.ExecOptions{})
	if err != nil {
		t.Fatalf("exec shim: %v", err)
	}
	if result.ExitCode != 0 || result.Stdout != "real:one\nreal:two words\n" {
		t.Errorf("shim exec = exit %d stdout %q stderr %q", result.ExitCode, result.Stdout, result.Stderr)
	}
}

func TestWriteShimsValidatesInput(t *testing.T) {
	client := newExecTestClient(t)
	ctx := testContext(t)
	shimDir := ShimDir(t.TempDir())
	if err := WriteShims(ctx, client, shimDir, map[string]string{"../escape": "/bin/true"}, false); err == nil {
		t.Error("WriteShims accepted a name that escapes the shim directory")
	}
	if err := WriteShims(ctx, client, shimDir, map[string]string{"tool": ""}, false); err == nil {
		t.Error("WriteShims accepted an empty entrypoint")
	}
	if err := WriteShims(ctx, client, shimDir, nil, false); err != nil {
		t.Errorf("WriteShims with no entrypoints = %v, want nil", err)
	}
	if err := WriteShims(ctx, nil, shimDir, map[string]string{"tool": "/bin/true"}, false); err == nil {
		t.Error("WriteShims accepted a nil client")
	}
}
