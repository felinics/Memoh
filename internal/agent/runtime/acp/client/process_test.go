package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/memohai/memoh/internal/workspace/bridge"
	pb "github.com/memohai/memoh/internal/workspace/bridgepb"
)

func TestBuildShellCommandQuotesCommandAndArgs(t *testing.T) {
	got := buildShellCommand("codex-acp", []string{"--flag", "value with spaces", "it's", "$HOME"})
	want := `codex-acp --flag 'value with spaces' 'it'\''s' '$HOME'`
	if got != want {
		t.Fatalf("buildShellCommand() = %q, want %q", got, want)
	}
}

func TestPrepareRuntimeLeaseUsesProfileStateEnvAndWorkspaceToolHome(t *testing.T) {
	tests := []struct {
		name        string
		agentID     string
		mode        SetupMode
		stateEnv    string
		runtimeHome bool
		additional  func(*testing.T, *runtimeLease, *recordingBridgeServer)
	}{
		{
			name:     "codex api key",
			agentID:  "codex",
			mode:     SetupModeAPIKey,
			stateEnv: "CODEX_HOME",
			additional: func(t *testing.T, lease *runtimeLease, _ *recordingBridgeServer) {
				t.Helper()
				if sqliteHome := envValue(lease.agentEnv, "CODEX_SQLITE_HOME"); !strings.HasPrefix(sqliteHome, lease.root+"/") || sqliteHome == envValue(lease.agentEnv, "CODEX_HOME") {
					t.Fatalf("CODEX_SQLITE_HOME = %q, want a distinct path under %q", sqliteHome, lease.root)
				}
			},
		},
		{
			name:     "claude managed oauth",
			agentID:  "claude-code",
			mode:     SetupModeOAuth,
			stateEnv: "CLAUDE_CONFIG_DIR",
			additional: func(t *testing.T, lease *runtimeLease, server *recordingBridgeServer) {
				t.Helper()
				settings, ok := findWrite(server.writes(), path.Join(envValue(lease.agentEnv, "CLAUDE_CONFIG_DIR"), "settings.json"))
				if !ok || !strings.Contains(string(settings.Content), `"ask"`) || !strings.Contains(string(settings.Content), `"Bash"`) {
					t.Fatalf("managed Claude settings = %#v", settings)
				}
			},
		},
		{
			name:        "hermes self",
			agentID:     "hermes",
			mode:        SetupModeSelf,
			stateEnv:    "HERMES_HOME",
			runtimeHome: true,
			additional: func(t *testing.T, lease *runtimeLease, _ *recordingBridgeServer) {
				t.Helper()
				if got := envValue(lease.agentEnv, "HERMES_REAL_HOME"); got != dataMountPath {
					t.Fatalf("HERMES_REAL_HOME = %q, want %q", got, dataMountPath)
				}
				if got := envValue(lease.agentEnv, "UV_CACHE_DIR"); !strings.HasPrefix(got, runtimeCacheRoot+"/") || strings.HasPrefix(got, lease.root+"/") {
					t.Fatalf("UV_CACHE_DIR = %q, want shared container-local cache", got)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, server := newRecordingBridgeClient(t)
			lease, err := prepareRuntimeLease(context.Background(), client, processOptions{
				Backend:   WorkspaceBackendContainer,
				BotID:     "bot-1",
				AgentID:   test.agentID,
				SetupMode: test.mode,
				Env:       []string{"CUSTOM_FLAG=enabled", "HOME=/host-home", test.stateEnv + "=/host-state"},
			})
			if err != nil {
				t.Fatalf("prepareRuntimeLease() error = %v", err)
			}
			if !validOwnedRuntimeRoot(lease.root, test.agentID) {
				t.Fatalf("runtime root = %q, want process-owned UUID path", lease.root)
			}
			wantAgentHome := dataMountPath
			if test.runtimeHome {
				wantAgentHome = path.Join(lease.root, "home")
			}
			if got := envValue(lease.agentEnv, "HOME"); got != wantAgentHome {
				t.Fatalf("agent HOME = %q, want %q", got, wantAgentHome)
			}
			stateHome := envValue(lease.agentEnv, test.stateEnv)
			if stateHome != path.Join(lease.root, "state") {
				t.Fatalf("%s = %q, want lease state dir", test.stateEnv, stateHome)
			}
			if got := envValue(lease.toolEnv, "HOME"); got != dataMountPath {
				t.Fatalf("tool HOME = %q, want workspace HOME", got)
			}
			if envHasKey(lease.toolEnv, test.stateEnv) {
				t.Fatalf("tool env leaked %s: %v", test.stateEnv, lease.toolEnv)
			}
			assertEnvHas(t, lease.agentEnv, "CUSTOM_FLAG=enabled")
			if test.additional != nil {
				test.additional(t, lease, server)
			}
			if err := lease.finalize(context.Background(), false); err != nil {
				t.Fatalf("lease cleanup error = %v", err)
			}
			if server.exists(lease.root) {
				t.Fatalf("runtime root %q still exists after cleanup", lease.root)
			}
		})
	}
}

func TestPrepareRuntimeLeaseFiltersManagedHermesHostCredentials(t *testing.T) {
	client, _ := newRecordingBridgeClient(t)
	lease, err := prepareRuntimeLease(context.Background(), client, processOptions{
		AgentID:   "hermes",
		SetupMode: SetupModeAPIKey,
		Env:       []string{"HERMES_HOME=/host/hermes", "OPENAI_API_KEY=sk-host", "OPENROUTER_API_KEY=sk-router", "CUSTOM_FLAG=1"},
		UnsetEnv:  HermesManagedUnsetEnvKeys(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.finalize(context.Background(), false) }()
	if envHasKey(lease.agentEnv, "OPENAI_API_KEY") || envHasKey(lease.agentEnv, "OPENROUTER_API_KEY") {
		t.Fatalf("host provider credential leaked into Hermes env: %v", lease.agentEnv)
	}
	if got := envValue(lease.agentEnv, "HERMES_HOME"); got != path.Join(lease.root, "state") {
		t.Fatalf("HERMES_HOME = %q, want lease state dir", got)
	}
	assertEnvHas(t, lease.agentEnv, "CUSTOM_FLAG=1")
}

func TestStartBridgeProcessHermesManagedPassesCleanEnvControls(t *testing.T) {
	client, server := newRecordingBridgeClient(t)
	proc, err := startBridgeProcess(context.Background(), client, "hermes-acp", nil, "/data", time.Minute, processOptions{
		Backend:   WorkspaceBackendContainer,
		BotID:     "bot-hermes",
		AgentID:   "hermes",
		SetupMode: SetupModeAPIKey,
		CleanEnv:  true,
		UnsetEnv:  HermesManagedUnsetEnvKeys(),
	})
	if err != nil {
		t.Fatalf("startBridgeProcess() error = %v", err)
	}
	server.waitForRecordWithTimeout(t, int32(time.Minute.Seconds()), 2*time.Second)
	_ = proc.Close()
	processRecord, ok := findRecordWithTimeout(server.records(), int32(time.Minute.Seconds()))
	if !ok {
		t.Fatalf("missing process exec record: %#v", server.records())
	}
	if !processRecord.CleanEnv {
		t.Fatalf("CleanEnv = false, want true")
	}
	if !hasString(processRecord.UnsetEnv, "HERMES_*") || !hasString(processRecord.UnsetEnv, "MEMOH_HERMES_API_KEY") || !hasString(processRecord.UnsetEnv, "OPENAI_API_KEY") || !hasString(processRecord.UnsetEnv, "OPENAI_BASE_URL") {
		t.Fatalf("UnsetEnv = %#v, want Hermes/provider cleanup keys", processRecord.UnsetEnv)
	}
}

func TestCreateTerminalFiltersBlockedHermesEnv(t *testing.T) {
	client, server := newRecordingBridgeClient(t)
	manager := newTerminalManager(
		context.Background(),
		client,
		"/data",
		"/data",
		7,
		[]string{"HERMES_HOME=/data/.memoh-hermes"},
		true,
		HermesManagedUnsetEnvKeys(),
		nil,
	)
	term, err := manager.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
		Command: "env",
		Env: []acp.EnvVariable{
			{Name: "OPENAI_API_KEY", Value: "sk-agent"},
			{Name: "MEMOH_HERMES_API_KEY", Value: "sk-agent-memoh"},
			{Name: "OPENROUTER_API_KEY", Value: "sk-router"},
			{Name: "CUSTOM_FLAG", Value: "1"},
		},
	}, nil, terminalRuntimeScope{})
	if err != nil {
		t.Fatalf("CreateTerminal() error = %v", err)
	}
	if _, err := manager.WaitForTerminalExit(context.Background(), acp.WaitForTerminalExitRequest{TerminalId: term.TerminalId}); err != nil {
		t.Fatalf("WaitForTerminalExit() error = %v", err)
	}
	server.waitForRecordWithTimeout(t, 7, time.Second)
	record, ok := findRecordWithTimeout(server.records(), 7)
	if !ok {
		t.Fatalf("missing terminal exec record: %#v", server.records())
	}
	if !record.CleanEnv {
		t.Fatalf("terminal CleanEnv = false, want true")
	}
	if !envHasKeyValue(record.Env, "HERMES_HOME", "/data/.memoh-hermes") {
		t.Fatalf("terminal env missing managed HERMES_HOME: %#v", record.Env)
	}
	if !envHasKeyValue(record.Env, "CUSTOM_FLAG", "1") {
		t.Fatalf("terminal env missing allowed custom flag: %#v", record.Env)
	}
	if envHasKey(record.Env, "MEMOH_HERMES_API_KEY") || envHasKey(record.Env, "OPENAI_API_KEY") || envHasKey(record.Env, "OPENROUTER_API_KEY") {
		t.Fatalf("terminal env leaked provider key: %#v", record.Env)
	}
	for _, key := range []string{"HERMES_*", "MEMOH_HERMES_API_KEY", "OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENROUTER_API_KEY", "OPENROUTER_BASE_URL", "GOOGLE_API_KEY", "GOOGLE_BASE_URL", "GEMINI_API_KEY", "GEMINI_BASE_URL"} {
		if !hasString(record.UnsetEnv, key) {
			t.Fatalf("terminal UnsetEnv = %#v, missing %q", record.UnsetEnv, key)
		}
	}
}

func TestWriteCodexManagedConfigWritesOAuthAuth(t *testing.T) { //nolint:gosec // test fixture validates token-shaped Codex auth JSON.
	client, server := newRecordingBridgeClient(t)
	lastRefresh := time.Date(2026, 5, 28, 1, 2, 3, 0, time.UTC)
	err := WriteCodexManagedConfigWithAuth(context.Background(), client, CodexManagedConfig{
		Mode: SetupModeOAuth,
		OAuth: &CodexOAuthCredentials{ //nolint:gosec // test fixture token-shaped values
			AccessToken:  "access.jwt.token",
			IDToken:      "id.jwt.token",
			RefreshToken: "refresh-token",
			AccountID:    "account-123",
			BaseURL:      "https://chatgpt.com/backend-api",
			LastRefresh:  lastRefresh,
		},
	})
	if err != nil {
		t.Fatalf("WriteCodexManagedConfigWithAuth() error = %v", err)
	}
	writes := server.writes()
	if len(writes) != 2 {
		t.Fatalf("managed writes len = %d, want config.toml + auth.json: %#v", len(writes), writes)
	}
	if writes[0].Path != CodexManagedConfigDir+"/auth.json" || writes[1].Path != CodexManagedConfigDir+"/config.toml" {
		t.Fatalf("managed writes order = %#v, want auth.json then config.toml", writes)
	}
	configWrite, ok := findWrite(writes, CodexManagedConfigDir+"/config.toml")
	if !ok {
		t.Fatalf("missing Codex config.toml write: %#v", writes)
	}
	config := string(configWrite.Content)
	for _, want := range []string{
		`model_provider = "chatgpt-http"`,
		`model_reasoning_effort = "xhigh"`,
		`model_reasoning_summary = "detailed"`,
		`model_supports_reasoning_summaries = true`,
		`hide_agent_reasoning = false`,
		`show_raw_agent_reasoning = false`,
		`[model_providers.chatgpt-http]`,
		`name = "ChatGPT HTTP"`,
		`base_url = "https://chatgpt.com/backend-api/codex"`,
		`wire_api = "responses"`,
		`requires_openai_auth = true`,
		`supports_websockets = false`,
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("Codex OAuth config missing %q:\n%s", want, config)
		}
	}
	authWrite, ok := findWrite(writes, CodexManagedConfigDir+"/auth.json")
	if !ok {
		t.Fatalf("missing Codex auth.json write: %#v", writes)
	}
	var auth map[string]any
	if err := json.Unmarshal(authWrite.Content, &auth); err != nil {
		t.Fatalf("invalid auth json: %v\n%s", err, string(authWrite.Content))
	}
	if auth["auth_mode"] != "chatgpt" {
		t.Fatalf("auth_mode = %#v, want chatgpt", auth["auth_mode"])
	}
	tokens, ok := auth["tokens"].(map[string]any)
	if !ok {
		t.Fatalf("tokens missing from auth json: %#v", auth)
	}
	for key, want := range map[string]string{ //nolint:gosec // test fixture token-shaped values
		"id_token":      "id.jwt.token",
		"access_token":  "access.jwt.token",
		"refresh_token": "refresh-token",
		"account_id":    "account-123",
	} {
		if got := tokens[key]; got != want {
			t.Fatalf("tokens[%s] = %#v, want %q", key, got, want)
		}
	}
	if auth["last_refresh"] != lastRefresh.Format(time.RFC3339Nano) {
		t.Fatalf("last_refresh = %#v, want %q", auth["last_refresh"], lastRefresh.Format(time.RFC3339Nano))
	}
}

func TestWriteCodexManagedConfigWritesOAuthAuthWithoutAccountID(t *testing.T) { //nolint:gosec // test fixture validates token-shaped Codex auth JSON.
	client, server := newRecordingBridgeClient(t)
	err := WriteCodexManagedConfigWithAuth(context.Background(), client, CodexManagedConfig{
		Mode: SetupModeOAuth,
		OAuth: &CodexOAuthCredentials{ //nolint:gosec // test fixture token-shaped values
			AccessToken:  "access.jwt.token",
			IDToken:      "id.jwt.token",
			RefreshToken: "refresh-token",
		},
	})
	if err != nil {
		t.Fatalf("WriteCodexManagedConfigWithAuth() error = %v", err)
	}
	writes := server.writes()
	authWrite, ok := findWrite(writes, CodexManagedConfigDir+"/auth.json")
	if !ok {
		t.Fatalf("missing Codex auth.json write: %#v", writes)
	}
	var auth map[string]any
	if err := json.Unmarshal(authWrite.Content, &auth); err != nil {
		t.Fatalf("invalid auth json: %v\n%s", err, string(authWrite.Content))
	}
	tokens, ok := auth["tokens"].(map[string]any)
	if !ok {
		t.Fatalf("tokens missing from auth json: %#v", auth)
	}
	if _, ok := tokens["account_id"]; ok {
		t.Fatalf("auth json should omit empty account_id: %#v", tokens)
	}
}

func TestWriteCodexManagedConfigFileWritesOnlyConfig(t *testing.T) {
	client, server := newRecordingBridgeClient(t)
	if err := WriteCodexManagedConfigFile(context.Background(), client, CodexManagedConfig{Mode: SetupModeOAuth}); err != nil {
		t.Fatalf("WriteCodexManagedConfigFile() error = %v", err)
	}
	writes := server.writes()
	if len(writes) != 1 {
		t.Fatalf("writes len = %d, want only config.toml: %#v", len(writes), writes)
	}
	configWrite, ok := findWrite(writes, CodexManagedConfigDir+"/config.toml")
	if !ok {
		t.Fatalf("missing Codex config.toml write: %#v", writes)
	}
	config := string(configWrite.Content)
	for _, want := range []string{
		`model_provider = "chatgpt-http"`,
		`model_reasoning_summary = "detailed"`,
		`hide_agent_reasoning = false`,
		`show_raw_agent_reasoning = false`,
		`requires_openai_auth = true`,
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("Codex config missing %q:\n%s", want, config)
		}
	}
	if _, ok := findWrite(writes, CodexManagedConfigDir+"/auth.json"); ok {
		t.Fatalf("config-only write unexpectedly touched auth.json: %#v", writes)
	}
}

func TestWriteCodexManagedConfigFilePreservesOAuthBaseURL(t *testing.T) {
	t.Parallel()

	client := newTestBridgeClient(t, t.TempDir())
	if err := WriteCodexManagedConfigWithAuth(context.Background(), client, CodexManagedConfig{
		Mode: SetupModeOAuth,
		OAuth: &CodexOAuthCredentials{ //nolint:gosec // test fixture token-shaped values
			AccessToken: "access.jwt.token",
			IDToken:     "id.jwt.token",
			AccountID:   "account-123",
			BaseURL:     "https://enterprise.example/backend-api/codex",
		},
	}); err != nil {
		t.Fatalf("WriteCodexManagedConfigWithAuth() error = %v", err)
	}

	// A config-only refresh without credentials in hand must keep the custom
	// endpoint instead of resetting it to the default ChatGPT URL.
	if err := WriteCodexManagedConfigFile(context.Background(), client, CodexManagedConfig{Mode: SetupModeOAuth}); err != nil {
		t.Fatalf("WriteCodexManagedConfigFile() error = %v", err)
	}
	config := readBridgeFile(t, client, CodexManagedConfigDir+"/config.toml")
	if !strings.Contains(config, `base_url = "https://enterprise.example/backend-api/codex"`) {
		t.Fatalf("custom OAuth base_url was not preserved:\n%s", config)
	}
}

func TestWriteCodexManagedConfigFileIgnoresAPIKeyLeftoverBaseURL(t *testing.T) {
	t.Parallel()

	client := newTestBridgeClient(t, t.TempDir())
	leftover, err := renderCodexManagedConfig(CodexManagedConfig{
		Mode: SetupModeAPIKey,
		Managed: map[string]string{
			"api_key":  "sk-test",
			"base_url": "https://proxy.example/v1",
		},
	})
	if err != nil {
		t.Fatalf("render API-key leftover config: %v", err)
	}
	if err := client.WriteFile(context.Background(), CodexManagedConfigDir+"/config.toml", leftover); err != nil {
		t.Fatalf("seed API-key leftover config: %v", err)
	}

	// An api_key-mode leftover config must not leak its OpenAI-style URL into
	// an OAuth refresh; the OAuth default applies instead.
	if err := WriteCodexManagedConfigFile(context.Background(), client, CodexManagedConfig{Mode: SetupModeOAuth}); err != nil {
		t.Fatalf("WriteCodexManagedConfigFile() error = %v", err)
	}
	config := readBridgeFile(t, client, CodexManagedConfigDir+"/config.toml")
	if !strings.Contains(config, `base_url = "https://chatgpt.com/backend-api/codex"`) {
		t.Fatalf("OAuth refresh over api_key config should use the default URL:\n%s", config)
	}
}

func readBridgeFile(t *testing.T, client *bridge.Client, path string) string {
	t.Helper()
	rc, err := client.ReadRaw(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadRaw(%s) error = %v", path, err)
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestStartBridgeProcessCanRunWithoutBridgeHardTimeout(t *testing.T) {
	client, server := newRecordingBridgeClient(t)
	proc, err := startBridgeProcess(context.Background(), client, "codex-acp", nil, "/data", time.Minute, processOptions{
		Backend:   WorkspaceBackendContainer,
		AgentID:   "codex",
		SetupMode: SetupModeAPIKey,
		Env:       []string{"TRACE_ID=trace-1"},
		NoTimeout: true,
	})
	if err != nil {
		t.Fatalf("startBridgeProcess() error = %v", err)
	}

	// The exec stream input is sent asynchronously; wait until the bridge
	// recording server has received it before closing the process and
	// reading records.
	server.waitForRecordWithTimeout(t, -1, 2*time.Second)
	_ = proc.Close()

	records := server.records()
	if len(records) < 2 {
		t.Fatalf("records len = %d, want at least command check + process exec: %#v", len(records), records)
	}
	processRecord, ok := findRecordWithTimeout(records, -1)
	if !ok {
		t.Fatalf("expected a record with NoTimeout (-1); got %#v", records)
	}
	if processRecord.Timeout != -1 {
		t.Fatalf("process timeout = %d, want -1 no bridge hard timeout", processRecord.Timeout)
	}
	if strings.Contains(processRecord.Command, "TRACE_ID=trace-1") {
		t.Fatalf("process command leaked env var: %q", processRecord.Command)
	}
	assertEnvHas(t, processRecord.Env, "TRACE_ID=trace-1")
	assertEnvHas(t, processRecord.Env, "HOME=/data")
	assertEnvHas(t, processRecord.Env, "CODEX_HOME="+path.Join(proc.lease.root, "state"))
}

func TestStartBridgeProcessUsesContainerToolkitFallback(t *testing.T) {
	client, server := newRecordingBridgeClient(t)
	server.setExitCode("command -v codex-acp >/dev/null 2>&1", 127)

	proc, err := startBridgeProcess(context.Background(), client, "codex-acp", nil, "/data", time.Minute, processOptions{
		Backend:   WorkspaceBackendContainer,
		AgentID:   "codex",
		SetupMode: SetupModeAPIKey,
	})
	if err != nil {
		t.Fatalf("startBridgeProcess() error = %v", err)
	}
	server.waitForRecordWithTimeout(t, int32(time.Minute.Seconds()), 2*time.Second)
	_ = proc.Close()

	processRecord, ok := findRecordWithTimeout(server.records(), int32(time.Minute.Seconds()))
	if !ok {
		t.Fatalf("missing process exec record: %#v", server.records())
	}
	want := containerToolkitBin + "/codex-acp"
	if processRecord.Command != want {
		t.Fatalf("process command = %q, want %q", processRecord.Command, want)
	}
}

func TestStartBridgeProcessRetriesTransientMissingCommand(t *testing.T) {
	oldWindow := commandResolveWindow
	oldDelay := commandResolveDelay
	commandResolveWindow = time.Second
	commandResolveDelay = time.Millisecond
	t.Cleanup(func() {
		commandResolveWindow = oldWindow
		commandResolveDelay = oldDelay
	})

	client, server := newRecordingBridgeClient(t)
	server.setExitCodes("command -v codex-acp >/dev/null 2>&1", 127, 0)
	server.setExitCode("test -x "+containerToolkitBin+"/codex-acp", 1)

	proc, err := startBridgeProcess(context.Background(), client, "codex-acp", nil, "/data", time.Minute, processOptions{
		Backend:   WorkspaceBackendContainer,
		AgentID:   "codex",
		SetupMode: SetupModeAPIKey,
	})
	if err != nil {
		t.Fatalf("startBridgeProcess() error = %v", err)
	}
	server.waitForRecordWithTimeout(t, int32(time.Minute.Seconds()), 2*time.Second)
	_ = proc.Close()

	var checks int
	for _, record := range server.records() {
		if record.Command == "command -v codex-acp >/dev/null 2>&1" {
			checks++
		}
	}
	if checks < 2 {
		t.Fatalf("command checks = %d, want retry; records=%#v", checks, server.records())
	}
	processRecord, ok := findRecordWithTimeout(server.records(), int32(time.Minute.Seconds()))
	if !ok || processRecord.Command != "codex-acp" {
		t.Fatalf("process record = %#v, ok=%v", processRecord, ok)
	}
}

func TestStartBridgeProcessReportsToolkitFallbackFailure(t *testing.T) {
	oldWindow := commandResolveWindow
	commandResolveWindow = 0
	t.Cleanup(func() { commandResolveWindow = oldWindow })

	client, server := newRecordingBridgeClient(t)
	server.setExitCode("command -v codex-acp >/dev/null 2>&1", 127)
	server.setExitCode("test -x "+containerToolkitBin+"/codex-acp", 1)

	_, err := startBridgeProcess(context.Background(), client, "codex-acp", nil, "/data", time.Minute, processOptions{
		Backend:   WorkspaceBackendContainer,
		AgentID:   "codex",
		SetupMode: SetupModeAPIKey,
	})
	if err == nil {
		t.Fatalf("startBridgeProcess() error = nil, want missing command error")
	}
	msg := err.Error()
	for _, want := range []string{"codex-acp", "workspace PATH", containerToolkitBin} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q missing %q", msg, want)
		}
	}
}

type execRecord struct {
	Command  string
	WorkDir  string
	Env      []string
	CleanEnv bool
	UnsetEnv []string
	Timeout  int32
}

type writeRecord struct {
	Path    string
	Content []byte
}

type recordingFSNode struct {
	isDir     bool
	isSymlink bool
	content   []byte
	modTime   time.Time
}

type recordingBridgeServer struct {
	pb.UnimplementedContainerServiceServer

	mu     sync.Mutex
	execs  []execRecord
	files  []writeRecord
	reads  []string
	exits  map[string]int32
	seqs   map[string][]int32
	stdout map[string]string
	fs     map[string]recordingFSNode
}

func (s *recordingBridgeServer) Exec(stream grpc.BidiStreamingServer[pb.ExecInput, pb.ExecOutput]) error {
	input, err := stream.Recv()
	if err != nil {
		return err
	}
	s.mu.Lock()
	exitCode := s.exits[input.GetCommand()]
	stdout := s.stdout[input.GetCommand()]
	if len(s.seqs[input.GetCommand()]) > 0 {
		exitCode = s.seqs[input.GetCommand()][0]
		s.seqs[input.GetCommand()] = s.seqs[input.GetCommand()][1:]
	}
	s.execs = append(s.execs, execRecord{
		Command:  input.GetCommand(),
		WorkDir:  input.GetWorkDir(),
		Env:      append([]string(nil), input.GetEnv()...),
		CleanEnv: input.GetCleanEnv(),
		UnsetEnv: append([]string(nil), input.GetUnsetEnv()...),
		Timeout:  input.GetTimeoutSeconds(),
	})
	s.mu.Unlock()
	if stdout != "" {
		if err := stream.Send(&pb.ExecOutput{Stream: pb.ExecOutput_STDOUT, Data: []byte(stdout)}); err != nil {
			return err
		}
	}
	if err := stream.Send(&pb.ExecOutput{Stream: pb.ExecOutput_EXIT, ExitCode: exitCode}); err != nil {
		return err
	}
	return nil
}

func (s *recordingBridgeServer) setExitCodes(command string, codes ...int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seqs == nil {
		s.seqs = make(map[string][]int32)
	}
	s.seqs[command] = append([]int32(nil), codes...)
}

func (s *recordingBridgeServer) setExitCode(command string, code int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exits == nil {
		s.exits = make(map[string]int32)
	}
	s.exits[command] = code
}

func (s *recordingBridgeServer) setStdout(command, output string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stdout == nil {
		s.stdout = make(map[string]string)
	}
	s.stdout[command] = output
}

func (s *recordingBridgeServer) WriteFile(_ context.Context, req *pb.WriteFileRequest) (*pb.WriteFileResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeFileLocked(req.GetPath(), req.GetContent())
	s.files = append(s.files, writeRecord{
		Path:    req.GetPath(),
		Content: append([]byte(nil), req.GetContent()...),
	})
	return &pb.WriteFileResponse{}, nil
}

func (s *recordingBridgeServer) ReadFile(_ context.Context, req *pb.ReadFileRequest) (*pb.ReadFileResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, req.GetPath())
	if node, ok := s.fs[path.Clean(req.GetPath())]; ok && !node.isDir {
		return &pb.ReadFileResponse{Content: string(node.content), TotalLines: int32(strings.Count(string(node.content), "\n"))}, nil //nolint:gosec // in-memory test files cannot approach int32 limits.
	}
	return &pb.ReadFileResponse{Content: "recorded input\n", TotalLines: 1}, nil
}

func (s *recordingBridgeServer) Mkdir(_ context.Context, req *pb.MkdirRequest) (*pb.MkdirResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureDirLocked(req.GetPath())
	return &pb.MkdirResponse{}, nil
}

func (s *recordingBridgeServer) ListDir(_ context.Context, req *pb.ListDirRequest) (*pb.ListDirResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := path.Clean(req.GetPath())
	node, ok := s.fs[dir]
	if !ok || !node.isDir {
		return nil, status.Error(codes.NotFound, "not found")
	}
	prefix := strings.TrimSuffix(dir, "/") + "/"
	entries := make([]*pb.FileEntry, 0)
	for filePath, child := range s.fs {
		if filePath == dir || !strings.HasPrefix(filePath, prefix) {
			continue
		}
		rel := strings.TrimPrefix(filePath, prefix)
		if !req.GetRecursive() && strings.Contains(rel, "/") {
			continue
		}
		mode := "-rw-------"
		if child.isSymlink {
			mode = "Lrwxrwxrwx"
		} else if child.isDir {
			mode = "drwx------"
		}
		entries = append(entries, &pb.FileEntry{
			Path:    rel,
			IsDir:   child.isDir,
			Size:    int64(len(child.content)),
			Mode:    mode,
			ModTime: child.modTime.UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].GetPath() < entries[j].GetPath() })
	return &pb.ListDirResponse{Entries: entries, TotalCount: int32(len(entries))}, nil //nolint:gosec // test fixture contains only a bounded handful of entries.
}

func (s *recordingBridgeServer) Stat(_ context.Context, req *pb.StatRequest) (*pb.StatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	filePath := path.Clean(req.GetPath())
	node, ok := s.fs[filePath]
	if !ok {
		return nil, status.Error(codes.NotFound, "not found")
	}
	mode := "-rw-------"
	if node.isSymlink {
		mode = "Lrwxrwxrwx"
	} else if node.isDir {
		mode = "drwx------"
	}
	return &pb.StatResponse{Entry: &pb.FileEntry{
		Path:    path.Base(filePath),
		IsDir:   node.isDir,
		Size:    int64(len(node.content)),
		Mode:    mode,
		ModTime: node.modTime.UTC().Format(time.RFC3339),
	}}, nil
}

func (s *recordingBridgeServer) ReadRaw(req *pb.ReadRawRequest, stream pb.ContainerService_ReadRawServer) error {
	s.mu.Lock()
	node, ok := s.fs[path.Clean(req.GetPath())]
	s.mu.Unlock()
	if !ok || node.isDir {
		return status.Error(codes.NotFound, "not found")
	}
	if len(node.content) == 0 {
		return nil
	}
	return stream.Send(&pb.DataChunk{Data: append([]byte(nil), node.content...)})
}

func (s *recordingBridgeServer) WriteRaw(stream grpc.ClientStreamingServer[pb.WriteRawChunk, pb.WriteRawResponse]) error {
	var filePath string
	var content []byte
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if filePath == "" {
			filePath = chunk.GetPath()
		}
		content = append(content, chunk.GetData()...)
	}
	if strings.TrimSpace(filePath) == "" {
		return status.Error(codes.InvalidArgument, "path is required")
	}
	s.mu.Lock()
	s.writeFileLocked(filePath, content)
	s.files = append(s.files, writeRecord{Path: filePath, Content: append([]byte(nil), content...)})
	s.mu.Unlock()
	return stream.SendAndClose(&pb.WriteRawResponse{BytesWritten: int64(len(content))})
}

func (s *recordingBridgeServer) DeleteFile(_ context.Context, req *pb.DeleteFileRequest) (*pb.DeleteFileResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	target := path.Clean(req.GetPath())
	delete(s.fs, target)
	if req.GetRecursive() {
		prefix := strings.TrimSuffix(target, "/") + "/"
		for filePath := range s.fs {
			if strings.HasPrefix(filePath, prefix) {
				delete(s.fs, filePath)
			}
		}
	}
	return &pb.DeleteFileResponse{}, nil
}

func (s *recordingBridgeServer) ensureDirLocked(dir string) {
	if s.fs == nil {
		s.fs = make(map[string]recordingFSNode)
	}
	dir = path.Clean(dir)
	parts := strings.Split(strings.TrimPrefix(dir, "/"), "/")
	current := "/"
	s.fs[current] = recordingFSNode{isDir: true, modTime: time.Now()}
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		current = path.Join(current, part)
		s.fs[current] = recordingFSNode{isDir: true, modTime: time.Now()}
	}
}

func (s *recordingBridgeServer) writeFileLocked(filePath string, content []byte) {
	filePath = path.Clean(filePath)
	s.ensureDirLocked(path.Dir(filePath))
	s.fs[filePath] = recordingFSNode{content: append([]byte(nil), content...), modTime: time.Now()}
}

func (s *recordingBridgeServer) records() []execRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]execRecord, len(s.execs))
	copy(out, s.execs)
	return out
}

func (s *recordingBridgeServer) writes() []writeRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]writeRecord, len(s.files))
	copy(out, s.files)
	return out
}

func (s *recordingBridgeServer) readPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.reads...)
}

func (s *recordingBridgeServer) exists(filePath string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.fs[path.Clean(filePath)]
	return ok
}

// waitForRecordWithTimeout polls until a record with the given timeout value
// has been recorded, or the deadline elapses. It is used to bridge the gap
// between the async ExecStreamWithEnv input send and the server-side Recv.
func (s *recordingBridgeServer) waitForRecordWithTimeout(t *testing.T, want int32, deadline time.Duration) {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if _, ok := findRecordWithTimeout(s.records(), want); ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func findRecordWithTimeout(records []execRecord, want int32) (execRecord, bool) {
	for _, rec := range records {
		if rec.Timeout == want {
			return rec, true
		}
	}
	return execRecord{}, false
}

func findWrite(writes []writeRecord, path string) (writeRecord, bool) {
	for _, write := range writes {
		if write.Path == path {
			return write, true
		}
	}
	return writeRecord{}, false
}

func newRecordingBridgeClient(t *testing.T) (*bridge.Client, *recordingBridgeServer) {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	recorder := &recordingBridgeServer{fs: map[string]recordingFSNode{
		"/":     {isDir: true, modTime: time.Now()},
		"/data": {isDir: true, modTime: time.Now()},
		"/tmp":  {isDir: true, modTime: time.Now()},
	}}
	pb.RegisterContainerServiceServer(server, recorder)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	conn, err := grpc.NewClient("passthrough:///acpclient-process-test",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return bridge.NewClientFromConn(conn), recorder
}

func assertEnvHas(t *testing.T, env []string, want string) {
	t.Helper()
	for _, item := range env {
		if item == want {
			return
		}
	}
	t.Fatalf("env %v missing %q", env, want)
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}

func envHasKeyValue(env []string, key, value string) bool {
	want := key + "=" + value
	for _, item := range env {
		if item == want {
			return true
		}
	}
	return false
}

func envHasKey(env []string, key string) bool {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return true
		}
	}
	return false
}

func hasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
