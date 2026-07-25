package client

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
)

func TestCodexACPLiveContainerAPIKey(t *testing.T) {
	if os.Getenv("MEMOH_LIVE_CODEX_ACP_CONTAINER") != "1" {
		t.Skip("set MEMOH_LIVE_CODEX_ACP_CONTAINER=1 to run the live Codex ACP container api_key smoke test")
	}
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY is required for the live Codex ACP container api_key smoke test")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("docker is required for the live Codex ACP container api_key smoke test: %v", err)
	}

	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "README.md"), []byte("# Memoh ACP container live smoke\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	client := startLiveBridgeContainer(t, root)

	runner := NewRunner(nil, testWorkspace{
		client: client,
		info: bridge.WorkspaceInfo{
			Backend:        bridge.WorkspaceBackendContainer,
			DefaultWorkDir: "/data",
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	managed := map[string]string{"api_key": apiKey}
	// An OpenAI-compatible proxy endpoint, e.g. https://proxy.example.com/v1.
	if baseURL := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")); baseURL != "" {
		managed["base_url"] = baseURL
	}
	if err := WriteCodexManagedConfig(ctx, client, managed); err != nil {
		t.Fatalf("write live Codex managed config: %v", err)
	}
	result, err := runner.Run(ctx, RunRequest{
		AgentID:     "codex",
		BotID:       "bot-live-container",
		Task:        "Reply with exactly this text and do not modify files: memoh-acp-container-live-ok",
		ProjectPath: "/data/project",
		Command:     "codex-acp",
		SetupMode:   SetupModeAPIKey,
		Timeout:     2 * time.Minute,
	})
	if err != nil {
		skipIfExternalCodexLimit(t, err)
		t.Fatalf("live container api_key Codex ACP run failed: %v", err)
	}
	if !strings.Contains(strings.ToLower(result.Text), "memoh-acp-container-live-ok") {
		t.Fatalf("live container api_key Codex ACP text = %q, want marker memoh-acp-container-live-ok", result.Text)
	}
}

func TestACPContainerAPIKeyCredentialNotInPSEF(t *testing.T) {
	if os.Getenv("MEMOH_LIVE_CODEX_ACP_CONTAINER") != "1" {
		t.Skip("set MEMOH_LIVE_CODEX_ACP_CONTAINER=1 to run the live ACP container ps smoke test")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("docker is required for the live ACP container ps smoke test: %v", err)
	}

	client := startLiveBridgeContainer(t, "")
	const secret = "sk-memoh-ps-secret" //nolint:gosec // live test uses a fake secret to verify process listing redaction.
	if err := WriteCodexManagedConfig(context.Background(), client, map[string]string{"api_key": secret}); err != nil {
		t.Fatalf("write Codex managed config: %v", err)
	}
	proc, err := startBridgeProcess(context.Background(), client, "sh", []string{"-c", "sleep 30"}, "/data", time.Minute, processOptions{
		Backend:   WorkspaceBackendContainer,
		AgentID:   "codex",
		SetupMode: SetupModeAPIKey,
		NoTimeout: true,
	})
	if err != nil {
		t.Fatalf("start managed process: %v", err)
	}
	defer func() { _ = proc.Close() }()

	time.Sleep(500 * time.Millisecond)
	result, err := client.Exec(context.Background(), "ps -ef", "/data", 10)
	if err != nil {
		t.Fatalf("ps -ef failed: %v", err)
	}
	ps := result.Stdout + result.Stderr
	if result.ExitCode != 0 {
		t.Fatalf("ps -ef exit code = %d\n%s", result.ExitCode, ps)
	}
	if strings.Contains(ps, "OPENAI_API_KEY") || strings.Contains(ps, secret) {
		t.Fatalf("ps -ef leaked managed credential:\n%s", ps)
	}
}

func startLiveBridgeContainer(t *testing.T, dataRoot string) *bridge.Client {
	t.Helper()
	repoRoot := findRepoRoot(t)
	image := strings.TrimSpace(os.Getenv("MEMOH_LIVE_CODEX_ACP_CONTAINER_IMAGE"))
	if image == "" {
		image = "memoh-toolkit-acp-bridge-live:local"
		runCmd(t, repoRoot, 5*time.Minute,
			"docker", "build",
			"-f", "docker/Dockerfile.workspace",
			"--target", "toolkit-acp-bridge-live",
			"-t", image,
			".",
		)
	}

	args := []string{
		"run", "-d", "--rm",
		"-e", "BRIDGE_TCP_ADDR=:1455",
		"-p", "127.0.0.1::1455",
	}
	if strings.TrimSpace(dataRoot) != "" {
		args = append(args, "-v", dataRoot+":/data")
	}
	args = append(args, image)
	containerID := strings.TrimSpace(runCmd(t, repoRoot, time.Minute, "docker", args...))
	if containerID == "" {
		t.Fatal("docker run did not return a container id")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "docker", "rm", "-f", containerID).Run() //nolint:gosec // live test runs operator-controlled docker cleanup.
	})

	port := waitForDockerBridgePort(t, containerID)
	client, err := bridge.Dial(context.Background(), net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		t.Fatalf("dial bridge: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	waitForBridgeExec(t, client)
	return client
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "docker", "Dockerfile.workspace")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("failed to locate repository root")
		}
		dir = parent
	}
}

func runCmd(t *testing.T, workDir string, timeout time.Duration, name string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // live test runs fixed docker commands assembled by test code.
	cmd.Dir = workDir
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("%s timed out: %v\n%s", name, ctx.Err(), string(output))
	}
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, string(output))
	}
	return string(output)
}

func waitForDockerBridgePort(t *testing.T, containerID string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		out, err := exec.CommandContext(ctx, "docker", "port", containerID, "1455/tcp").CombinedOutput() //nolint:gosec // live test inspects an operator-created container.
		cancel()
		if err == nil {
			fields := strings.Fields(strings.TrimSpace(string(out)))
			if len(fields) > 0 {
				_, port, splitErr := net.SplitHostPort(fields[len(fields)-1])
				if splitErr == nil && port != "" {
					return port
				}
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	logs, _ := exec.CommandContext(ctx, "docker", "logs", containerID).CombinedOutput() //nolint:gosec // live test reads logs from an operator-created container.
	t.Fatalf("bridge port was not published for container %s\n%s", containerID, string(logs))
	return ""
}

func waitForBridgeExec(t *testing.T, client *bridge.Client) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		result, err := client.Exec(ctx, "true", "/data", 5)
		cancel()
		if err == nil && result.ExitCode == 0 {
			return
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("exit code %d: %s%s", result.ExitCode, result.Stdout, result.Stderr)
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("bridge did not become ready: %v", lastErr)
}

func skipIfExternalCodexLimit(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "usage_limit_exceeded") ||
		strings.Contains(message, "you've hit your usage limit") ||
		strings.Contains(message, "insufficient_quota") {
		t.Skipf("Codex live smoke skipped because the external Codex account/API quota is unavailable: %v", err)
	}
}
