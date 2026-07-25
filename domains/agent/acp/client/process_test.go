package client

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/memohai/memoh/domains/runtime/bridge/bridgepb"
	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
)

func TestBuildShellCommandQuotesCommandAndArgs(t *testing.T) {
	got := buildShellCommand("codex-acp", []string{"--flag", "value with spaces", "it's", "$HOME"})
	want := `codex-acp --flag 'value with spaces' 'it'\''s' '$HOME'`
	if got != want {
		t.Fatalf("buildShellCommand() = %q, want %q", got, want)
	}
}

func TestPrepareProcessEnvContainerClaudeWritesManagedSettings(t *testing.T) {
	client, server := newRecordingBridgeClient(t)
	env, cleanup, err := prepareProcessEnv(context.Background(), client, "/data", processOptions{
		Backend:   WorkspaceBackendContainer,
		AgentID:   "claude-code",
		SetupMode: SetupModeOAuth,
		Env:       []string{"CLAUDE_CODE_OAUTH_TOKEN=token"},
	})
	if err != nil {
		t.Fatalf("prepareProcessEnv() error = %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	home := envValue(env, "HOME")
	if home == "" {
		t.Fatalf("container Claude env missing HOME: %v", env)
	}
	settings, ok := findWrite(server.writes(), home+"/.claude/settings.json")
	if !ok {
		t.Fatalf("managed Claude settings were not written: %#v", server.writes())
	}
	// The explicit ask rule is what forces "safe" read-only Bash commands
	// (pwd, ls, ...) through Memoh tool approval instead of the CLI's
	// built-in auto-allow.
	if !strings.Contains(string(settings.Content), `"ask"`) || !strings.Contains(string(settings.Content), `"Bash"`) {
		t.Fatalf("managed Claude settings missing Bash ask rule:\n%s", settings.Content)
	}
}

func TestPrepareProcessEnvContainerCodexWritesNoClaudeSettings(t *testing.T) {
	client, server := newRecordingBridgeClient(t)
	_, cleanup, err := prepareProcessEnv(context.Background(), client, "/data", processOptions{
		Backend:   WorkspaceBackendContainer,
		AgentID:   "codex",
		SetupMode: SetupModeAPIKey,
		Env:       []string{"OPENAI_API_KEY=sk-test"},
	})
	if err != nil {
		t.Fatalf("prepareProcessEnv() error = %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	for _, write := range server.writes() {
		if strings.HasSuffix(write.Path, "/.claude/settings.json") {
			t.Fatalf("Codex setup unexpectedly wrote Claude settings: %#v", server.writes())
		}
	}
}

func TestPrepareProcessEnvContainerAPIKey(t *testing.T) {
	client, server := newRecordingBridgeClient(t)
	env, cleanup, err := prepareProcessEnv(context.Background(), client, "/data", processOptions{
		Backend:   WorkspaceBackendContainer,
		AgentID:   "codex",
		SetupMode: SetupModeAPIKey,
		Env:       []string{"CUSTOM_FLAG=enabled", "HOME=/profile-home", "PATH=/profile-bin"},
	})
	if err != nil {
		t.Fatalf("prepareProcessEnv() error = %v", err)
	}
	if cleanup == nil {
		t.Fatalf("managed cleanup is nil")
	}
	assertEnvHas(t, env, "CUSTOM_FLAG=enabled")
	home := envValue(env, "HOME")
	if home != dataMountPath {
		t.Fatalf("api_key Codex HOME = %q, want %q", home, dataMountPath)
	}
	if got := envValue(env, "PATH"); got != defaultContainerPath {
		t.Fatalf("PATH = %q, want %q", got, defaultContainerPath)
	}
	if envHasKeyValue(env, "HOME", "/profile-home") || envHasKeyValue(env, "PATH", "/profile-bin") {
		t.Fatalf("container runtime HOME/PATH must not be overridden by profile env: %v", env)
	}
	if envHasKey(env, "CODEX_HOME") {
		t.Fatalf("api_key Codex runtime must not set CODEX_HOME: %v", env)
	}
	if writes := server.writes(); len(writes) != 0 {
		t.Fatalf("runtime prepare writes = %#v, want no config writes", writes)
	}
	dirs := server.dirs()
	if len(dirs) != 1 || dirs[0] != dataMountPath {
		t.Fatalf("prepare dirs = %#v, want workspace HOME %q", dirs, dataMountPath)
	}

	records := server.records()
	if len(records) != 0 {
		t.Fatalf("prepare records = %#v, want no shell command for temporary HOME", records)
	}

	cleanup()
	records = server.records()
	if len(records) != 0 {
		t.Fatalf("cleanup records = %#v, want no cleanup for workspace HOME", records)
	}
}

func TestPrepareProcessEnvContainerSelf(t *testing.T) {
	client, server := newRecordingBridgeClient(t)
	env, cleanup, err := prepareProcessEnv(context.Background(), client, "/data", processOptions{
		Backend:   WorkspaceBackendContainer,
		AgentID:   "codex",
		Env:       []string{"HOME=/profile-home", "PATH=/profile-bin"},
		SetupMode: SetupModeSelf,
	})
	if err != nil {
		t.Fatalf("prepareProcessEnv() error = %v", err)
	}
	if cleanup != nil {
		t.Fatalf("self cleanup should be nil")
	}
	if got := envValue(env, "HOME"); got != dataMountPath {
		t.Fatalf("self Codex HOME = %q, want %q", got, dataMountPath)
	}
	if got := envValue(env, "PATH"); got != defaultContainerPath {
		t.Fatalf("self PATH = %q, want %q", got, defaultContainerPath)
	}
	records := server.records()
	if len(records) != 0 {
		t.Fatalf("self records = %#v, want no HOME preparation", records)
	}
	if writes := server.writes(); len(writes) != 0 {
		t.Fatalf("self writes = %#v, want no managed config", writes)
	}
}

func TestPrepareProcessEnvContainerSelfGenericAgentDoesNotOverrideHome(t *testing.T) {
	client, _ := newRecordingBridgeClient(t)
	env, cleanup, err := prepareProcessEnv(context.Background(), client, "/data", processOptions{
		Backend:   WorkspaceBackendContainer,
		AgentID:   "other-acp",
		Env:       []string{"HOME=/profile-home", "PATH=/profile-bin"},
		SetupMode: SetupModeSelf,
	})
	if err != nil {
		t.Fatalf("prepareProcessEnv() error = %v", err)
	}
	if cleanup != nil {
		t.Fatalf("self cleanup should be nil")
	}
	if envHasKey(env, "HOME") {
		t.Fatalf("generic self env must not override workspace HOME: %v", env)
	}
	if got := envValue(env, "PATH"); got != defaultContainerPath {
		t.Fatalf("generic self PATH = %q, want %q", got, defaultContainerPath)
	}
}

func TestPrepareProcessEnvContainerSelfHermesUsesPersistentHome(t *testing.T) {
	client, server := newRecordingBridgeClient(t)
	env, cleanup, err := prepareProcessEnv(context.Background(), client, "/data", processOptions{
		Backend:   WorkspaceBackendContainer,
		AgentID:   "hermes",
		Env:       []string{"HOME=/profile-home", "PATH=/profile-bin"},
		SetupMode: SetupModeSelf,
	})
	if err != nil {
		t.Fatalf("prepareProcessEnv() error = %v", err)
	}
	if cleanup != nil {
		t.Fatalf("self cleanup should be nil")
	}
	if got := envValue(env, "HOME"); got != dataMountPath {
		t.Fatalf("self Hermes HOME = %q, want %q", got, dataMountPath)
	}
	if got := envValue(env, "HERMES_HOME"); got != dataMountPath+"/.hermes" {
		t.Fatalf("self Hermes HERMES_HOME = %q", got)
	}
	if dirs := server.dirs(); len(dirs) != 1 || dirs[0] != dataMountPath+"/.hermes" {
		t.Fatalf("prepare dirs = %#v, want Hermes self home", dirs)
	}
	if writes := server.writes(); len(writes) != 0 {
		t.Fatalf("self writes = %#v, want no managed config", writes)
	}
}

func TestPrepareProcessEnvContainerHermesManagedUsesStableHermesHome(t *testing.T) {
	client, server := newRecordingBridgeClient(t)
	env, cleanup, err := prepareProcessEnv(context.Background(), client, "/data", processOptions{
		Backend:    WorkspaceBackendContainer,
		AgentID:    "hermes",
		SetupMode:  SetupModeAPIKey,
		Env:        []string{"HOME=/tmp/profile", "PATH=/bad", "HERMES_HOME=/host/hermes", "MEMOH_HERMES_API_KEY=sk-host-memoh", "OPENAI_API_KEY=sk-host", "OPENROUTER_API_KEY=sk-host-router", "CUSTOM_FLAG=1"},
		HermesHome: "/data/.memoh-hermes",
	})
	if err != nil {
		t.Fatalf("prepareProcessEnv() error = %v", err)
	}
	if cleanup != nil {
		t.Fatalf("Hermes managed cleanup should be nil")
	}
	if got := envValue(env, "HERMES_HOME"); got != "/data/.memoh-hermes" {
		t.Fatalf("HERMES_HOME = %q, want /data/.memoh-hermes; env=%v", got, env)
	}
	if got := envValue(env, "HOME"); got != dataMountPath {
		t.Fatalf("HOME = %q, want %q", got, dataMountPath)
	}
	if got := envValue(env, "PATH"); got != defaultContainerPath {
		t.Fatalf("PATH = %q, want %q", got, defaultContainerPath)
	}
	if envHasKeyValue(env, "HERMES_HOME", "/host/hermes") {
		t.Fatalf("host HERMES_HOME leaked into env: %v", env)
	}
	if envHasKey(env, "MEMOH_HERMES_API_KEY") || envHasKey(env, "OPENAI_API_KEY") || envHasKey(env, "OPENROUTER_API_KEY") {
		t.Fatalf("host provider key leaked into env: %v", env)
	}
	dirs := server.dirs()
	if len(dirs) != 1 || dirs[0] != "/data/.memoh-hermes" {
		t.Fatalf("prepare dirs = %#v, want Hermes home", dirs)
	}
}

func TestPrepareProcessEnvContainerHermesManagedRequiresHermesHome(t *testing.T) {
	client, _ := newRecordingBridgeClient(t)
	_, _, err := prepareProcessEnv(context.Background(), client, "/data", processOptions{
		Backend:   WorkspaceBackendContainer,
		AgentID:   "hermes",
		SetupMode: SetupModeAPIKey,
	})
	if err == nil || !strings.Contains(err.Error(), "HERMES_HOME") {
		t.Fatalf("prepareProcessEnv() error = %v, want HERMES_HOME isolation error", err)
	}
}

func TestStartBridgeProcessHermesManagedPassesCleanEnvControls(t *testing.T) {
	client, server := newRecordingBridgeClient(t)
	proc, err := startBridgeProcess(context.Background(), client, "hermes-acp", nil, "/data", time.Minute, processOptions{
		Backend:    WorkspaceBackendContainer,
		AgentID:    "hermes",
		SetupMode:  SetupModeAPIKey,
		HermesHome: "/data/.memoh-hermes",
		CleanEnv:   true,
		UnsetEnv:   HermesManagedUnsetEnvKeys(),
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

func TestWriteCodexManagedConfigWritesFixedContainerConfig(t *testing.T) {
	client, server := newRecordingBridgeClient(t)
	err := WriteCodexManagedConfig(context.Background(), client, map[string]string{
		"api_key":  "sk-secret",
		"base_url": "https://proxy.example.com/v1",
	})
	if err != nil {
		t.Fatalf("WriteCodexManagedConfig() error = %v", err)
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
		`model_provider = "OpenAI"`,
		`model_reasoning_effort = "xhigh"`,
		`model_reasoning_summary = "detailed"`,
		`model_supports_reasoning_summaries = true`,
		`hide_agent_reasoning = false`,
		`show_raw_agent_reasoning = false`,
		`[model_providers.OpenAI]`,
		`name = "OpenAI"`,
		`base_url = "https://proxy.example.com/v1"`,
		`wire_api = "responses"`,
		`requires_openai_auth = false`,
		`supports_websockets = false`,
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("Codex config missing %q:\n%s", want, config)
		}
	}
	if strings.Contains(config, "secret") {
		t.Fatalf("Codex config leaked API key:\n%s", config)
	}
	authWrite, ok := findWrite(writes, CodexManagedConfigDir+"/auth.json")
	if !ok {
		t.Fatalf("missing Codex auth.json write: %#v", writes)
	}
	auth := string(authWrite.Content)
	if !strings.Contains(auth, `"OPENAI_API_KEY": "sk-secret"`) {
		t.Fatalf("Codex auth missing API key:\n%s", auth)
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
	if err := WriteCodexManagedConfigWithAuth(context.Background(), client, CodexManagedConfig{
		Mode: SetupModeAPIKey,
		Managed: map[string]string{
			"api_key":  "sk-test",
			"base_url": "https://proxy.example/v1",
		},
	}); err != nil {
		t.Fatalf("WriteCodexManagedConfigWithAuth() error = %v", err)
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
	if envHasKey(processRecord.Env, "CODEX_HOME") {
		t.Fatalf("process env must not set CODEX_HOME: %v", processRecord.Env)
	}
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

type recordingBridgeServer struct {
	bridgepb.UnimplementedContainerServiceServer

	mu     sync.Mutex
	execs  []execRecord
	files  []writeRecord
	reads  []string
	dirs_  []string
	exits  map[string]int32
	seqs   map[string][]int32
	stdout map[string]string
}

func (s *recordingBridgeServer) Exec(stream grpc.BidiStreamingServer[bridgepb.ExecInput, bridgepb.ExecOutput]) error {
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
		if err := stream.Send(&bridgepb.ExecOutput{Stream: bridgepb.ExecOutput_STDOUT, Data: []byte(stdout)}); err != nil {
			return err
		}
	}
	if err := stream.Send(&bridgepb.ExecOutput{Stream: bridgepb.ExecOutput_EXIT, ExitCode: exitCode}); err != nil {
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

func (s *recordingBridgeServer) WriteFile(_ context.Context, req *bridgepb.WriteFileRequest) (*bridgepb.WriteFileResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files = append(s.files, writeRecord{
		Path:    req.GetPath(),
		Content: append([]byte(nil), req.GetContent()...),
	})
	return &bridgepb.WriteFileResponse{}, nil
}

func (s *recordingBridgeServer) ReadFile(_ context.Context, req *bridgepb.ReadFileRequest) (*bridgepb.ReadFileResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, req.GetPath())
	return &bridgepb.ReadFileResponse{Content: "recorded input\n", TotalLines: 1}, nil
}

func (s *recordingBridgeServer) Mkdir(_ context.Context, req *bridgepb.MkdirRequest) (*bridgepb.MkdirResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dirs_ = append(s.dirs_, req.GetPath())
	return &bridgepb.MkdirResponse{}, nil
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

func (s *recordingBridgeServer) dirs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.dirs_))
	copy(out, s.dirs_)
	return out
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
	recorder := &recordingBridgeServer{}
	bridgepb.RegisterContainerServiceServer(server, recorder)
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
