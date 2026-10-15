package acp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
)

func TestSessionPoolReplacesExitedIdleProcessBeforeNextPrompt(t *testing.T) {
	pool, root := newFakeScriptPoolForBot(t, enabledACPBot("bot-1", "api_key", nil))
	identityPath := filepath.Join(root, "owned-process")
	promptLog := filepath.Join(root, "prompts")
	launcher := filepath.Join(root, "bin", "codex-acp")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n%%s\\n' \"$$\" \"$TMPDIR\" > %s\nexport MEMOH_ACP_SESSION_POOL_FAKE_AGENT_CONFIG_LOG=%s\nMEMOH_ACP_SESSION_POOL_FAKE_AGENT=1 exec %s -test.run '^TestSessionPoolFakeAgentHelper$' --\n", sessionPoolShellArg(identityPath), sessionPoolShellArg(promptLog), sessionPoolShellArg(os.Args[0]))
	if err := os.WriteFile(launcher, []byte(script), 0o700); err != nil { //nolint:gosec // executable test-owned process fixture.
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	input := PromptInput{BotID: "bot-1", SessionID: "session-1", RunID: "first", AgentID: acpprofile.AgentACPID, ProjectPath: "/data/project", Prompt: "first", RuntimeOwnerAccountID: "user-1"}
	if result, err := pool.Prompt(ctx, input); err != nil || result.Text != "session-pool-ok" {
		t.Fatalf("first prompt = %+v, %v", result, err)
	}
	first := pool.sessionHandle(input.SessionID)
	identity, err := os.ReadFile(identityPath) //nolint:gosec // path is an owned fixture in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(strings.TrimSpace(string(identity)), "\n")
	if len(parts) != 2 || !strings.Contains(parts[1], "/memoh-acp-runtime/") {
		t.Fatalf("invalid owned process identity: %q", identity)
	}
	pid, err := strconv.Atoi(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Kill(); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(parts[1]); os.IsNotExist(err) {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("exited process did not finalize its runtime directory")
		case <-time.After(10 * time.Millisecond):
		}
	}
	input.RunID, input.Prompt = "second", "second"
	if result, err := pool.Prompt(ctx, input); err != nil || result.Text != "session-pool-ok" {
		t.Fatalf("next prompt reused exited process: result=%+v err=%v", result, err)
	}
	second := pool.sessionHandle(input.SessionID)
	if second == nil || second == first {
		t.Fatal("next prompt did not acquire a replacement runtime")
	}
	input.RunID, input.Prompt = "third", "third"
	if result, err := pool.Prompt(ctx, input); err != nil || result.Text != "session-pool-ok" {
		t.Fatalf("replacement did not continue: result=%+v err=%v", result, err)
	}
	if pool.sessionHandle(input.SessionID) != second {
		t.Fatal("healthy replacement was not reused")
	}
	logged, err := os.ReadFile(promptLog) //nolint:gosec // path is an owned fixture in t.TempDir.
	if err != nil || strings.Count(string(logged), "prompt:") != 3 {
		t.Fatalf("prompts were lost or repeated: %q, %v", logged, err)
	}
}

func TestSessionPoolDoesNotReplayPromptDispatchedBeforeProcessExit(t *testing.T) {
	pool, root := newFakeScriptPoolForBot(t, enabledACPBot("bot-1", "api_key", nil))
	started := filepath.Join(root, "prompt-started")
	starts := filepath.Join(root, "process-starts")
	identity := filepath.Join(root, "owned-process")
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_HANG_PROMPT", "1")
	t.Setenv("MEMOH_ACP_PROMPT_STARTED_FILE", started)
	t.Setenv("MEMOH_ACP_START_LOG", starts)
	launcher := filepath.Join(root, "bin", "codex-acp")
	script, err := os.ReadFile(launcher) //nolint:gosec // path is an owned fixture in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	script = []byte(strings.Replace(string(script), "#!/bin/sh\n", "#!/bin/sh\nprintf '%s' \"$$\" > "+sessionPoolShellArg(identity)+"\n", 1))
	if err := os.WriteFile(launcher, script, 0o700); err != nil { //nolint:gosec // executable test-owned process fixture.
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := pool.Prompt(ctx, PromptInput{BotID: "bot-1", SessionID: "session-1", RunID: "dispatched", AgentID: acpprofile.AgentACPID, ProjectPath: "/data/project", Prompt: "one attempt", RuntimeOwnerAccountID: "user-1"})
		done <- err
	}()
	waitForSessionPoolFile(t, started, 2*time.Second)
	raw, err := os.ReadFile(identity) //nolint:gosec // path is an owned fixture in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("dispatched prompt without a response succeeded")
		}
	case <-ctx.Done():
		t.Fatal("dispatched prompt did not terminate")
	}
	logged, err := os.ReadFile(starts) //nolint:gosec // path is an owned fixture in t.TempDir.
	if err != nil || string(logged) != "start\n" {
		t.Fatalf("dispatched prompt restarted automatically: %q, %v", logged, err)
	}
	if _, err := os.Stat(started); err != nil {
		t.Fatalf("partial prompt effect was lost: %v", err)
	}
}
