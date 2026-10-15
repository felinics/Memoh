//go:build dockertest && linux

package acp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/runtime/acp/client"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

func TestDockerSessionPoolRecoversAfterWorkspaceStopStart(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	root := t.TempDir()
	dataDir, socketDir := filepath.Join(root, "data"), filepath.Join(root, "socket")
	for _, dir := range []string{filepath.Join(dataDir, "project"), socketDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	repository, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(root, "bridge"), "./cmd/bridge") //nolint:gosec // fixed repository command with a t.TempDir output.
	build.Dir = repository
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build real workspace bridge: %v\n%s", err, output)
	}
	binary, err := os.ReadFile(os.Args[0]) //nolint:gosec // copy this controlled Go test executable into the fixture.
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "agent-test"), binary, 0o700); err != nil { //nolint:gosec // executable copy of this test binary.
		t.Fatal(err)
	}
	script := "#!/bin/sh\nexport MEMOH_ACP_SESSION_POOL_FAKE_AGENT=1\nexport MEMOH_ACP_SESSION_POOL_FAKE_AGENT_CONFIG_LOG=/data/prompts\nprintf '%s\\n' \"$TMPDIR\" >> /data/runtime-roots\nexec /test/agent-test -test.run '^TestSessionPoolFakeAgentHelper$' --\n"
	if err := os.WriteFile(filepath.Join(root, "agent"), []byte(script), 0o700); err != nil { //nolint:gosec // executable test-owned process fixture.
		t.Fatal(err)
	}
	image := os.Getenv("MEMOH_ACP_TEST_IMAGE")
	if image == "" {
		image = "memohai/workspace:debian"
	}
	name := "memoh-acp-restart-" + uuid.NewString()
	docker := func(commandCtx context.Context, args ...string) error {
		output, err := exec.CommandContext(commandCtx, "docker", args...).CombinedOutput() //nolint:gosec // argv only operates on this UUID-named fixture.
		if err != nil {
			return fmt.Errorf("docker %s: %w: %s", args[0], err, output)
		}
		return nil
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if err := docker(cleanupCtx, "rm", "-f", name); err != nil {
			t.Error(err)
		}
	})
	if err := docker(ctx, "run", "-d", "--name", name, "--network", "none", "--memory", "512m", "--cpus", "1", "--pids-limit", "128",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--entrypoint", "/test/bridge",
		"--mount", "type=bind,src="+root+",dst=/test,readonly",
		"--mount", "type=bind,src="+dataDir+",dst=/data",
		"--mount", "type=bind,src="+socketDir+",dst=/bridge",
		"-e", "BRIDGE_SOCKET_PATH=/bridge/bridge.sock", image); err != nil {
		t.Fatal(err)
	}
	connection, err := bridge.Dial(ctx, "unix://"+filepath.Join(socketDir, "bridge.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	waitReady := func() {
		for {
			if _, err := connection.Stat(ctx, "/data/project"); err == nil {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal("workspace bridge did not become ready")
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	waitReady()
	runner := client.NewRunner(nil, sessionPoolWorkspace{client: connection, info: bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"}})
	pool := newSessionPool(nil, runner, fakeBotGetter{bot: enabledACPBot("bot", "api_key", map[string]any{"command": "/test/agent"})})
	t.Cleanup(pool.CloseAll)
	input := PromptInput{BotID: "bot", SessionID: "session", RunID: "first", AgentID: acpprofile.AgentACPID, ProjectPath: "/data/project", Prompt: "first", RuntimeOwnerAccountID: "owner"}
	if result, err := pool.Prompt(ctx, input); err != nil || result.Text != "session-pool-ok" {
		t.Fatalf("first Docker prompt = %+v, %v", result, err)
	}
	first := pool.sessionHandle(input.SessionID)
	if err := docker(ctx, "stop", "--time", "1", name); err != nil {
		t.Fatal(err)
	}
	for !first.session.Disconnected() {
		select {
		case <-ctx.Done():
			t.Fatal("stopped workspace did not disconnect its ACP transport")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := docker(ctx, "start", name); err != nil {
		t.Fatal(err)
	}
	waitReady()
	for _, runID := range []string{"second", "third"} {
		input.RunID, input.Prompt = runID, runID
		if result, err := pool.Prompt(ctx, input); err != nil || result.TerminationUnconfirmed || result.Text != "session-pool-ok" {
			t.Fatalf("ready replacement inherited stale connection or cleanup failure: run=%s result=%+v err=%v", runID, result, err)
		}
	}
	if replacement := pool.sessionHandle(input.SessionID); replacement == nil || replacement == first {
		t.Fatal("workspace restart retained the stale pool handle")
	}
	prompts, err := os.ReadFile(filepath.Join(dataDir, "prompts")) //nolint:gosec // fixed receipt filename under the test-owned data directory.
	if err != nil || strings.Count(string(prompts), "prompt:") != 3 {
		t.Fatalf("Docker prompt effects were lost or replayed: %q, %v", prompts, err)
	}
	roots, err := os.ReadFile(filepath.Join(dataDir, "runtime-roots")) //nolint:gosec // fixed receipt filename under the test-owned data directory.
	if err != nil || len(strings.Fields(string(roots))) != 2 {
		t.Fatalf("expected one initial and one replacement process: %q, %v", roots, err)
	}
}
