package acp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	sdk "github.com/memohai/twilight-ai/sdk"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	toolapproval "github.com/memohai/memoh/internal/agent/decision/approval"
	"github.com/memohai/memoh/internal/agent/decision/feedback"
	userinput "github.com/memohai/memoh/internal/agent/decision/input"
	"github.com/memohai/memoh/internal/agent/event"
	"github.com/memohai/memoh/internal/agent/runtime/acp/client"
	acpprofile "github.com/memohai/memoh/internal/agent/runtime/acp/profile"
	"github.com/memohai/memoh/internal/agent/sessionmode"
	"github.com/memohai/memoh/internal/bots"
	"github.com/memohai/memoh/internal/config"
	"github.com/memohai/memoh/internal/mcp"
	"github.com/memohai/memoh/internal/runtimefence"
	"github.com/memohai/memoh/internal/workspace/bridge"
	pb "github.com/memohai/memoh/internal/workspace/bridgepb"
	"github.com/memohai/memoh/internal/workspace/bridgesvc"
)

// injectRuntime registers a hand-built handle for tests that exercise
// internal state without booting a real agent process.
func injectRuntime(p *SessionPool, h *runtimeHandle) {
	p.mu.Lock()
	p.runtimes[h.id] = h
	if h.boundSession != "" {
		p.bySession[h.boundSession] = h.id
	}
	p.mu.Unlock()
}

func newFakeScriptPool(t *testing.T) *SessionPool {
	pool, _ := newFakeScriptPoolForBot(t, enabledACPBot("bot-1", "api_key", map[string]any{"api_key": "sk-container-byok"}))
	return pool
}

// Built-in profiles deliberately use the adapter binaries pinned in the
// workspace image. These tests exercise the optional dynamic-launch extension
// itself, so they temporarily opt the Codex profile into that mechanism.
func enableDynamicCodexProfileForTest(t *testing.T) {
	t.Helper()
	original, ok := acpprofile.Lookup(acpprofile.AgentCodexID)
	if !ok {
		t.Fatal("Codex profile is not registered")
	}
	dynamic := original
	dynamic.DynamicCommand = "npx"
	dynamic.DynamicArgs = []string{"-y"}
	dynamic.DynamicPackage = "@agentclientprotocol/codex-acp"
	acpprofile.Register(dynamic)
	t.Cleanup(func() { acpprofile.Register(original) })
}

func newFakeScriptPoolForBot(t *testing.T, bot bots.Bot) (*SessionPool, string) {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatal(err)
	}
	writeSessionPoolFakeAgentScript(t, binDir, "codex-acp")
	writeSessionPoolFakeAgentScript(t, binDir, "hermes-acp")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	runner := client.NewRunner(nil, sessionPoolWorkspace{
		client: newSessionPoolBridgeClient(t, root),
		info: bridge.WorkspaceInfo{
			Backend:        bridge.WorkspaceBackendContainer,
			DefaultWorkDir: root,
		},
	})
	pool := newSessionPool(nil, runner, fakeBotGetter{bot: bot})
	t.Cleanup(pool.CloseAll)
	return pool, root
}

func TestSessionPoolPromptColdStartsBindsAndReuses(t *testing.T) {
	pool := newFakeScriptPool(t)
	pool.timeout = time.Hour

	input := PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		RunID:                 "run-1",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		Prompt:                "first prompt",
		RuntimeOwnerAccountID: "user-1",
		CurrentPlatform:       "web",
	}
	result, err := pool.Prompt(context.Background(), input)
	if err != nil {
		t.Fatalf("Prompt(first) error = %v", err)
	}
	if !strings.Contains(result.Text, "session-pool-ok") {
		t.Fatalf("first result text = %q", result.Text)
	}
	first := pool.sessionHandle("session-1")
	if first == nil || first.session == nil {
		t.Fatalf("cold start did not register a bound runtime")
	}
	if !strings.HasPrefix(first.id, runtimeIDPrefix) {
		t.Fatalf("runtime id = %q, want server-generated %q prefix", first.id, runtimeIDPrefix)
	}
	if first.boundSession != "session-1" {
		t.Fatalf("cold-start runtime bound to %q, want session-1", first.boundSession)
	}
	first.state.Lock()
	activeAfter := first.active
	statusAfter := first.status
	first.state.Unlock()
	if activeAfter != nil || statusAfter != stateIdle {
		t.Fatalf("per-prompt context not cleared after prompt: active=%v status=%q", activeAfter, statusAfter)
	}

	input.Prompt = "second prompt"
	if _, err := pool.Prompt(context.Background(), input); err != nil {
		t.Fatalf("Prompt(second) error = %v", err)
	}
	if got := pool.sessionHandle("session-1"); got != first {
		t.Fatalf("same session started a new runtime")
	}

	input.SessionID = "session-2"
	input.Prompt = "third prompt"
	if _, err := pool.Prompt(context.Background(), input); err != nil {
		t.Fatalf("Prompt(third) error = %v", err)
	}
	if got := pool.sessionHandle("session-2"); got == nil || got == first {
		t.Fatalf("different session did not get an independent runtime")
	}

	status := pool.RuntimeStatus("session-1", "", "")
	if status.State != "idle" || status.ACPSession == "" || status.ProjectPath != "/data/project" || status.RuntimeID != first.id {
		t.Fatalf("RuntimeStatus() = %#v", status)
	}
	if err := pool.CloseSession("session-1"); err != nil {
		t.Fatalf("CloseSession() error = %v", err)
	}
	if pool.sessionHandle("session-1") != nil {
		t.Fatalf("CloseSession did not remove the runtime")
	}
	pool.mu.RLock()
	_, stillRegistered := pool.runtimes[first.id]
	pool.mu.RUnlock()
	if stillRegistered {
		t.Fatalf("CloseSession left the handle registered")
	}
}

func TestSessionPoolPromptForceFreshRuntimeReplacesBoundRuntime(t *testing.T) {
	pool := newFakeScriptPool(t)

	input := PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		Prompt:                "first prompt",
		RuntimeOwnerAccountID: "user-1",
	}
	if _, err := pool.Prompt(context.Background(), input); err != nil {
		t.Fatalf("Prompt(first) error = %v", err)
	}
	first := pool.sessionHandle("session-1")
	if first == nil {
		t.Fatal("first runtime was not registered")
	}

	input.Prompt = "fresh prompt"
	input.ForceFreshRuntime = true
	if _, err := pool.Prompt(context.Background(), input); err != nil {
		t.Fatalf("Prompt(fresh) error = %v", err)
	}
	fresh := pool.sessionHandle("session-1")
	if fresh == nil || fresh == first {
		t.Fatalf("ForceFreshRuntime did not replace the session runtime")
	}
	pool.mu.RLock()
	_, firstStillRegistered := pool.runtimes[first.id]
	pool.mu.RUnlock()
	if firstStillRegistered {
		t.Fatalf("ForceFreshRuntime left the old runtime registered")
	}
}

func TestSessionPoolPromptSupportsImageOnly(t *testing.T) {
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_IMAGE", "1")
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_EXPECT_IMAGE", "1")
	pool := newFakeScriptPool(t)

	result, err := pool.Prompt(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		Images:                []client.PromptImage{{Data: "aW1hZ2U=", MimeType: "image/png"}},
		RuntimeOwnerAccountID: "user-1",
	})
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if !strings.Contains(result.Text, "session-pool-ok") {
		t.Fatalf("result text = %q, want fake agent response", result.Text)
	}
}

func TestSessionPoolPromptKeepsRuntimeWhenImageCapabilityUnsupported(t *testing.T) {
	pool := newFakeScriptPool(t)

	_, err := pool.Prompt(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		Prompt:                "inspect",
		Images:                []client.PromptImage{{Data: "aW1hZ2U=", MimeType: "image/png"}},
		RuntimeOwnerAccountID: "user-1",
	})
	if !errors.Is(err, client.ErrImagePromptUnsupported) {
		t.Fatalf("Prompt() error = %v, want ErrImagePromptUnsupported", err)
	}
	if handle := pool.sessionHandle("session-1"); handle == nil || handle.session == nil {
		t.Fatal("unsupported image prompt tore down a healthy runtime")
	}
}

func TestSessionPoolPromptFallsBackToAttachmentReferenceWhenImageUnsupported(t *testing.T) {
	pool := newFakeScriptPool(t)

	result, err := pool.Prompt(context.Background(), PromptInput{
		BotID:                    "bot-1",
		SessionID:                "session-1",
		AgentID:                  acpprofile.AgentCodexID,
		ProjectPath:              "/data/project",
		Prompt:                   "inspect the image",
		Images:                   []client.PromptImage{{Data: "aW1hZ2U=", MimeType: "image/png"}},
		AttachmentReferences:     []string{"/data/media/aa/image.png"},
		CanFallbackImagesToFiles: true,
		ContextURI:               "memoh://context/current-turn",
		ContextMarkdown:          "Attachment path: /data/media/aa/image.png",
		RuntimeOwnerAccountID:    "user-1",
	})
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if !strings.Contains(result.Text, "session-pool-ok") {
		t.Fatalf("result text = %q, want fake agent response", result.Text)
	}
}

func TestSessionPoolPromptSupportsAttachmentOnly(t *testing.T) {
	pool := newFakeScriptPool(t)

	result, err := pool.Prompt(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		AttachmentReferences:  []string{"/data/media/aa/pasted-text.txt"},
		ContextURI:            "memoh://context/current-turn",
		ContextMarkdown:       "Attachment path: /data/media/aa/pasted-text.txt",
		RuntimeOwnerAccountID: "user-1",
	})
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if !strings.Contains(result.Text, "session-pool-ok") {
		t.Fatalf("result text = %q, want fake agent response", result.Text)
	}
}

func TestSessionPoolRejectsInvalidImageBeforeStartingRuntime(t *testing.T) {
	runner := &recordingRunner{
		info:     bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"},
		startErr: errors.New("runtime should not start"),
	}
	pool := newSessionPool(nil, runner, fakeBotGetter{bot: enabledACPBot("bot-1", "api_key", map[string]any{"api_key": "sk-test"})})

	_, err := pool.Prompt(context.Background(), PromptInput{
		BotID:     "bot-1",
		SessionID: "session-1",
		AgentID:   acpprofile.AgentCodexID,
		Images:    []client.PromptImage{{Data: "not-valid***", MimeType: "image/png"}},
	})
	if !errors.Is(err, client.ErrInvalidPromptImage) {
		t.Fatalf("Prompt() error = %v, want ErrInvalidPromptImage", err)
	}
	if runner.req.AgentID != "" {
		t.Fatalf("runtime was started for invalid input: %#v", runner.req)
	}
}

func TestSessionPoolEnsureStartsRuntimeAndReportsModels(t *testing.T) {
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_MODELS", "1")
	pool := newFakeScriptPool(t)

	status, err := pool.Ensure(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		RuntimeOwnerAccountID: "user-1",
	})
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if status.State != "idle" || status.ACPSession == "" {
		t.Fatalf("Ensure() status = %#v, want idle runtime with ACP session id", status)
	}
	if !strings.HasPrefix(status.RuntimeID, runtimeIDPrefix) || status.SessionID != "session-1" {
		t.Fatalf("Ensure() identity = %#v, want bound server-generated runtime", status)
	}
	if status.Models == nil || !status.Models.Supported || status.Models.CurrentModelID != "gpt-5.1-codex" {
		t.Fatalf("Ensure() models = %#v, want protocol model state", status.Models)
	}
	if len(status.Models.Available) != 2 || status.Models.Available[0].ID != "gpt-5.1-codex" || status.Models.Available[1].ID != "gpt-5.1-codex-high" {
		t.Fatalf("Ensure() available models = %#v", status.Models.Available)
	}
	if status.DefaultModelID != "gpt-5.1-codex" {
		t.Fatalf("Ensure() default model = %q, want startup model", status.DefaultModelID)
	}
}

func TestSessionPoolStartRuntimeReconcilesManagedCodexAPIKeyConfig(t *testing.T) {
	pool, root := newFakeScriptPoolForBot(t, enabledACPBot("bot-1", "api_key", map[string]any{
		"api_key":  "sk-container-byok",
		"base_url": "https://proxy.example.com/v1",
	}))

	if _, err := pool.Ensure(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		RuntimeOwnerAccountID: "user-1",
	}); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	config := readSessionPoolFile(t, root, ".codex", "config.toml")
	for _, want := range []string{
		`model_provider = "OpenAI"`,
		`model_reasoning_summary = "detailed"`,
		`hide_agent_reasoning = false`,
		`show_raw_agent_reasoning = false`,
		`base_url = "https://proxy.example.com/v1"`,
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("Codex config missing %q:\n%s", want, config)
		}
	}
	auth := readSessionPoolFile(t, root, ".codex", "auth.json")
	if !strings.Contains(auth, `"OPENAI_API_KEY": "sk-container-byok"`) {
		t.Fatalf("Codex auth missing managed key:\n%s", auth)
	}
}

func TestSessionPoolStartRuntimeReconcilesCodexOAuthConfigWithoutOverwritingAuth(t *testing.T) {
	pool, root := newFakeScriptPoolForBot(t, enabledACPBot("bot-1", "oauth", nil))
	authPath := filepath.Join(root, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(authPath), 0o750); err != nil {
		t.Fatal(err)
	}
	const existingAuth = `{"auth_mode":"chatgpt","tokens":{"id_token":"id.jwt.token","access_token":"access.jwt.token","refresh_token":"refresh-token","account_id":"account-123"}}`
	if err := os.WriteFile(authPath, []byte(existingAuth), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Ensure(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		RuntimeOwnerAccountID: "user-1",
	}); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	config := readSessionPoolFile(t, root, ".codex", "config.toml")
	for _, want := range []string{
		`model_provider = "chatgpt-http"`,
		`model_reasoning_summary = "detailed"`,
		`hide_agent_reasoning = false`,
		`show_raw_agent_reasoning = false`,
		`requires_openai_auth = true`,
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("Codex OAuth config missing %q:\n%s", want, config)
		}
	}
	if got := readSessionPoolFile(t, root, ".codex", "auth.json"); got != existingAuth {
		t.Fatalf("OAuth auth.json was overwritten:\n%s", got)
	}
}

func TestSessionPoolCreateRuntimeGeneratesIDAndReportsModels(t *testing.T) {
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_MODELS", "1")
	pool := newFakeScriptPool(t)

	status, err := pool.CreateRuntime(context.Background(), CreateRuntimeInput{
		BotID:                 "bot-1",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		RuntimeOwnerAccountID: "user-1",
	})
	if err != nil {
		t.Fatalf("CreateRuntime() error = %v", err)
	}
	if !strings.HasPrefix(status.RuntimeID, runtimeIDPrefix) {
		t.Fatalf("runtime id = %q, want server-generated %q prefix", status.RuntimeID, runtimeIDPrefix)
	}
	if status.SessionID != "" {
		t.Fatalf("fresh runtime should be unbound, got session %q", status.SessionID)
	}
	if status.State != "idle" || status.Models == nil || status.Models.CurrentModelID != "gpt-5.1-codex" {
		t.Fatalf("CreateRuntime() status = %#v", status)
	}
	if status.DefaultModelID != "gpt-5.1-codex" {
		t.Fatalf("default model = %q", status.DefaultModelID)
	}

	got, err := pool.RuntimeStatusByID("bot-1", status.RuntimeID)
	if err != nil {
		t.Fatalf("RuntimeStatusByID() error = %v", err)
	}
	if got.RuntimeID != status.RuntimeID || got.ACPSession == "" {
		t.Fatalf("RuntimeStatusByID() = %#v", got)
	}
}

func TestSessionPoolBindRuntimeAttachesWarmProcessToSession(t *testing.T) {
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_MODELS", "1")
	pool := newFakeScriptPool(t)

	created, err := pool.CreateRuntime(context.Background(), CreateRuntimeInput{
		BotID:                 "bot-1",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		RuntimeOwnerAccountID: "user-1",
	})
	if err != nil {
		t.Fatalf("CreateRuntime() error = %v", err)
	}
	if _, err := pool.SetRuntimeModel(context.Background(), "bot-1", created.RuntimeID, "gpt-5.1-codex-high"); err != nil {
		t.Fatalf("SetRuntimeModel() error = %v", err)
	}

	if err := pool.BindRuntime("bot-1", created.RuntimeID, "session-1", acpprofile.AgentCodexID, "/data/project", "user-1"); err != nil {
		t.Fatalf("BindRuntime() error = %v", err)
	}
	h := pool.sessionHandle("session-1")
	if h == nil || h.id != created.RuntimeID {
		t.Fatalf("session index does not point at the bound runtime")
	}

	// The bound session reuses the warm process - including its model.
	status, err := pool.Ensure(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		RuntimeOwnerAccountID: "user-1",
	})
	if err != nil {
		t.Fatalf("Ensure(bound) error = %v", err)
	}
	if status.RuntimeID != created.RuntimeID {
		t.Fatalf("Ensure started a new runtime %q, want bound %q", status.RuntimeID, created.RuntimeID)
	}
	if status.Models == nil || status.Models.CurrentModelID != "gpt-5.1-codex-high" {
		t.Fatalf("bound runtime lost its model: %#v", status.Models)
	}
	if status.DefaultModelID != "gpt-5.1-codex" {
		t.Fatalf("default model = %q, want startup default", status.DefaultModelID)
	}

	// A bound runtime cannot be bound again.
	if err := pool.BindRuntime("bot-1", created.RuntimeID, "session-2", acpprofile.AgentCodexID, "/data/project", "user-1"); !errors.Is(err, ErrRuntimeBindRejected) {
		t.Fatalf("second BindRuntime() error = %v, want ErrRuntimeBindRejected", err)
	}
}

func TestSessionPoolSetRuntimeModelEmptyResetsToDefault(t *testing.T) {
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_MODELS", "1")
	pool := newFakeScriptPool(t)

	created, err := pool.CreateRuntime(context.Background(), CreateRuntimeInput{
		BotID:                 "bot-1",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		RuntimeOwnerAccountID: "user-1",
	})
	if err != nil {
		t.Fatalf("CreateRuntime() error = %v", err)
	}
	status, err := pool.SetRuntimeModel(context.Background(), "bot-1", created.RuntimeID, "gpt-5.1-codex-high")
	if err != nil {
		t.Fatalf("SetRuntimeModel(high) error = %v", err)
	}
	if status.Models == nil || status.Models.CurrentModelID != "gpt-5.1-codex-high" {
		t.Fatalf("model after set = %#v", status.Models)
	}

	status, err = pool.SetRuntimeModel(context.Background(), "bot-1", created.RuntimeID, "")
	if err != nil {
		t.Fatalf("SetRuntimeModel(reset) error = %v", err)
	}
	if status.Models == nil || status.Models.CurrentModelID != "gpt-5.1-codex" {
		t.Fatalf("model after reset = %#v, want startup default", status.Models)
	}
}

func TestSessionPoolSetRuntimeReasoningUpdatesEffort(t *testing.T) {
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_REASONING", "1")
	pool := newFakeScriptPool(t)

	created, err := pool.CreateRuntime(context.Background(), CreateRuntimeInput{
		BotID:                 "bot-1",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		RuntimeOwnerAccountID: "user-1",
	})
	if err != nil {
		t.Fatalf("CreateRuntime() error = %v", err)
	}
	status, err := pool.SetRuntimeReasoning(context.Background(), "bot-1", created.RuntimeID, "low")
	if err != nil {
		t.Fatalf("SetRuntimeReasoning() error = %v", err)
	}
	if status.Reasoning == nil || status.Reasoning.CurrentEffort != "low" {
		t.Fatalf("reasoning after set = %#v", status.Reasoning)
	}
}

func TestSessionPoolBindRuntimeRejectsMismatches(t *testing.T) {
	pool := newSessionPool(nil, nil, nil)
	live := &client.Session{}
	pending := &runtimeHandle{
		id:                    newRuntimeID(),
		botID:                 "bot-2",
		agentID:               acpprofile.AgentCodexID,
		projectPath:           "/data",
		runtimeOwnerAccountID: "user-1",
		session:               live,
		status:                stateIdle,
		lastActive:            time.Now(),
	}
	injectRuntime(pool, pending)

	cases := []struct {
		name                          string
		botID, sessionID, agent, path string
		wantErr                       error
	}{
		{"cross bot", "bot-1", "real", acpprofile.AgentCodexID, "/data", ErrRuntimeNotFound},
		{"wrong agent", "bot-2", "real", acpprofile.AgentClaudeCodeID, "/data", ErrRuntimeBindRejected},
		{"wrong project", "bot-2", "real", acpprofile.AgentCodexID, "/other", ErrRuntimeBindRejected},
	}
	for _, tc := range cases {
		if err := pool.BindRuntime(tc.botID, pending.id, tc.sessionID, tc.agent, tc.path, "user-1"); !errors.Is(err, tc.wantErr) {
			t.Fatalf("%s: BindRuntime() error = %v, want %v", tc.name, err, tc.wantErr)
		}
	}
	if err := pool.BindRuntime("bot-2", "rt_missing", "real", acpprofile.AgentCodexID, "/data", "user-1"); !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("missing runtime: BindRuntime() error = %v, want ErrRuntimeNotFound", err)
	}

	// Session already served by another runtime.
	other := &runtimeHandle{id: newRuntimeID(), botID: "bot-2", boundSession: "real", status: stateIdle}
	injectRuntime(pool, other)
	if err := pool.BindRuntime("bot-2", pending.id, "real", acpprofile.AgentCodexID, "/data", "user-1"); !errors.Is(err, ErrRuntimeBindRejected) {
		t.Fatalf("occupied session: BindRuntime() error = %v, want ErrRuntimeBindRejected", err)
	}

	// A still-starting runtime (no live process yet) is not bindable.
	starting := &runtimeHandle{id: newRuntimeID(), botID: "bot-2", agentID: acpprofile.AgentCodexID, projectPath: "/data", status: stateStarting}
	injectRuntime(pool, starting)
	if err := pool.BindRuntime("bot-2", starting.id, "real-2", acpprofile.AgentCodexID, "/data", "user-1"); !errors.Is(err, ErrRuntimeBindRejected) {
		t.Fatalf("starting runtime: BindRuntime() error = %v, want ErrRuntimeBindRejected", err)
	}

	// Everything matching succeeds.
	if err := pool.BindRuntime("bot-2", pending.id, "real-2", acpprofile.AgentCodexID, "/data", "user-1"); err != nil {
		t.Fatalf("matching BindRuntime() error = %v", err)
	}
	if pool.sessionHandle("real-2") != pending {
		t.Fatalf("bound session does not resolve to the runtime")
	}
}

func TestSessionPoolOwnedGateHasZeroSideEffectsAcrossBots(t *testing.T) {
	pool := newSessionPool(nil, nil, nil)
	foreign := &runtimeHandle{
		id:           newRuntimeID(),
		botID:        "bot-2",
		agentID:      acpprofile.AgentCodexID,
		projectPath:  "/data",
		session:      &client.Session{},
		status:       stateIdle,
		lastActive:   time.Now(),
		boundSession: "their-session",
	}
	injectRuntime(pool, foreign)

	if _, err := pool.RuntimeStatusByID("bot-1", foreign.id); !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("RuntimeStatusByID(cross bot) error = %v, want ErrRuntimeNotFound", err)
	}
	if _, err := pool.SetRuntimeModel(context.Background(), "bot-1", foreign.id, "gpt-5.1-codex"); !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("SetRuntimeModel(cross bot) error = %v, want ErrRuntimeNotFound", err)
	}
	if err := pool.CloseRuntime("bot-1", foreign.id); !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("CloseRuntime(cross bot) error = %v, want ErrRuntimeNotFound", err)
	}
	if err := pool.BindRuntime("bot-1", foreign.id, "my-session", acpprofile.AgentCodexID, "/data", "user-1"); !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("BindRuntime(cross bot) error = %v, want ErrRuntimeNotFound", err)
	}
	if _, ok := pool.ResolveRuntimeToolContext("bot-1", foreign.id, "runtime-token-1"); ok {
		t.Fatalf("ResolveRuntimeToolContext(cross bot) resolved")
	}

	// Zero side effects: the foreign runtime is fully intact.
	pool.mu.RLock()
	registered := pool.runtimes[foreign.id] == foreign
	indexed := pool.bySession["their-session"] == foreign.id
	pool.mu.RUnlock()
	foreign.state.Lock()
	untouched := !foreign.closed && foreign.session != nil && foreign.status == stateIdle
	foreign.state.Unlock()
	if !registered || !indexed || !untouched {
		t.Fatalf("cross-bot operations disturbed the runtime: registered=%v indexed=%v untouched=%v", registered, indexed, untouched)
	}

	// The owner can close it.
	if err := pool.CloseRuntime("bot-2", foreign.id); err != nil {
		t.Fatalf("CloseRuntime(owner) error = %v", err)
	}
	if pool.sessionHandle("their-session") != nil {
		t.Fatalf("owner close left the session index entry")
	}
}

func TestSessionPoolCloseBotAgentRuntimesDoesNotWaitForActivePrompt(t *testing.T) {
	pool := newSessionPool(nil, nil, nil)
	active := &runtimeHandle{
		id:           newRuntimeID(),
		botID:        "bot-1",
		agentID:      acpprofile.AgentHermesID,
		projectPath:  "/data",
		session:      &client.Session{},
		status:       stateActive,
		lastActive:   time.Now(),
		boundSession: "session-1",
		active: &client.ToolSessionContext{
			BotID:     "bot-1",
			SessionID: "session-1",
		},
	}
	injectRuntime(pool, active)
	active.op.Lock()
	defer active.op.Unlock()

	done := make(chan error, 1)
	go func() {
		done <- pool.CloseBotAgentRuntimes("bot-1", acpprofile.AgentHermesID)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("CloseBotAgentRuntimes() error = %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("CloseBotAgentRuntimes waited for the active prompt op lock")
	}

	active.state.Lock()
	closed := active.closed
	active.state.Unlock()
	if !closed {
		t.Fatal("runtime was not marked closed")
	}
	if got := pool.sessionHandle("session-1"); got != nil {
		t.Fatalf("session index still points at closed runtime: %#v", got)
	}
}

func TestSessionPoolUnboundCapEvictsOldestIdle(t *testing.T) {
	pool := newFakeScriptPool(t)

	now := time.Now()
	for i := 0; i < maxUnboundRuntimesPerBot; i++ {
		injectRuntime(pool, &runtimeHandle{
			id:         fmt.Sprintf("rt_old-%d", i),
			botID:      "bot-1",
			agentID:    acpprofile.AgentCodexID,
			status:     stateIdle,
			lastActive: now.Add(-time.Duration(i+1) * time.Minute),
		})
	}
	// Bound and other-bot runtimes must not count toward the cap.
	injectRuntime(pool, &runtimeHandle{id: "rt_bound", botID: "bot-1", boundSession: "session-9", status: stateIdle, lastActive: now})
	injectRuntime(pool, &runtimeHandle{id: "rt_other-bot", botID: "bot-9", status: stateIdle, lastActive: now.Add(-time.Minute)})

	created, err := pool.CreateRuntime(context.Background(), CreateRuntimeInput{
		BotID:                 "bot-1",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		RuntimeOwnerAccountID: "user-1",
	})
	if err != nil {
		t.Fatalf("CreateRuntime() error = %v", err)
	}

	pool.mu.RLock()
	_, oldestAlive := pool.runtimes[fmt.Sprintf("rt_old-%d", maxUnboundRuntimesPerBot-1)]
	_, newAlive := pool.runtimes[created.RuntimeID]
	_, boundAlive := pool.runtimes["rt_bound"]
	_, otherAlive := pool.runtimes["rt_other-bot"]
	survivors := 0
	for i := 0; i < maxUnboundRuntimesPerBot-1; i++ {
		if _, ok := pool.runtimes[fmt.Sprintf("rt_old-%d", i)]; ok {
			survivors++
		}
	}
	pool.mu.RUnlock()
	if oldestAlive {
		t.Fatalf("oldest idle unbound runtime should be evicted")
	}
	if !newAlive || !boundAlive || !otherAlive || survivors != maxUnboundRuntimesPerBot-1 {
		t.Fatalf("eviction touched the wrong runtimes: new=%v bound=%v other=%v survivors=%d", newAlive, boundAlive, otherAlive, survivors)
	}
}

func TestSessionPoolUnboundCapErrorsWhenAllBusy(t *testing.T) {
	pool := newSessionPool(nil, &recordingRunner{
		info: bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"},
	}, fakeBotGetter{bot: enabledACPBot("bot-1", "api_key", map[string]any{"api_key": "sk-test"})})
	for i := 0; i < maxUnboundRuntimesPerBot; i++ {
		injectRuntime(pool, &runtimeHandle{
			id:         fmt.Sprintf("rt_busy-%d", i),
			botID:      "bot-1",
			status:     stateActive,
			lastActive: time.Now(),
		})
	}

	_, err := pool.CreateRuntime(context.Background(), CreateRuntimeInput{
		BotID:                 "bot-1",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		RuntimeOwnerAccountID: "user-1",
	})
	if !errors.Is(err, ErrTooManyRuntimes) {
		t.Fatalf("CreateRuntime() error = %v, want ErrTooManyRuntimes", err)
	}
	pool.mu.RLock()
	count := len(pool.runtimes)
	pool.mu.RUnlock()
	if count != maxUnboundRuntimesPerBot {
		t.Fatalf("capped create registered a runtime: %d handles", count)
	}
}

func TestSessionPoolEnsureReplacesMismatchedAgentRuntimeWithoutDeadlock(t *testing.T) {
	pool := newFakeScriptPool(t)

	// A stale bound runtime whose agent differs forces the replace path,
	// which formerly deadlocked on the per-session lock.
	injectRuntime(pool, &runtimeHandle{
		id:           newRuntimeID(),
		botID:        "bot-1",
		agentID:      acpprofile.AgentClaudeCodeID,
		projectPath:  "/data/project",
		status:       stateIdle,
		lastActive:   time.Now(),
		boundSession: "session-x",
		session:      &client.Session{},
	})

	done := make(chan error, 1)
	go func() {
		_, err := pool.Ensure(context.Background(), PromptInput{
			BotID:                 "bot-1",
			SessionID:             "session-x",
			AgentID:               acpprofile.AgentCodexID,
			ProjectPath:           "/data/project",
			RuntimeOwnerAccountID: "user-1",
		})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Ensure() error = %v", err)
		}
	case <-time.After(time.Minute):
		t.Fatal("Ensure() deadlocked while replacing a mismatched runtime")
	}
	replaced := pool.sessionHandle("session-x")
	if replaced == nil || replaced.session == nil || replaced.agentID != acpprofile.AgentCodexID {
		t.Fatalf("replaced runtime = %#v, want fresh codex runtime", replaced)
	}
}

func TestSessionPoolSetModelUpdatesRuntimeModel(t *testing.T) {
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_MODELS", "1")
	pool := newFakeScriptPool(t)

	status, err := pool.SetModel(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		RuntimeOwnerAccountID: "user-1",
	}, "gpt-5.1-codex-high")
	if err != nil {
		t.Fatalf("SetModel() error = %v", err)
	}
	if status.State != "idle" || status.ACPSession == "" {
		t.Fatalf("SetModel() status = %#v, want idle runtime with ACP session id", status)
	}
	if status.Models == nil || !status.Models.Supported || status.Models.CurrentModelID != "gpt-5.1-codex-high" {
		t.Fatalf("SetModel() models = %#v, want selected model", status.Models)
	}
}

func TestSessionPoolSetReasoningUpdatesRuntimeEffort(t *testing.T) {
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_REASONING", "1")
	pool := newFakeScriptPool(t)

	status, err := pool.SetReasoning(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		RuntimeOwnerAccountID: "user-1",
	}, "low")
	if err != nil {
		t.Fatalf("SetReasoning() error = %v", err)
	}
	if status.State != "idle" || status.Reasoning == nil || status.Reasoning.CurrentEffort != "low" {
		t.Fatalf("SetReasoning() status = %#v", status)
	}
}

func TestSessionPoolPromptAppliesModelThenReasoningAndSkipsMatchingValues(t *testing.T) {
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_MODELS", "1")
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_REASONING", "1")
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_MODEL_RESETS_REASONING", "1")
	configLog := filepath.Join(t.TempDir(), "config.log")
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_CONFIG_LOG", configLog)
	pool := newFakeScriptPool(t)

	input := PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		ModelID:               "gpt-5.1-codex-high",
		ReasoningEffort:       "xhigh",
		Prompt:                "first",
		RuntimeOwnerAccountID: "user-1",
	}
	if _, err := pool.Prompt(context.Background(), input); err != nil {
		t.Fatalf("Prompt(first) error = %v", err)
	}
	lines := nonEmptyLines(readOptionalFile(t, configLog))
	if len(lines) < 3 {
		t.Fatalf("first turn config log = %#v, want model, reasoning, and prompt entries", lines)
	}
	if got, want := lines[len(lines)-3:], []string{
		"config:model=gpt-5.1-codex-high",
		"config:thinking=xhigh",
		"prompt:model=gpt-5.1-codex-high,reasoning=xhigh",
	}; !equalStrings(got, want) {
		t.Fatalf("first turn config log = %#v, want suffix %#v (all %#v)", got, want, lines)
	}

	if err := os.Truncate(configLog, 0); err != nil {
		t.Fatal(err)
	}
	input.Prompt = "same config"
	if _, err := pool.Prompt(context.Background(), input); err != nil {
		t.Fatalf("Prompt(same config) error = %v", err)
	}
	if got, want := nonEmptyLines(readOptionalFile(t, configLog)), []string{
		"prompt:model=gpt-5.1-codex-high,reasoning=xhigh",
	}; !equalStrings(got, want) {
		t.Fatalf("matching turn config log = %#v, want %#v", got, want)
	}

	if err := os.Truncate(configLog, 0); err != nil {
		t.Fatal(err)
	}
	input.Prompt = "reasoning only"
	input.ReasoningEffort = "low"
	if _, err := pool.Prompt(context.Background(), input); err != nil {
		t.Fatalf("Prompt(reasoning only) error = %v", err)
	}
	if got, want := nonEmptyLines(readOptionalFile(t, configLog)), []string{
		"config:thinking=low",
		"prompt:model=gpt-5.1-codex-high,reasoning=low",
	}; !equalStrings(got, want) {
		t.Fatalf("reasoning-only config log = %#v, want %#v", got, want)
	}
}

func TestSessionPoolPromptRejectsUnavailableTurnConfigWithoutDroppingRuntime(t *testing.T) {
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_MODELS", "1")
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_REASONING", "1")
	pool := newFakeScriptPool(t)

	input := PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		ReasoningEffort:       "ultra",
		Prompt:                "invalid config",
		RuntimeOwnerAccountID: "user-1",
	}
	_, err := pool.Prompt(context.Background(), input)
	if !errors.Is(err, client.ErrReasoningEffortUnavailable) {
		t.Fatalf("Prompt() error = %v, want ErrReasoningEffortUnavailable", err)
	}
	h := pool.sessionHandle("session-1")
	if h == nil || h.session == nil || h.closed {
		t.Fatalf("validation failure dropped reusable runtime: %#v", h)
	}
}

func TestSessionPoolModelTransportFailureDropsUncertainRuntime(t *testing.T) {
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_MODELS", "1")
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_CONFIG_FAIL", "model")
	pool := newFakeScriptPool(t)

	created, err := pool.CreateRuntime(context.Background(), CreateRuntimeInput{
		BotID:                 "bot-1",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		RuntimeOwnerAccountID: "user-1",
	})
	if err != nil {
		t.Fatalf("CreateRuntime() error = %v", err)
	}
	_, err = pool.SetRuntimeModel(context.Background(), "bot-1", created.RuntimeID, "gpt-5.1-codex-high")
	if !errors.Is(err, ErrRuntimeConfigUpdateFailed) {
		t.Fatalf("SetRuntimeModel() error = %v, want ErrRuntimeConfigUpdateFailed", err)
	}
	if _, err := pool.RuntimeStatusByID("bot-1", created.RuntimeID); !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("RuntimeStatusByID() error = %v, want dropped runtime", err)
	}
}

func TestSessionPoolAbortedPromptConfigApplyKeepsRuntime(t *testing.T) {
	pool := newSessionPool(nil, nil, nil)
	h := &runtimeHandle{
		id:           newRuntimeID(),
		botID:        "bot-1",
		agentID:      acpprofile.AgentCodexID,
		status:       stateIdle,
		lastActive:   time.Now(),
		boundSession: "session-1",
		session:      &client.Session{},
	}
	injectRuntime(pool, h)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, retry, err := pool.promptOnHandle(ctx, h, PromptInput{
		BotID:     "bot-1",
		SessionID: "session-1",
		ModelID:   "model-b",
		Prompt:    "hello",
	})
	if retry || err == nil {
		t.Fatalf("promptOnHandle() = retry %v err %v, want config error without retry", retry, err)
	}
	if errors.Is(err, ErrRuntimeConfigUpdateFailed) {
		t.Fatalf("promptOnHandle() error = %v, want cancellation kept out of the teardown contract", err)
	}
	if h.closed || h.session == nil {
		t.Fatalf("aborted per-turn config apply dropped runtime: handle=%#v", h)
	}
	if _, err := pool.RuntimeStatusByID("bot-1", h.id); err != nil {
		t.Fatalf("RuntimeStatusByID() error = %v, want reusable runtime", err)
	}
}

func TestSessionPoolCanceledConfigUpdateKeepsRuntime(t *testing.T) {
	pool := newSessionPool(nil, nil, nil)
	h := &runtimeHandle{
		id:           newRuntimeID(),
		botID:        "bot-1",
		agentID:      acpprofile.AgentCodexID,
		status:       stateIdle,
		lastActive:   time.Now(),
		boundSession: "session-1",
		session:      &client.Session{},
	}
	injectRuntime(pool, h)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := pool.updateConfigOnHandle(
		ctx,
		h,
		func(*client.Session) bool { return false },
		func(ctx context.Context, _ *client.Session) error { return ctx.Err() },
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("updateConfigOnHandle() error = %v, want context.Canceled", err)
	}
	status, err := pool.RuntimeStatusByID("bot-1", h.id)
	if err != nil {
		t.Fatalf("RuntimeStatusByID() error = %v, want reusable runtime", err)
	}
	if h.closed || h.session == nil || status.State != stateIdle {
		t.Fatalf("canceled config update dropped runtime: handle=%#v status=%#v", h, status)
	}
}

func nonEmptyLines(value string) []string {
	var out []string
	for _, line := range strings.Split(value, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func readOptionalFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // reads a path created under t.TempDir.
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func TestSessionPoolRuntimeStatusReportsActiveDuringColdStart(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	runner := &blockingRunner{
		info:    bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"},
		started: started,
		release: release,
	}
	pool := newSessionPool(
		nil,
		runner,
		fakeBotGetter{bot: enabledACPBot("bot-1", "api_key", map[string]any{"api_key": "sk-test"})},
	)

	errCh := make(chan error, 1)
	go func() {
		_, err := pool.Prompt(context.Background(), PromptInput{
			BotID:                 "bot-1",
			SessionID:             "session-1",
			AgentID:               "codex",
			ProjectPath:           "/data/project",
			Prompt:                "run",
			RuntimeOwnerAccountID: "user-1",
		})
		errCh <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not start")
	}

	status := pool.RuntimeStatus("session-1", "", "")
	if status.State != "active" || status.ACPSession != "" {
		t.Fatalf("RuntimeStatus during cold start = %#v, want active without ACP session id", status)
	}

	close(release)
	if err := <-errCh; err == nil || err.Error() != "released" {
		t.Fatalf("Prompt() error = %v, want released", err)
	}
	status = pool.RuntimeStatus("session-1", "codex", "/data/project")
	if status.State != "idle" || status.ACPSession != "" {
		t.Fatalf("RuntimeStatus after failed start = %#v, want idle without process", status)
	}
}

func TestSessionPoolPinsExactAdapterVersionForProcess(t *testing.T) {
	enableDynamicCodexProfileForTest(t)
	runner := &dynamicRecordingRunner{
		info:     bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"},
		versions: []string{"1.2.0"},
		starts: []dynamicStartResult{
			{session: &client.Session{}},
			{session: &client.Session{}},
		},
	}
	pool := newSessionPool(
		nil,
		runner,
		fakeBotGetter{bot: enabledACPBot("bot-1", "api_key", map[string]any{"api_key": "sk-test"})},
	)
	ensure := func(sessionID string) RuntimeStatus {
		t.Helper()
		status, err := pool.Ensure(context.Background(), PromptInput{
			BotID:                 "bot-1",
			SessionID:             sessionID,
			AgentID:               acpprofile.AgentCodexID,
			ProjectPath:           "/data/project",
			RuntimeOwnerAccountID: "user-1",
		})
		if err != nil {
			t.Fatalf("Ensure(%s) error = %v", sessionID, err)
		}
		return status
	}

	if status := ensure("session-1"); status.State != stateIdle {
		t.Fatalf("first status = %#v", status)
	}
	if got := runner.resolveCallCount(); got != 1 {
		t.Fatalf("version lookups after first cold start = %d, want 1", got)
	}
	requests := runner.requests()
	if len(requests) != 1 || len(requests[0].Args) != 2 || requests[0].Args[1] != "@agentclientprotocol/codex-acp@1.2.0" {
		t.Fatalf("first dynamic request = %#v", requests)
	}

	ensure("session-2")
	if got := runner.resolveCallCount(); got != 1 {
		t.Fatalf("version lookups after second cold start = %d, want 1", got)
	}
	requests = runner.requests()
	if len(requests) != 2 || requests[1].Args[1] != "@agentclientprotocol/codex-acp@1.2.0" {
		t.Fatalf("cached exact-version request = %#v", requests)
	}
}

func TestSessionPoolPinsAdapterVersionsPerBot(t *testing.T) {
	runner := &dynamicRecordingRunner{versions: []string{"1.2.0", "1.3.0"}}
	pool := newSessionPool(nil, runner, fakeBotGetter{})
	const packageName = "@agentclientprotocol/codex-acp"

	for _, tc := range []struct {
		botID string
		want  string
	}{
		{botID: "bot-1", want: "1.2.0"},
		{botID: "bot-2", want: "1.3.0"},
	} {
		version, err := resolveAdapterVersionForTest(pool, tc.botID, packageName)
		if err != nil {
			t.Fatalf("resolveDynamicAdapter(%s) error = %v", tc.botID, err)
		}
		if version != tc.want {
			t.Fatalf("resolveDynamicAdapter(%s) = %q, want %q", tc.botID, version, tc.want)
		}
	}

	for _, tc := range []struct {
		botID string
		want  string
	}{
		{botID: "bot-1", want: "1.2.0"},
		{botID: "bot-2", want: "1.3.0"},
	} {
		version, err := resolveAdapterVersionForTest(pool, tc.botID, packageName)
		if err != nil || version != tc.want {
			t.Fatalf("cached resolveDynamicAdapter(%s) = %q, %v; want %q, nil", tc.botID, version, err, tc.want)
		}
	}
	if got := runner.resolveCallCount(); got != 2 {
		t.Fatalf("version lookups = %d, want one per bot", got)
	}
}

func TestSessionPoolSharesAdapterLookupAcrossConcurrentColdStarts(t *testing.T) {
	enableDynamicCodexProfileForTest(t)
	started := make(chan struct{})
	release := make(chan struct{})
	runner := &dynamicRecordingRunner{
		info:           bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"},
		versions:       []string{"1.2.0"},
		starts:         []dynamicStartResult{{session: &client.Session{}}, {session: &client.Session{}}},
		blockDynamic:   true,
		dynamicStarted: started,
		dynamicRelease: release,
	}
	pool := newSessionPool(
		nil,
		runner,
		fakeBotGetter{bot: enabledACPBot("bot-1", "api_key", map[string]any{"api_key": "sk-test"})},
	)

	errCh := make(chan error, 2)
	ensure := func(sessionID string) {
		_, err := pool.Ensure(context.Background(), PromptInput{
			BotID:                 "bot-1",
			SessionID:             sessionID,
			AgentID:               acpprofile.AgentCodexID,
			ProjectPath:           "/data/project",
			RuntimeOwnerAccountID: "user-1",
		})
		errCh <- err
	}
	go ensure("session-1")
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("dynamic adapter did not start")
	}
	go ensure("session-2")
	deadline := time.Now().Add(2 * time.Second)
	for len(runner.requests()) != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := len(runner.requests()); got != 2 {
		t.Fatalf("concurrent StartSession calls = %d, want 2", got)
	}
	if got := runner.resolveCallCount(); got != 1 {
		t.Fatalf("concurrent version lookups = %d, want 1", got)
	}
	close(release)
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatalf("Ensure() error = %v", err)
		}
	}
	requests := runner.requests()
	if len(requests) != 2 || requests[0].Args[1] != "@agentclientprotocol/codex-acp@1.2.0" || requests[1].Args[1] != "@agentclientprotocol/codex-acp@1.2.0" {
		t.Fatalf("concurrent exact-version requests = %#v", requests)
	}
}

func TestSessionPoolCanceledAdapterLookupCanRetry(t *testing.T) {
	enableDynamicCodexProfileForTest(t)
	started := make(chan struct{})
	runner := &dynamicRecordingRunner{
		info:           bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"},
		versions:       []string{"1.2.0"},
		starts:         []dynamicStartResult{{session: &client.Session{}}},
		blockResolve:   true,
		resolveStarted: started,
	}
	pool := newSessionPool(
		nil,
		runner,
		fakeBotGetter{bot: enabledACPBot("bot-1", "api_key", map[string]any{"api_key": "sk-test"})},
	)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := pool.Ensure(ctx, PromptInput{
			BotID:                 "bot-1",
			SessionID:             "session-1",
			AgentID:               acpprofile.AgentCodexID,
			ProjectPath:           "/data/project",
			RuntimeOwnerAccountID: "user-1",
		})
		errCh <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("adapter version lookup did not start")
	}
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Ensure() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ensure did not return after lookup cancellation")
	}
	if requests := runner.requests(); len(requests) != 0 {
		t.Fatalf("StartSession requests after lookup cancellation = %#v", requests)
	}

	runner.mu.Lock()
	runner.blockResolve = false
	runner.mu.Unlock()
	if _, err := pool.Ensure(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-2",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		RuntimeOwnerAccountID: "user-1",
	}); err != nil {
		t.Fatalf("Ensure after lookup cancellation error = %v", err)
	}
	if got := runner.resolveCallCount(); got != 2 {
		t.Fatalf("version lookups = %d, want canceled lookup followed by retry", got)
	}
	requests := runner.requests()
	if len(requests) != 1 || len(requests[0].Args) != 2 || requests[0].Args[1] != "@agentclientprotocol/codex-acp@1.2.0" {
		t.Fatalf("dynamic request after lookup retry = %#v", requests)
	}
}

func TestSessionPoolAdapterLookupWaiterCanCancel(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	runner := &dynamicRecordingRunner{
		versions:       []string{"1.2.0"},
		blockResolve:   true,
		resolveStarted: started,
		resolveRelease: release,
	}
	pool := newSessionPool(nil, runner, fakeBotGetter{})
	const packageName = "@agentclientprotocol/codex-acp"

	type result struct {
		version string
		err     error
	}
	leaderResult := make(chan result, 1)
	go func() {
		version, err := resolveAdapterVersionForTest(pool, "bot-1", packageName)
		leaderResult <- result{version: version, err: err}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("adapter version lookup did not start")
	}

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	waiterResult := make(chan error, 1)
	go func() {
		_, _, err := pool.resolveDynamicAdapter(waiterCtx, "bot-1", packageName, nil)
		waiterResult <- err
	}()
	cancelWaiter()
	select {
	case err := <-waiterResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting resolveDynamicAdapter() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiting resolveDynamicAdapter did not return after cancellation")
	}
	if got := runner.resolveCallCount(); got != 1 {
		t.Fatalf("version lookups while waiter canceled = %d, want 1", got)
	}

	close(release)
	select {
	case got := <-leaderResult:
		if got.err != nil || got.version != "1.2.0" {
			t.Fatalf("leading resolveDynamicAdapter() = %q, %v", got.version, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("leading resolveDynamicAdapter did not finish")
	}
	version, err := resolveAdapterVersionForTest(pool, "bot-1", packageName)
	if err != nil || version != "1.2.0" {
		t.Fatalf("cached resolveDynamicAdapter() = %q, %v", version, err)
	}
	if got := runner.resolveCallCount(); got != 1 {
		t.Fatalf("version lookups after cached result = %d, want 1", got)
	}
}

func TestSessionPoolAdapterLookupTimeoutFallsBackAndDisables(t *testing.T) {
	enableDynamicCodexProfileForTest(t)
	started := make(chan struct{})
	runner := &dynamicRecordingRunner{
		info:           bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"},
		blockResolve:   true,
		resolveStarted: started,
		starts: []dynamicStartResult{
			{session: &client.Session{}},
			{session: &client.Session{}},
		},
	}
	pool := newSessionPool(
		nil,
		runner,
		fakeBotGetter{bot: enabledACPBot("bot-1", "api_key", map[string]any{"api_key": "sk-test"})},
	)
	pool.dynamicAdapterStartTimeout = 20 * time.Millisecond

	if _, err := pool.Ensure(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		RuntimeOwnerAccountID: "user-1",
	}); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	select {
	case <-started:
	default:
		t.Fatal("adapter version lookup did not start")
	}
	if got := runner.resolveCallCount(); got != 1 {
		t.Fatalf("version lookups after timeout = %d, want 1", got)
	}
	requests := runner.requests()
	if len(requests) != 1 || requests[0].Command != "codex-acp" {
		t.Fatalf("requests after lookup timeout = %#v, want bundled fallback", requests)
	}

	if _, err := pool.Ensure(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-2",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		RuntimeOwnerAccountID: "user-1",
	}); err != nil {
		t.Fatalf("second Ensure() error = %v", err)
	}
	if got := runner.resolveCallCount(); got != 1 {
		t.Fatalf("version lookups after disabled timeout = %d, want 1", got)
	}
	requests = runner.requests()
	if len(requests) != 2 || requests[1].Command != "codex-acp" {
		t.Fatalf("requests after disabled timeout = %#v, want bundled fallback", requests)
	}
}

func TestSessionPoolDynamicAdapterFailureFallsBackAndBinds(t *testing.T) {
	enableDynamicCodexProfileForTest(t)
	fallbackSession := &client.Session{}
	runner := &dynamicRecordingRunner{
		info:     bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"},
		versions: []string{"1.2.0"},
		starts: []dynamicStartResult{
			{err: errors.New("dynamic unavailable")},
			{session: fallbackSession},
			{session: &client.Session{}},
		},
	}
	pool := newSessionPool(
		nil,
		runner,
		fakeBotGetter{bot: enabledACPBot("bot-1", "api_key", map[string]any{"api_key": "sk-test"})},
	)

	status, err := pool.Ensure(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		RuntimeOwnerAccountID: "user-1",
	})
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if status.State != stateIdle {
		t.Fatalf("fallback status = %#v", status)
	}
	h := pool.sessionHandle("session-1")
	if h == nil || h.session != fallbackSession {
		t.Fatalf("fallback session was not bound: %#v", h)
	}
	requests := runner.requests()
	if len(requests) != 2 {
		t.Fatalf("StartSession calls = %d, want dynamic then fallback", len(requests))
	}
	dynamic := requests[0]
	if dynamic.Command != "npx" || len(dynamic.Args) != 2 || dynamic.Args[1] != "@agentclientprotocol/codex-acp@1.2.0" {
		t.Fatalf("dynamic request = command %q args %#v", dynamic.Command, dynamic.Args)
	}
	if !startRequestEnvHas(dynamic.Env, "NPM_CONFIG_CACHE", "/data/.memoh/acp/npm-cache") {
		t.Fatalf("dynamic request env = %#v, want persistent npm cache", dynamic.Env)
	}
	fallback := requests[1]
	if fallback.Command != "codex-acp" {
		t.Fatalf("fallback request = command %q", fallback.Command)
	}
	if startRequestEnvHas(fallback.Env, "NPM_CONFIG_CACHE", "/data/.memoh/acp/npm-cache") {
		t.Fatalf("fallback request unexpectedly carries dynamic cache env: %#v", fallback.Env)
	}
	if _, err := pool.Ensure(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-2",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		RuntimeOwnerAccountID: "user-1",
	}); err != nil {
		t.Fatalf("second Ensure() error = %v", err)
	}
	requests = runner.requests()
	if runner.resolveCallCount() != 1 || len(requests) != 3 || requests[2].Command != "codex-acp" {
		t.Fatalf("failed candidate was retried: lookups=%d requests=%#v", runner.resolveCallCount(), requests)
	}
}

func TestSessionPoolDynamicAdapterCancellationDoesNotFallback(t *testing.T) {
	enableDynamicCodexProfileForTest(t)
	started := make(chan struct{})
	runner := &dynamicRecordingRunner{
		info:           bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"},
		versions:       []string{"1.2.0"},
		starts:         []dynamicStartResult{{session: &client.Session{}}},
		blockDynamic:   true,
		dynamicStarted: started,
	}
	pool := newSessionPool(
		nil,
		runner,
		fakeBotGetter{bot: enabledACPBot("bot-1", "api_key", map[string]any{"api_key": "sk-test"})},
	)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := pool.Ensure(ctx, PromptInput{
			BotID:                 "bot-1",
			SessionID:             "session-1",
			AgentID:               acpprofile.AgentCodexID,
			ProjectPath:           "/data/project",
			RuntimeOwnerAccountID: "user-1",
		})
		errCh <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("dynamic adapter did not start")
	}
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Ensure() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ensure did not return after cancellation")
	}
	if got := len(runner.requests()); got != 1 {
		t.Fatalf("StartSession calls = %d, want no fallback after cancellation", got)
	}
	runner.mu.Lock()
	runner.blockDynamic = false
	runner.mu.Unlock()
	if _, err := pool.Ensure(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-2",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		RuntimeOwnerAccountID: "user-1",
	}); err != nil {
		t.Fatalf("Ensure after caller cancellation error = %v", err)
	}
	if got := runner.resolveCallCount(); got != 1 {
		t.Fatalf("version lookups after caller cancellation = %d, want cached exact version", got)
	}
}

func TestSessionPoolDynamicAdapterTimeoutFallsBack(t *testing.T) {
	enableDynamicCodexProfileForTest(t)
	started := make(chan struct{})
	runner := &dynamicRecordingRunner{
		info:           bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"},
		versions:       []string{"1.2.0"},
		blockDynamic:   true,
		dynamicStarted: started,
		starts:         []dynamicStartResult{{session: &client.Session{}}, {session: &client.Session{}}},
	}
	pool := newSessionPool(
		nil,
		runner,
		fakeBotGetter{bot: enabledACPBot("bot-1", "api_key", map[string]any{"api_key": "sk-test"})},
	)
	pool.dynamicAdapterStartTimeout = 20 * time.Millisecond

	status, err := pool.Ensure(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		RuntimeOwnerAccountID: "user-1",
	})
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if status.State != stateIdle {
		t.Fatalf("timeout fallback status = %#v", status)
	}
	if got := len(runner.requests()); got != 2 {
		t.Fatalf("StartSession calls = %d, want timed-out dynamic then fallback", got)
	}
	if _, err := pool.Ensure(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-2",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		RuntimeOwnerAccountID: "user-1",
	}); err != nil {
		t.Fatalf("second Ensure() error = %v", err)
	}
	if runner.resolveCallCount() != 1 || len(runner.requests()) != 3 {
		t.Fatalf("timed-out candidate was retried: lookups=%d requests=%#v", runner.resolveCallCount(), runner.requests())
	}
}

func TestSessionPoolAdapterLookupFailureDisablesDynamicLaunchForProcess(t *testing.T) {
	enableDynamicCodexProfileForTest(t)
	runner := &dynamicRecordingRunner{
		info:        bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"},
		resolveErrs: []error{errors.New("registry unavailable")},
		starts: []dynamicStartResult{
			{session: &client.Session{}},
			{session: &client.Session{}},
		},
	}
	pool := newSessionPool(
		nil,
		runner,
		fakeBotGetter{bot: enabledACPBot("bot-1", "api_key", map[string]any{"api_key": "sk-test"})},
	)

	for _, sessionID := range []string{"session-1", "session-2"} {
		if _, err := pool.Ensure(context.Background(), PromptInput{
			BotID:                 "bot-1",
			SessionID:             sessionID,
			AgentID:               acpprofile.AgentCodexID,
			ProjectPath:           "/data/project",
			RuntimeOwnerAccountID: "user-1",
		}); err != nil {
			t.Fatalf("Ensure(%s) error = %v", sessionID, err)
		}
	}
	if got := runner.resolveCallCount(); got != 1 {
		t.Fatalf("version lookups after disabled lookup = %d, want 1", got)
	}
	for i, req := range runner.requests() {
		if req.Command != "codex-acp" {
			t.Fatalf("request %d command = %q, want bundled fallback", i, req.Command)
		}
	}
}

func TestDynamicACPEnvAddsToolkitCAOnlyWhenAvailableAndUnset(t *testing.T) {
	env := dynamicACPEnv([]string{"CUSTOM=1"}, true)
	if !startRequestEnvHas(env, "SSL_CERT_FILE", containerToolkitCABundle) {
		t.Fatalf("dynamic env = %#v, want toolkit CA bundle", env)
	}

	env = dynamicACPEnv([]string{"SSL_CERT_FILE=/custom/ca.pem"}, true)
	if !startRequestEnvHas(env, "SSL_CERT_FILE", "/custom/ca.pem") || startRequestEnvHas(env, "SSL_CERT_FILE", containerToolkitCABundle) {
		t.Fatalf("dynamic env replaced explicit CA bundle: %#v", env)
	}

	env = dynamicACPEnv(nil, false)
	if startRequestEnvHas(env, "SSL_CERT_FILE", containerToolkitCABundle) {
		t.Fatalf("dynamic env added missing toolkit CA bundle: %#v", env)
	}
}

func TestSessionPoolDetectsContainerToolkitCABundle(t *testing.T) {
	client, statServer := newCABundleStatClient(t)
	runner := &caBundleRunner{client: client}
	pool := newSessionPool(nil, runner, nil)
	info := bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"}
	if !pool.containerToolkitCABundleAvailable(context.Background(), "bot-1", info) {
		t.Fatal("container toolkit CA bundle was not detected")
	}
	statServer.mu.Lock()
	gotPath := statServer.path
	statServer.mu.Unlock()
	if gotPath != containerToolkitCABundle {
		t.Fatalf("Stat path = %q, want %q", gotPath, containerToolkitCABundle)
	}
}

func TestAdapterLookupEnvExcludesAgentCredentials(t *testing.T) {
	env := adapterLookupEnv([]string{
		"NPM_CONFIG_CACHE=/data/.memoh/acp/npm-cache",
		"SSL_CERT_FILE=/opt/memoh/toolkit/certs/ca-certificates.crt",
		"ANTHROPIC_API_KEY=sk-secret",
		"CUSTOM_FLAG=1",
	})
	if len(env) != 2 || !startRequestEnvHas(env, "NPM_CONFIG_CACHE", "/data/.memoh/acp/npm-cache") ||
		!startRequestEnvHas(env, "SSL_CERT_FILE", containerToolkitCABundle) {
		t.Fatalf("adapter lookup env = %#v", env)
	}
	for _, key := range []string{"ANTHROPIC_API_KEY", "CUSTOM_FLAG"} {
		if envHasKey(env, key) {
			t.Fatalf("adapter lookup env unexpectedly contains %s: %#v", key, env)
		}
	}
}

func TestSessionPoolCloseDuringColdStartPreventsReinsert(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	runner := &delayedStartRunner{
		info:    bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"},
		started: started,
		release: release,
		session: &client.Session{},
	}
	pool := newSessionPool(
		nil,
		runner,
		fakeBotGetter{bot: enabledACPBot("bot-1", "api_key", map[string]any{"api_key": "sk-test"})},
	)

	type startResult struct {
		handle *runtimeHandle
		err    error
	}
	resultCh := make(chan startResult, 1)
	go func() {
		h, err := pool.runtimeForSession(context.Background(), PromptInput{
			BotID:                 "bot-1",
			SessionID:             "session-1",
			AgentID:               "codex",
			ProjectPath:           "/data/project",
			RuntimeOwnerAccountID: "user-1",
		})
		resultCh <- startResult{handle: h, err: err}
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not start")
	}

	starting := pool.sessionHandle("session-1")
	if starting == nil {
		t.Fatal("starting handle was not registered in the session index")
	}
	closed := make(chan error, 1)
	go func() {
		closed <- pool.CloseSession("session-1")
	}()
	// Wait until CloseSession has aborted the start before releasing the
	// runner, mirroring a close that lands mid-startup.
	deadline := time.Now().Add(2 * time.Second)
	for {
		starting.state.Lock()
		aborted := starting.closed
		starting.state.Unlock()
		if aborted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("CloseSession did not abort the in-flight start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)

	var result startResult
	select {
	case result = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("runtimeForSession did not return")
	}
	if result.handle != nil {
		t.Fatalf("runtimeForSession returned a handle after CloseSession during startup")
	}
	if result.err == nil || !strings.Contains(result.err.Error(), "closed during startup") {
		t.Fatalf("runtimeForSession error = %v, want closed during startup", result.err)
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("CloseSession() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CloseSession did not return")
	}
	if pool.sessionHandle("session-1") != nil {
		t.Fatalf("closed cold-start runtime was reinserted into the pool")
	}
}

func TestSessionPoolCloseDuringColdStartCancelsStartup(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	runner := &cancelAwareStartRunner{
		info:      bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"},
		started:   started,
		cancelled: cancelled,
	}
	pool := newSessionPool(
		nil,
		runner,
		fakeBotGetter{bot: enabledACPBot("bot-1", "api_key", map[string]any{"api_key": "sk-test"})},
	)

	type startResult struct {
		handle *runtimeHandle
		err    error
	}
	resultCh := make(chan startResult, 1)
	go func() {
		h, err := pool.runtimeForSession(context.Background(), PromptInput{
			BotID:                 "bot-1",
			SessionID:             "session-1",
			AgentID:               "codex",
			ProjectPath:           "/data/project",
			RuntimeOwnerAccountID: "user-1",
		})
		resultCh <- startResult{handle: h, err: err}
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not start")
	}

	closed := make(chan error, 1)
	go func() {
		closed <- pool.CloseSession("session-1")
	}()
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("startup context was not cancelled")
	}

	var result startResult
	select {
	case result = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("runtimeForSession did not return after startup cancellation")
	}
	if result.handle != nil {
		t.Fatalf("runtimeForSession returned a handle after startup cancellation")
	}
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("runtimeForSession error = %v, want context.Canceled", result.err)
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("CloseSession() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CloseSession did not return")
	}
	if pool.sessionHandle("session-1") != nil {
		t.Fatalf("cancelled cold-start runtime remained in the pool")
	}
}

func TestSessionPoolCloseSessionWaitsForInFlightOperation(t *testing.T) {
	pool := newSessionPool(nil, nil, nil)
	h := &runtimeHandle{
		id:           newRuntimeID(),
		botID:        "bot-1",
		boundSession: "session-1",
		status:       stateActive,
		lastActive:   time.Now(),
	}
	injectRuntime(pool, h)
	h.op.Lock()

	closed := make(chan error, 1)
	go func() {
		closed <- pool.CloseSession("session-1")
	}()

	select {
	case err := <-closed:
		t.Fatalf("CloseSession returned before the in-flight operation released: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	h.op.Unlock()

	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("CloseSession() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CloseSession did not unblock after the operation released")
	}
}

func TestSessionPoolCloseSessionCancelsActivePrompt(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
	startedFile := filepath.Join(root, "prompt-started")
	cancelledFile := filepath.Join(root, "prompt-cancelled")
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_HANG_PROMPT", "1")
	t.Setenv("MEMOH_ACP_PROMPT_STARTED_FILE", startedFile)
	t.Setenv("MEMOH_ACP_PROMPT_CANCELLED_FILE", cancelledFile)

	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatal(err)
	}
	writeSessionPoolFakeAgentScript(t, binDir, "codex-acp")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	runner := client.NewRunner(nil, sessionPoolWorkspace{
		client: newSessionPoolBridgeClient(t, root),
		info: bridge.WorkspaceInfo{
			Backend:        bridge.WorkspaceBackendContainer,
			DefaultWorkDir: root,
		},
	})
	pool := newSessionPool(
		nil,
		runner,
		fakeBotGetter{bot: enabledACPBot("bot-1", "api_key", map[string]any{"api_key": "sk-container-byok"})},
	)
	t.Cleanup(pool.CloseAll)

	promptErrCh := make(chan error, 1)
	go func() {
		_, err := pool.Prompt(context.Background(), PromptInput{
			BotID:                 "bot-1",
			SessionID:             "session-1",
			AgentID:               acpprofile.AgentCodexID,
			ProjectPath:           "/data/project",
			Prompt:                "hang until close",
			RuntimeOwnerAccountID: "user-1",
		})
		promptErrCh <- err
	}()
	waitForSessionPoolFile(t, startedFile, 10*time.Second)

	closeErrCh := make(chan error, 1)
	go func() {
		closeErrCh <- pool.CloseSession("session-1")
	}()

	waitForSessionPoolFile(t, cancelledFile, 10*time.Second)
	select {
	case err := <-promptErrCh:
		if err == nil {
			t.Fatal("Prompt returned nil error after CloseSession cancelled it")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt did not return after CloseSession")
	}
	select {
	case err := <-closeErrCh:
		if err != nil {
			t.Fatalf("CloseSession() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CloseSession did not return after cancelling the prompt")
	}
}

func TestSessionPoolSerializesColdStartForSameSession(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}

	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatal(err)
	}
	writeSessionPoolFakeAgentScript(t, binDir, "codex-acp")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	startLog := filepath.Join(root, "starts.log")
	t.Setenv("MEMOH_ACP_START_LOG", startLog)

	runner := client.NewRunner(nil, sessionPoolWorkspace{
		client: newSessionPoolBridgeClient(t, root),
		info: bridge.WorkspaceInfo{
			Backend:        bridge.WorkspaceBackendContainer,
			DefaultWorkDir: root,
		},
	})
	pool := newSessionPool(nil, runner, fakeBotGetter{bot: enabledACPBot("bot-1", "api_key", map[string]any{"api_key": "sk-container-byok"})})
	t.Cleanup(pool.CloseAll)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := pool.Prompt(context.Background(), PromptInput{
				BotID:                 "bot-1",
				SessionID:             "session-1",
				AgentID:               "codex",
				ProjectPath:           "/data/project",
				Prompt:                "same session",
				RuntimeOwnerAccountID: "user-1",
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Prompt() error = %v", err)
		}
	}

	raw, err := os.ReadFile(startLog) //nolint:gosec // test path under t.TempDir.
	if err != nil {
		t.Fatalf("read start log: %v", err)
	}
	if starts := strings.Count(string(raw), "start\n"); starts != 1 {
		t.Fatalf("fake ACP process starts = %d, want 1; log=%q", starts, string(raw))
	}
}

func TestSessionPoolSetupModeResolution(t *testing.T) {
	missingAPIKey := newSessionPool(nil, &recordingRunner{
		info: bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"},
	}, fakeBotGetter{bot: enabledACPBot("bot-1", "api_key", nil)})
	_, err := missingAPIKey.Prompt(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               "codex",
		ProjectPath:           "/data/project",
		Prompt:                "run",
		RuntimeOwnerAccountID: "user-1",
	})
	var feedbackErr *feedback.Error
	if !errors.As(err, &feedbackErr) || feedbackErr.Code != feedback.CodeAgentNotConfigured || !strings.Contains(feedbackErr.Message, "api_key required") {
		t.Fatalf("container api_key missing key error = %v", err)
	}

	apiKeyRunner := &recordingRunner{
		info:     bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data", ACPToolsHTTPURL: "http://127.0.0.1:18732/mcp"},
		startErr: errors.New("started"),
	}
	apiKeyPool := newSessionPool(nil, apiKeyRunner, fakeBotGetter{bot: enabledACPBot("bot-1", "api_key", map[string]any{"api_key": "sk-test", "base_url": "https://proxy.example.com/v1"})})
	_, err = apiKeyPool.Prompt(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               "codex",
		ProjectPath:           "/data/project",
		Prompt:                "run",
		RuntimeOwnerAccountID: "user-1",
	})
	if err == nil || err.Error() != "started" {
		t.Fatalf("container api_key error = %v, want runner start error", err)
	}
	if apiKeyRunner.req.SetupMode != client.SetupModeAPIKey {
		t.Fatalf("api_key setup mode = %q", apiKeyRunner.req.SetupMode)
	}
	if len(apiKeyRunner.req.Env) != 0 {
		t.Fatalf("api_key mode must use Codex files, not credential env: %v", apiKeyRunner.req.Env)
	}

	oauthRoot := t.TempDir()
	oauthRunner := &hermesRecordingRunner{
		info:     bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"},
		client:   newSessionPoolBridgeClient(t, oauthRoot),
		startErr: errors.New("started"),
	}
	oauthPool := newSessionPool(nil, oauthRunner, fakeBotGetter{bot: enabledACPBot("bot-1", "oauth", map[string]any{"provider_id": "provider-1"})})
	_, err = oauthPool.Prompt(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               "codex",
		ProjectPath:           "/data/project",
		Prompt:                "run",
		RuntimeOwnerAccountID: "user-1",
	})
	if err == nil || err.Error() != "started" {
		t.Fatalf("container oauth error = %v, want runner start error", err)
	}
	if oauthRunner.req.SetupMode != client.SetupModeOAuth {
		t.Fatalf("oauth setup mode = %q", oauthRunner.req.SetupMode)
	}

	selfRunner := &recordingRunner{
		info:     bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"},
		startErr: errors.New("started"),
	}
	selfPool := newSessionPool(nil, selfRunner, fakeBotGetter{bot: enabledACPBot("bot-1", "self", nil)})
	_, err = selfPool.Prompt(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               "codex",
		ProjectPath:           "/data/project",
		Prompt:                "run",
		RuntimeOwnerAccountID: "user-1",
	})
	if err == nil || err.Error() != "started" {
		t.Fatalf("container self error = %v, want runner start error", err)
	}
	if selfRunner.req.SetupMode != client.SetupModeSelf {
		t.Fatalf("self setup mode = %q", selfRunner.req.SetupMode)
	}
	if len(selfRunner.req.Env) != 0 {
		t.Fatalf("self mode injected credential env: %v", selfRunner.req.Env)
	}
	if got := selfPool.RuntimeStatus("session-1", "codex", "/data/project"); got.State != "idle" || got.ACPSession != "" {
		t.Fatalf("RuntimeStatus after failed start = %#v, want idle without process", got)
	}

	claudeRunner := &recordingRunner{
		info:     bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"},
		startErr: errors.New("started"),
	}
	claudePool := newSessionPool(nil, claudeRunner, fakeBotGetter{bot: enabledACPAgentBot("bot-1", acpprofile.AgentClaudeCodeID, "api_key", map[string]any{
		"api_key":  "sk-ant-test",
		"base_url": "https://anthropic-proxy.example.com",
	})})
	_, err = claudePool.Prompt(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               acpprofile.AgentClaudeCodeID,
		ProjectPath:           "/data/project",
		Prompt:                "run",
		RuntimeOwnerAccountID: "user-1",
	})
	if err == nil || err.Error() != "started" {
		t.Fatalf("container Claude Code api_key error = %v, want runner start error", err)
	}
	if claudeRunner.req.Command != "claude-agent-acp" {
		t.Fatalf("Claude Code command = %q", claudeRunner.req.Command)
	}
	if !startRequestEnvHas(claudeRunner.req.Env, "ANTHROPIC_API_KEY", "sk-ant-test") ||
		!startRequestEnvHas(claudeRunner.req.Env, "ANTHROPIC_BASE_URL", "https://anthropic-proxy.example.com") {
		t.Fatalf("Claude Code env = %#v, want Anthropic managed env", claudeRunner.req.Env)
	}
	if !startRequestEnvHas(claudeRunner.req.Env, "ANTHROPIC_AUTH_TOKEN", "") ||
		!startRequestEnvHas(claudeRunner.req.Env, "CLAUDE_CODE_OAUTH_TOKEN", "") {
		t.Fatalf("Claude Code api_key env = %#v, want conflicting auth env cleared", claudeRunner.req.Env)
	}

	hermesRoot := t.TempDir()
	hermesRunner := &hermesRecordingRunner{
		info: bridge.WorkspaceInfo{
			Backend:        bridge.WorkspaceBackendContainer,
			DefaultWorkDir: "/data",
		},
		client:   newSessionPoolBridgeClient(t, hermesRoot),
		startErr: errors.New("started"),
	}
	hermesPool := newSessionPool(nil, hermesRunner, fakeBotGetter{bot: enabledACPAgentBot("bot-1", acpprofile.AgentHermesID, "api_key", map[string]any{
		"provider": "openrouter",
		"model":    "anthropic/claude-sonnet-4",
		"api_key":  "sk-hermes",
	})})
	_, err = hermesPool.Prompt(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               acpprofile.AgentHermesID,
		ProjectPath:           "/data/project",
		Prompt:                "run",
		RuntimeOwnerAccountID: "user-1",
	})
	if err == nil || err.Error() != "started" {
		t.Fatalf("container Hermes api_key error = %v, want runner start error", err)
	}
	if hermesRunner.req.Command != "hermes-acp" {
		t.Fatalf("Hermes command = %q", hermesRunner.req.Command)
	}
	if !hermesRunner.req.CleanEnv {
		t.Fatalf("Hermes managed CleanEnv = false, want true")
	}
	if !hasString(hermesRunner.req.UnsetEnv, "HERMES_*") || !hasString(hermesRunner.req.UnsetEnv, "OPENROUTER_API_KEY") || !hasString(hermesRunner.req.UnsetEnv, "OPENROUTER_BASE_URL") {
		t.Fatalf("Hermes managed UnsetEnv = %#v", hermesRunner.req.UnsetEnv)
	}
	if hermesRunner.req.Resolved == nil || hermesRunner.req.Resolved.HermesHome != client.HermesContainerHome {
		t.Fatalf("Hermes resolved context = %#v", hermesRunner.req.Resolved)
	}
	configPath := filepath.Join(hermesRoot, ".memoh-hermes", "config.yaml")
	configBytes, readErr := os.ReadFile(configPath) //nolint:gosec // test path is under t.TempDir.
	if readErr != nil {
		t.Fatalf("read Hermes config: %v", readErr)
	}
	if content := string(configBytes); !strings.Contains(content, `provider: "openrouter"`) || strings.Contains(content, "sk-hermes") {
		t.Fatalf("Hermes config content =\n%s", content)
	}

	defaultBackendRoot := t.TempDir()
	defaultBackendRunner := &hermesRecordingRunner{
		info: bridge.WorkspaceInfo{
			DefaultWorkDir: "/data",
		},
		client:   newSessionPoolBridgeClient(t, defaultBackendRoot),
		startErr: errors.New("started"),
	}
	defaultBackendPool := newSessionPool(nil, defaultBackendRunner, fakeBotGetter{bot: enabledACPAgentBot("bot-1", acpprofile.AgentHermesID, "api_key", map[string]any{
		"provider": "gemini",
		"model":    "gemini-3.5-flash",
		"api_key":  "AIza-hermes",
	})})
	_, err = defaultBackendPool.Prompt(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               acpprofile.AgentHermesID,
		ProjectPath:           "/data/project",
		Prompt:                "run",
		RuntimeOwnerAccountID: "user-1",
	})
	if err == nil || err.Error() != "started" {
		t.Fatalf("default backend Hermes api_key error = %v, want runner start error", err)
	}
	if defaultBackendRunner.req.Resolved == nil || defaultBackendRunner.req.Resolved.Backend != client.WorkspaceBackendContainer {
		t.Fatalf("default backend resolved context = %#v, want container backend", defaultBackendRunner.req.Resolved)
	}

	claudeOAuthRunner := &recordingRunner{
		info:     bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"},
		startErr: errors.New("started"),
	}
	claudeOAuthManaged := map[string]any{ //nolint:gosec // Test fixture token, not a real credential.
		"oauth_token": "fake-claude-oauth-token",
		"base_url":    "https://anthropic-proxy.example.com",
	}
	claudeOAuthPool := newSessionPool(nil, claudeOAuthRunner, fakeBotGetter{bot: enabledACPAgentBot("bot-1", acpprofile.AgentClaudeCodeID, "oauth", claudeOAuthManaged)})
	_, err = claudeOAuthPool.Prompt(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               acpprofile.AgentClaudeCodeID,
		ProjectPath:           "/data/project",
		Prompt:                "run",
		RuntimeOwnerAccountID: "user-1",
	})
	if err == nil || err.Error() != "started" {
		t.Fatalf("container Claude Code oauth error = %v, want runner start error", err)
	}
	if !startRequestEnvHas(claudeOAuthRunner.req.Env, "CLAUDE_CODE_OAUTH_TOKEN", "fake-claude-oauth-token") ||
		!startRequestEnvHas(claudeOAuthRunner.req.Env, "ANTHROPIC_BASE_URL", "https://anthropic-proxy.example.com") {
		t.Fatalf("Claude Code oauth env = %#v, want Claude managed oauth env", claudeOAuthRunner.req.Env)
	}
	if !startRequestEnvHas(claudeOAuthRunner.req.Env, "ANTHROPIC_API_KEY", "") ||
		!startRequestEnvHas(claudeOAuthRunner.req.Env, "ANTHROPIC_AUTH_TOKEN", "") {
		t.Fatalf("Claude Code oauth env = %#v, want conflicting auth env cleared", claudeOAuthRunner.req.Env)
	}
}

func TestSessionPoolRejectsUnsupportedSetupMode(t *testing.T) {
	runner := &recordingRunner{
		info:     bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"},
		startErr: errors.New("started"),
	}
	pool := newSessionPool(nil, runner, fakeBotGetter{bot: enabledACPAgentBot("bot-1", acpprofile.AgentHermesID, "oauth", map[string]any{
		"oauth_token": "fake",
	})})
	_, err := pool.Prompt(context.Background(), PromptInput{
		BotID:     "bot-1",
		SessionID: "session-1",
		AgentID:   acpprofile.AgentHermesID,
		Prompt:    "run",
	})
	if err == nil || !strings.Contains(err.Error(), `does not support setup mode "oauth"`) {
		t.Fatalf("Prompt() error = %v, want unsupported setup mode", err)
	}
	if runner.req.AgentID != "" {
		t.Fatalf("runner should not have been started: %#v", runner.req)
	}
}

func TestSessionPoolRejectsUnsupportedBackend(t *testing.T) {
	runner := &recordingRunner{
		info:     bridge.WorkspaceInfo{Backend: "remote", DefaultWorkDir: "/data"},
		startErr: errors.New("started"),
	}
	pool := newSessionPool(nil, runner, fakeBotGetter{bot: enabledACPAgentBot("bot-1", acpprofile.AgentHermesID, "api_key", nil)})
	_, err := pool.Prompt(context.Background(), PromptInput{
		BotID:     "bot-1",
		SessionID: "session-1",
		AgentID:   acpprofile.AgentHermesID,
		Prompt:    "run",
	})
	if err == nil || !strings.Contains(err.Error(), `does not support workspace backend "remote"`) {
		t.Fatalf("Prompt() error = %v, want unsupported workspace backend", err)
	}
	if runner.req.AgentID != "" {
		t.Fatalf("runner should not have been started: %#v", runner.req)
	}
}

func TestProfileSupportsBackend(t *testing.T) {
	if !profileSupportsBackend(acpprofile.Profile{}, "custom-backend") {
		t.Fatal("profile with no supported_backends should allow any backend")
	}
	if !profileSupportsBackend(acpprofile.Profile{SupportedBackends: []string{bridge.WorkspaceBackendContainer}}, "") {
		t.Fatal("empty backend should be treated as container")
	}
	if profileSupportsBackend(acpprofile.Profile{SupportedBackends: []string{bridge.WorkspaceBackendRemote}}, bridge.WorkspaceBackendContainer) {
		t.Fatal("remote-only profile should reject container backend")
	}
}

func TestValidateManagedACPConfigAcceptsHermesOpenAIAPIProvider(t *testing.T) {
	profile, ok := acpprofile.Lookup(acpprofile.AgentHermesID)
	if !ok {
		t.Fatal("missing Hermes profile")
	}
	err := client.ValidateManagedACPConfig(profile, acpprofile.AgentSetup{Managed: map[string]string{
		"provider": "openai-api",
		"model":    "gpt-5.4",
		"api_key":  "sk-test",
	}}, client.SetupModeAPIKey)
	if err != nil {
		t.Fatalf("ValidateManagedACPConfig() error = %v, want openai-api accepted", err)
	}
}

func TestSessionPoolUsesSessionMetadataAsRuntimeTruth(t *testing.T) {
	runner := &recordingRunner{
		info:     bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data", ACPToolsHTTPURL: "http://127.0.0.1:18732/mcp"},
		startErr: errors.New("started"),
	}
	pool := newSessionPool(
		nil,
		runner,
		fakeBotGetter{bot: enabledACPBot("bot-1", "api_key", map[string]any{"api_key": "sk-test"})},
		fakeSessionGetter{session: SessionDescriptor{
			BotID:       "bot-1",
			SessionType: sessionmode.ACPAgent,
			IsACP:       true,
			Metadata: map[string]any{
				"acp_agent_id":             "codex",
				"project_path":             "/data/from-session",
				"runtime_owner_account_id": "user-1",
			},
		}},
	)

	_, err := pool.Prompt(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               "wrong-agent",
		ProjectPath:           "/data/from-caller",
		Prompt:                "run",
		RuntimeOwnerAccountID: "ignored-owner",
	})
	if err == nil || err.Error() != "started" {
		t.Fatalf("Prompt() error = %v, want runner start error", err)
	}
	if runner.req.AgentID != "codex" {
		t.Fatalf("runner agent_id = %q, want session metadata codex", runner.req.AgentID)
	}
	if runner.req.ProjectPath != "/data/from-session" {
		t.Fatalf("runner project_path = %q, want session metadata project path", runner.req.ProjectPath)
	}
}

func TestSessionPoolBakesOnlyStableRuntimeIdentity(t *testing.T) {
	runner := &recordingRunner{
		info:     bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data", ACPToolsHTTPURL: "http://127.0.0.1:18732/mcp"},
		startErr: errors.New("started"),
	}
	pool := newSessionPool(
		nil,
		runner,
		fakeBotGetter{bot: enabledACPBot("bot-1", "api_key", map[string]any{"api_key": "sk-test"})},
	)
	pool.SetToolGateway(mcp.NewToolGatewayService(nil, nil))
	contexts := mcp.NewToolSessionContextStore()
	pool.SetToolSessionContextStore(contexts)

	_, err := pool.Prompt(context.Background(), PromptInput{
		BotID:                 "bot-1",
		ChatID:                "chat-1",
		SessionID:             "session-1",
		RunID:                 "run-1",
		RouteID:               "route-1",
		AgentID:               "codex",
		ProjectPath:           "/data/project",
		Prompt:                "run",
		ChannelIdentityID:     "user-1",
		RuntimeOwnerAccountID: "user-1",
		SessionToken:          "token-1",
		CurrentPlatform:       "web",
		ReplyTarget:           "reply-1",
		ConversationType:      "private",
	})
	if err == nil || err.Error() != "started" {
		t.Fatalf("Prompt() error = %v, want runner start error", err)
	}
	if runner.req.ToolHTTPURL != "http://127.0.0.1:18732/mcp" {
		t.Fatalf("ToolHTTPURL = %q", runner.req.ToolHTTPURL)
	}
	// Only stable runtime identity may be baked into the process config: the
	// per-prompt fields (stream, token, reply target...) change every turn
	// and are resolved live from the handle instead.
	baked := runner.req.ToolSession
	if baked.BotID != "bot-1" || !strings.HasPrefix(baked.RuntimeID, runtimeIDPrefix) || baked.RuntimeToken == "" || baked.SessionType != sessionmode.ACPAgent {
		t.Fatalf("baked identity = %#v, want stable runtime identity", baked)
	}
	if baked.SessionID != "" || baked.RunID != "" || baked.SessionToken != "" || baked.ReplyTarget != "" || baked.RouteID != "" || baked.ChannelIdentityID != "" {
		t.Fatalf("baked identity leaks per-prompt fields: %#v", baked)
	}
	// The pool no longer publishes ACP contexts into the shared store.
	merged := contexts.Merge(mcp.ToolSessionContext{BotID: "bot-1", SessionID: "session-1"})
	if merged.RunID != "" || merged.ConversationType != "" {
		t.Fatalf("ACP context leaked into the shared store: %#v", merged)
	}
}

func TestSessionPoolUsesWorkspaceACPToolsEndpointForContainer(t *testing.T) {
	pool := newSessionPool(nil, nil, nil)
	pool.SetToolGateway(mcp.NewToolGatewayService(nil, nil))

	got, err := pool.resolveToolHTTPURL("", bridge.WorkspaceInfo{
		Backend:         bridge.WorkspaceBackendContainer,
		ACPToolsHTTPURL: "http://127.0.0.1:18732/mcp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://127.0.0.1:18732/mcp" {
		t.Fatalf("container ToolHTTPURL = %q", got)
	}
}

func TestRuntimeHandleToolContextOverlaysActivePrompt(t *testing.T) {
	h := &runtimeHandle{
		id:           "rt_test",
		botID:        "bot-1",
		boundSession: "session-1",
	}

	// Idle: stable identity plus the binding.
	ctx := h.toolContext()
	if ctx.BotID != "bot-1" || ctx.RuntimeID != "rt_test" || ctx.SessionID != "session-1" || ctx.SessionType != sessionmode.ACPAgent {
		t.Fatalf("idle tool context = %#v", ctx)
	}
	if ctx.RunID != "" || ctx.SessionToken != "" || ctx.IsSubagent {
		t.Fatalf("idle tool context leaks per-prompt fields: %#v", ctx)
	}
	if ctx.RuntimeActive {
		t.Fatalf("idle tool context must not allow tools/call: %#v", ctx)
	}
	if !ctx.CanListUserInput || ctx.CanRequestUserInput {
		t.Fatalf("idle tool context must expose list-only user input tools: %#v", ctx)
	}

	// During a prompt the live per-prompt fields overlay.
	wantFence := runtimefence.Fence{BotID: "bot-1", SessionID: "session-1", Token: 29}
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	guardCalls := 0
	active := client.ToolSessionContext{
		ChatID:             "chat-1",
		SessionID:          "session-1",
		RunID:              "stream-7",
		SessionToken:       "token-7",
		CurrentPlatform:    "web",
		ReplyTarget:        "reply-7",
		ConversationType:   "private",
		SupportsImageInput: true,
		RuntimeFence:       wantFence,
		RunContext:         runCtx,
		RuntimeGuard: func(context.Context) error {
			guardCalls++
			return nil
		},
	}
	h.state.Lock()
	h.active = &active
	h.state.Unlock()
	ctx = h.toolContext()
	if ctx.RunID != "stream-7" || ctx.SessionToken != "token-7" || ctx.ChatID != "chat-1" || ctx.ReplyTarget != "reply-7" || !ctx.RuntimeActive {
		t.Fatalf("active tool context = %#v", ctx)
	}
	if !ctx.CanListUserInput {
		t.Fatalf("active tool context must expose listable user input tools: %#v", ctx)
	}
	if ctx.RuntimeID != "rt_test" || ctx.IsSubagent {
		t.Fatalf("active tool context lost stable identity: %#v", ctx)
	}
	if !ctx.SupportsImageInput {
		t.Fatalf("active tool context lost image capability: %#v", ctx)
	}
	if ctx.RuntimeFence != wantFence {
		t.Fatalf("active tool context fence = %#v, want %#v", ctx.RuntimeFence, wantFence)
	}
	if ctx.RunContext != runCtx || ctx.RuntimeGuard == nil {
		t.Fatalf("active tool context lost runtime lifecycle: %#v", ctx)
	}
	if err := ctx.RuntimeGuard(context.Background()); err != nil || guardCalls != 1 {
		t.Fatalf("runtime guard = (%v, calls:%d), want one successful call", err, guardCalls)
	}

	// clearActive removes every per-prompt field again.
	h.clearActive()
	ctx = h.toolContext()
	if ctx.RunID != "" || ctx.SessionToken != "" || ctx.ChatID != "bot-1" || ctx.RuntimeActive || ctx.SupportsImageInput || !ctx.CanListUserInput || ctx.RunContext != nil || ctx.RuntimeGuard != nil {
		t.Fatalf("cleared tool context = %#v", ctx)
	}
}

func TestToolSessionContextCarriesPromptRuntimeFence(t *testing.T) {
	want := runtimefence.Fence{BotID: "bot-1", SessionID: "session-1", Token: 31}
	ctx := runtimefence.WithContext(context.Background(), want)
	guard := func(context.Context) error { return nil }
	got := toolSessionContext(ctx, PromptInput{SessionID: want.SessionID, RunID: "run-1", RuntimeGuard: guard}, &runtimeHandle{id: "rt-1", botID: want.BotID})
	if got.RuntimeFence != want {
		t.Fatalf("tool session fence = %#v, want %#v", got.RuntimeFence, want)
	}
	if got.RunContext != ctx || got.RuntimeGuard == nil {
		t.Fatalf("tool session runtime lifecycle = context:%v guard:%v", got.RunContext, got.RuntimeGuard != nil)
	}
}

func TestSessionPoolResolveRuntimeToolContext(t *testing.T) {
	pool := newSessionPool(nil, nil, nil)
	h := &runtimeHandle{
		id:           "rt_live",
		toolToken:    "runtime-token-1",
		botID:        "bot-1",
		boundSession: "session-1",
		status:       stateIdle,
		session:      &client.Session{},
	}
	injectRuntime(pool, h)

	ctx, ok := pool.ResolveRuntimeToolContext("bot-1", "rt_live", "runtime-token-1")
	if !ok || ctx.RuntimeID != "rt_live" || ctx.SessionID != "session-1" {
		t.Fatalf("ResolveRuntimeToolContext() = %#v, %v", ctx, ok)
	}
	if _, ok := pool.ResolveRuntimeToolContext("bot-1", "rt_live", "wrong-token"); ok {
		t.Fatalf("runtime context resolved with wrong token")
	}
	if _, ok := pool.ResolveRuntimeToolContext("bot-2", "rt_live", "runtime-token-1"); ok {
		t.Fatalf("cross-bot runtime context resolved")
	}
	if _, ok := pool.ResolveRuntimeToolContext("bot-1", "rt_missing", "runtime-token-1"); ok {
		t.Fatalf("missing runtime context resolved")
	}

	h.state.Lock()
	h.closed = true
	h.state.Unlock()
	if _, ok := pool.ResolveRuntimeToolContext("bot-1", "rt_live", "runtime-token-1"); ok {
		t.Fatalf("dead runtime context resolved; must fail closed")
	}
}

func TestPromptToolEventSinkPreservesACPAndHTTPToolEventOrder(t *testing.T) {
	sink := newPromptToolEventSink(nil)
	sink.EmitStreamEvent(event.StreamEvent{Type: event.TextDelta, Delta: "before"})
	sink.EmitToolStreamEvent(mcp.ToolStreamEvent{
		Type:       "tool_call_start",
		ToolCallID: "call-1",
		ToolName:   "write",
		Input:      map[string]any{"path": "notes.txt"},
	})
	sink.EmitToolStreamEvent(mcp.ToolStreamEvent{
		Type:       "tool_approval_request",
		ToolCallID: "call-1",
		ToolName:   "write",
		Input:      map[string]any{"path": "notes.txt"},
		ApprovalID: "approval-1",
		ShortID:    7,
		Status:     toolapproval.StatusPending,
		Metadata: map[string]any{
			"approval": toolapproval.RequestMetadata(toolapproval.Request{
				ID:      "approval-1",
				ShortID: 7,
				Status:  toolapproval.StatusPending,
			}),
		},
	})
	sink.EmitToolStreamEvent(mcp.ToolStreamEvent{
		Type:       "tool_call_end",
		ToolCallID: "call-1",
		ToolName:   "write",
		Result:     map[string]any{"ok": true},
	})
	sink.EmitStreamEvent(event.StreamEvent{Type: event.TextDelta, Delta: "after"})

	events := sink.Events()
	if len(events) != 5 {
		t.Fatalf("events = %#v", events)
	}
	if events[0].Type != event.TextDelta || events[1].Type != event.ToolCallStart || events[2].Type != event.ToolApprovalRequest || events[3].Type != event.ToolCallEnd || events[4].Type != event.TextDelta {
		t.Fatalf("events order = %#v", events)
	}

	result := client.PromptResult{}
	sink.ApplyToResult(&result)
	if len(result.Events) != 5 {
		t.Fatalf("result events = %#v, want sink events", result.Events)
	}
	if len(result.Output) != 3 {
		t.Fatalf("output = %#v, want assistant text+tool call/tool result/after", result.Output)
	}
	if len(result.Output[0].Content) != 2 {
		t.Fatalf("output[0] = %#v, want text plus tool call", result.Output[0])
	}
	toolCall, ok := result.Output[0].Content[1].(sdk.ToolCallPart)
	if !ok {
		t.Fatalf("output[0] = %#v, want tool call", result.Output[0])
	}
	approval, ok := toolCall.ProviderMetadata["approval"].(map[string]any)
	if !ok || approval["approval_id"] != "approval-1" || approval["status"] != toolapproval.StatusPending {
		t.Fatalf("tool call approval metadata = %#v", toolCall.ProviderMetadata)
	}
	toolResult, ok := result.Output[1].Content[0].(sdk.ToolResultPart)
	if !ok || toolResult.ToolCallID != "call-1" || toolResult.IsError {
		t.Fatalf("output[1] = %#v, want successful tool result", result.Output[1])
	}
}

// Resolving a bound runtime (e.g. the UI keeping it ensured while the user
// types) counts as activity and must defer idle reaping.
func TestSessionPoolEnsureRefreshesIdleClock(t *testing.T) {
	pool := newFakeScriptPool(t)
	pool.timeout = 30 * time.Minute

	stale := time.Now().Add(-29 * time.Minute)
	h := &runtimeHandle{
		id:                    newRuntimeID(),
		botID:                 "bot-1",
		agentID:               acpprofile.AgentCodexID,
		projectPath:           "/data/project",
		status:                stateIdle,
		lastActive:            stale,
		boundSession:          "session-1",
		session:               &client.Session{},
		runtimeOwnerAccountID: "user-1",
	}
	injectRuntime(pool, h)

	if _, err := pool.Ensure(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               acpprofile.AgentCodexID,
		ProjectPath:           "/data/project",
		RuntimeOwnerAccountID: "user-1",
	}); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	h.state.Lock()
	refreshed := h.lastActive.After(stale)
	h.state.Unlock()
	if !refreshed {
		t.Fatalf("Ensure did not refresh the idle clock")
	}
	// Two minutes later (31 minutes after the original activity) the runtime
	// must survive the reaper because the ensure refreshed it.
	if got := pool.reapIdle(time.Now().Add(2 * time.Minute)); got != 0 {
		t.Fatalf("reapIdle() = %d, want 0 after ensure refresh", got)
	}
}

func TestSessionPoolReapIdlePolicies(t *testing.T) {
	pool := newSessionPool(nil, nil, nil)
	pool.timeout = 30 * time.Minute
	now := time.Now()

	injectRuntime(pool, &runtimeHandle{id: "rt_bound-stale", botID: "b", boundSession: "s1", status: stateIdle, lastActive: now.Add(-31 * time.Minute)})
	injectRuntime(pool, &runtimeHandle{id: "rt_bound-active", botID: "b", boundSession: "s2", status: stateActive, lastActive: now.Add(-31 * time.Minute)})
	injectRuntime(pool, &runtimeHandle{id: "rt_bound-fresh", botID: "b", boundSession: "s3", status: stateIdle, lastActive: now.Add(-30 * time.Second)})
	injectRuntime(pool, &runtimeHandle{id: "rt_unbound-stale", botID: "b", status: stateIdle, lastActive: now.Add(-6 * time.Minute)})
	injectRuntime(pool, &runtimeHandle{id: "rt_bound-6m", botID: "b", boundSession: "s4", status: stateIdle, lastActive: now.Add(-6 * time.Minute)})

	if got := pool.reapIdle(now); got != 2 {
		t.Fatalf("reapIdle() = %d, want 2", got)
	}
	pool.mu.RLock()
	defer pool.mu.RUnlock()
	if _, ok := pool.runtimes["rt_bound-stale"]; ok {
		t.Fatalf("stale bound runtime was not reaped")
	}
	if _, ok := pool.runtimes["rt_unbound-stale"]; ok {
		t.Fatalf("stale unbound runtime was not reaped (5 minute policy)")
	}
	if _, ok := pool.runtimes["rt_bound-active"]; !ok {
		t.Fatalf("active runtime must not be reaped")
	}
	if _, ok := pool.runtimes["rt_bound-fresh"]; !ok {
		t.Fatalf("fresh runtime must not be reaped")
	}
	if _, ok := pool.runtimes["rt_bound-6m"]; !ok {
		t.Fatalf("bound runtime must use the 30 minute policy")
	}
	if _, ok := pool.bySession["s1"]; ok {
		t.Fatalf("reap left the session index entry behind")
	}
}

func TestCloseSessionCancelsPendingDecisions(t *testing.T) {
	t.Parallel()

	approval := &fakeToolApprovalService{}
	userInput := &fakeUserInputCanceller{}
	pool := newSessionPool(nil, nil, fakeBotGetter{})
	pool.SetToolApprovalService(approval)
	pool.SetUserInputService(userInput)
	injectRuntime(pool, &runtimeHandle{
		id:           "rt_decision-cleanup",
		botID:        "bot-1",
		status:       stateIdle,
		boundSession: "session-1",
		lastActive:   time.Now(),
		hadPrompt:    true,
	})

	if err := pool.CloseSession("session-1"); err != nil {
		t.Fatalf("CloseSession() error = %v", err)
	}
	if approval.cancelBotID != "bot-1" || approval.cancelSessionID != "session-1" || approval.cancelReason == "" {
		t.Fatalf("cancel pending approvals = bot:%q session:%q reason:%q", approval.cancelBotID, approval.cancelSessionID, approval.cancelReason)
	}
	if userInput.cancelBotID != "bot-1" || userInput.cancelSessionID != "session-1" || userInput.cancelReason == "" {
		t.Fatalf("cancel pending user inputs = bot:%q session:%q reason:%q", userInput.cancelBotID, userInput.cancelSessionID, userInput.cancelReason)
	}
	if approval.cancelCount != 2 || userInput.cancelCount != 2 {
		t.Fatalf("decision cleanup count = approval:%d user_input:%d, want pre and final cleanup", approval.cancelCount, userInput.cancelCount)
	}
}

func TestCloseSessionWithoutPromptDoesNotCancelPendingDecisions(t *testing.T) {
	t.Parallel()

	approval := &fakeToolApprovalService{}
	userInput := &fakeUserInputCanceller{}
	pool := newSessionPool(nil, nil, fakeBotGetter{})
	pool.SetToolApprovalService(approval)
	pool.SetUserInputService(userInput)
	injectRuntime(pool, &runtimeHandle{
		id: "rt-ensure-only", botID: "bot-1", status: stateIdle,
		boundSession: "session-1", lastActive: time.Now(),
	})

	if err := pool.CloseSession("session-1"); err != nil {
		t.Fatalf("CloseSession() error = %v", err)
	}
	if approval.cancelCount != 0 || userInput.cancelCount != 0 {
		t.Fatalf("ensure-only cleanup reached session decisions: approval=%d user_input=%d", approval.cancelCount, userInput.cancelCount)
	}
}

func TestPendingDecisionCleanupRunsServicesIndependently(t *testing.T) {
	t.Parallel()

	approvalStarted := make(chan struct{}, 1)
	approvalRelease := make(chan struct{})
	inputStarted := make(chan struct{}, 1)
	approval := &fakeToolApprovalService{cancelStarted: approvalStarted, cancelRelease: approvalRelease}
	userInput := &fakeUserInputCanceller{cancelStarted: inputStarted}
	pool := newSessionPool(nil, nil, fakeBotGetter{})
	pool.SetToolApprovalService(approval)
	pool.SetUserInputService(userInput)
	done := make(chan struct{})
	go func() {
		pool.cancelPendingDecisions(context.Background(), "bot-1", "session-1", "cleanup")
		close(done)
	}()

	select {
	case <-approvalStarted:
	case <-time.After(time.Second):
		t.Fatal("approval cleanup did not start")
	}
	select {
	case <-inputStarted:
	case <-time.After(time.Second):
		t.Fatal("user input cleanup waited for blocked approval cleanup")
	}
	close(approvalRelease)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("decision cleanup did not finish")
	}
}

func TestCloseSessionCarriesLatestRuntimeFenceToDecisionCleanup(t *testing.T) {
	want := runtimefence.Fence{BotID: "bot-1", SessionID: "session-1", Token: 37}
	approval := &fakeToolApprovalService{}
	userInput := &fakeUserInputCanceller{}
	pool := newSessionPool(nil, nil, fakeBotGetter{})
	pool.SetToolApprovalService(approval)
	pool.SetUserInputService(userInput)
	injectRuntime(pool, &runtimeHandle{
		id:               "rt-fenced-cleanup",
		botID:            want.BotID,
		status:           stateIdle,
		boundSession:     want.SessionID,
		persistenceFence: want,
		lastActive:       time.Now(),
		hadPrompt:        true,
	})

	if err := pool.CloseSession(want.SessionID); err != nil {
		t.Fatalf("CloseSession() error = %v", err)
	}
	if approval.cancelFence != want || userInput.cancelFence != want {
		t.Fatalf("decision cleanup fences = approval:%#v user_input:%#v, want %#v", approval.cancelFence, userInput.cancelFence, want)
	}
	if approval.cancelCount != 2 || userInput.cancelCount != 2 {
		t.Fatalf("fenced cleanup count = approval:%d user_input:%d, want pre and final cleanup", approval.cancelCount, userInput.cancelCount)
	}
}

func TestStaleRuntimeHandleDoesNotCancelNewHandleDecisions(t *testing.T) {
	approval := &fakeToolApprovalService{}
	userInput := &fakeUserInputCanceller{}
	pool := newSessionPool(nil, nil, fakeBotGetter{})
	pool.SetToolApprovalService(approval)
	pool.SetUserInputService(userInput)
	old := &runtimeHandle{id: "rt-old", botID: "bot-1", boundSession: "session-1", status: stateIdle, lastActive: time.Now(), hadPrompt: true}
	current := &runtimeHandle{id: "rt-current", botID: "bot-1", boundSession: "session-1", status: stateIdle, lastActive: time.Now(), hadPrompt: true}
	injectRuntime(pool, old)
	injectRuntime(pool, current)

	if err := pool.closeHandle(old); err != nil {
		t.Fatalf("close stale handle: %v", err)
	}
	if approval.cancelCount != 0 || userInput.cancelCount != 0 {
		t.Fatalf("stale cleanup reached current decisions: approval=%d user_input=%d", approval.cancelCount, userInput.cancelCount)
	}
}

type fakeBotGetter struct {
	bot bots.Bot
	err error
}

func (g fakeBotGetter) Get(context.Context, string) (bots.Bot, error) {
	return g.bot, g.err
}

type fakeSessionGetter struct {
	session SessionDescriptor
	err     error
}

func (g fakeSessionGetter) Get(context.Context, string) (SessionDescriptor, error) {
	return g.session, g.err
}

type fakeToolApprovalService struct {
	cancelBotID     string
	cancelSessionID string
	cancelReason    string
	cancelCount     int
	cancelFence     runtimefence.Fence
	cancelStarted   chan<- struct{}
	cancelRelease   <-chan struct{}
}

func (*fakeToolApprovalService) EvaluatePolicy(context.Context, toolapproval.CreatePendingInput) (toolapproval.Evaluation, error) {
	return toolapproval.Evaluation{Decision: toolapproval.DecisionBypass}, nil
}

func (*fakeToolApprovalService) CreatePending(context.Context, toolapproval.CreatePendingInput) (toolapproval.Request, error) {
	return toolapproval.Request{}, nil
}

func (*fakeToolApprovalService) Get(context.Context, string) (toolapproval.Request, error) {
	return toolapproval.Request{}, toolapproval.ErrNotFound
}

func (*fakeToolApprovalService) Reject(context.Context, string, string, string) (toolapproval.Request, error) {
	return toolapproval.Request{}, nil
}

func (*fakeToolApprovalService) WaitForDecision(context.Context, string) (toolapproval.Request, error) {
	return toolapproval.Request{}, nil
}

func (*fakeToolApprovalService) RegisterWaiter(string) func() {
	return func() {}
}

func (f *fakeToolApprovalService) CancelPendingForSession(ctx context.Context, botID, sessionID, reason string) ([]toolapproval.Request, error) {
	if f.cancelStarted != nil {
		f.cancelStarted <- struct{}{}
	}
	if f.cancelRelease != nil {
		<-f.cancelRelease
	}
	f.cancelBotID = botID
	f.cancelSessionID = sessionID
	f.cancelReason = reason
	f.cancelCount++
	f.cancelFence, _ = runtimefence.FromContext(ctx)
	return nil, nil
}

type fakeUserInputCanceller struct {
	cancelBotID     string
	cancelSessionID string
	cancelReason    string
	cancelCount     int
	cancelFence     runtimefence.Fence
	cancelStarted   chan<- struct{}
}

func (f *fakeUserInputCanceller) CancelPendingForSession(ctx context.Context, botID, sessionID, reason string) ([]userinput.Request, error) {
	if f.cancelStarted != nil {
		f.cancelStarted <- struct{}{}
	}
	f.cancelBotID = botID
	f.cancelSessionID = sessionID
	f.cancelReason = reason
	f.cancelCount++
	f.cancelFence, _ = runtimefence.FromContext(ctx)
	return nil, nil
}

type recordingRunner struct {
	info     bridge.WorkspaceInfo
	req      client.StartRequest
	startErr error
}

type dynamicStartResult struct {
	session *client.Session
	err     error
}

type dynamicRecordingRunner struct {
	mu             sync.Mutex
	info           bridge.WorkspaceInfo
	versions       []string
	resolveErrs    []error
	resolveCalls   int
	blockResolve   bool
	resolveStarted chan struct{}
	resolveRelease <-chan struct{}
	resolveOnce    sync.Once
	reqs           []client.StartRequest
	starts         []dynamicStartResult
	blockDynamic   bool
	dynamicStarted chan struct{}
	dynamicRelease <-chan struct{}
	startOnce      sync.Once
}

type caBundleRunner struct {
	client *bridge.Client
}

type caBundleStatServer struct {
	pb.UnimplementedContainerServiceServer
	mu   sync.Mutex
	path string
}

type hermesRecordingRunner struct {
	info     bridge.WorkspaceInfo
	client   *bridge.Client
	req      client.StartRequest
	startErr error
}

type blockingRunner struct {
	info    bridge.WorkspaceInfo
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type delayedStartRunner struct {
	info    bridge.WorkspaceInfo
	started chan struct{}
	release chan struct{}
	session *client.Session
}

type cancelAwareStartRunner struct {
	info      bridge.WorkspaceInfo
	started   chan struct{}
	cancelled chan struct{}
}

func (r *blockingRunner) WorkspaceInfo(context.Context, string) (bridge.WorkspaceInfo, error) {
	return r.info, nil
}

func (r *blockingRunner) StartSession(context.Context, client.StartRequest, client.EventSink) (*client.Session, error) {
	r.once.Do(func() { close(r.started) })
	<-r.release
	return nil, errors.New("released")
}

func (r *delayedStartRunner) WorkspaceInfo(context.Context, string) (bridge.WorkspaceInfo, error) {
	return r.info, nil
}

func (r *delayedStartRunner) StartSession(context.Context, client.StartRequest, client.EventSink) (*client.Session, error) {
	close(r.started)
	<-r.release
	return r.session, nil
}

func (r *cancelAwareStartRunner) WorkspaceInfo(context.Context, string) (bridge.WorkspaceInfo, error) {
	return r.info, nil
}

func (r *cancelAwareStartRunner) StartSession(ctx context.Context, _ client.StartRequest, _ client.EventSink) (*client.Session, error) {
	close(r.started)
	<-ctx.Done()
	close(r.cancelled)
	return nil, ctx.Err()
}

func (r *recordingRunner) WorkspaceInfo(context.Context, string) (bridge.WorkspaceInfo, error) {
	return r.info, nil
}

func (r *recordingRunner) StartSession(_ context.Context, req client.StartRequest, _ client.EventSink) (*client.Session, error) {
	r.req = req
	return nil, r.startErr
}

func (r *dynamicRecordingRunner) WorkspaceInfo(context.Context, string) (bridge.WorkspaceInfo, error) {
	return r.info, nil
}

func (r *dynamicRecordingRunner) ResolveACPAdapterVersion(ctx context.Context, _ string, _ string, _ []string) (string, error) {
	r.mu.Lock()
	r.resolveCalls++
	block := r.blockResolve
	started := r.resolveStarted
	release := r.resolveRelease
	r.mu.Unlock()
	if block {
		r.resolveOnce.Do(func() {
			if started != nil {
				close(started)
			}
		})
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-release:
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	var err error
	if len(r.resolveErrs) > 0 {
		err = r.resolveErrs[0]
		r.resolveErrs = r.resolveErrs[1:]
	}
	if err != nil {
		return "", err
	}
	if len(r.versions) == 0 {
		return "", errors.New("no fake npm version configured")
	}
	version := r.versions[0]
	r.versions = r.versions[1:]
	return version, nil
}

func (r *dynamicRecordingRunner) StartSession(ctx context.Context, req client.StartRequest, _ client.EventSink) (*client.Session, error) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	block := r.blockDynamic && req.Command == "npx"
	r.mu.Unlock()
	if block {
		r.startOnce.Do(func() {
			if r.dynamicStarted != nil {
				close(r.dynamicStarted)
			}
		})
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-r.dynamicRelease:
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.starts) == 0 {
		return nil, errors.New("no fake session start result configured")
	}
	result := r.starts[0]
	r.starts = r.starts[1:]
	return result.session, result.err
}

func (r *dynamicRecordingRunner) requests() []client.StartRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]client.StartRequest(nil), r.reqs...)
}

func (r *dynamicRecordingRunner) resolveCallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.resolveCalls
}

func resolveAdapterVersionForTest(pool *SessionPool, botID, packageName string) (string, error) {
	_, version, err := pool.resolveDynamicAdapter(context.Background(), botID, packageName, nil)
	return version, err
}

func (*caBundleRunner) WorkspaceInfo(context.Context, string) (bridge.WorkspaceInfo, error) {
	return bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"}, nil
}

func (*caBundleRunner) StartSession(context.Context, client.StartRequest, client.EventSink) (*client.Session, error) {
	return nil, errors.New("not implemented")
}

func (r *caBundleRunner) MCPClient(context.Context, string) (*bridge.Client, error) {
	return r.client, nil
}

func (s *caBundleStatServer) Stat(_ context.Context, req *pb.StatRequest) (*pb.StatResponse, error) {
	s.mu.Lock()
	s.path = req.GetPath()
	s.mu.Unlock()
	return &pb.StatResponse{Entry: &pb.FileEntry{Path: filepath.Base(req.GetPath())}}, nil
}

func (r *hermesRecordingRunner) WorkspaceInfo(context.Context, string) (bridge.WorkspaceInfo, error) {
	return r.info, nil
}

func (r *hermesRecordingRunner) MCPClient(context.Context, string) (*bridge.Client, error) {
	return r.client, nil
}

func (r *hermesRecordingRunner) StartSession(_ context.Context, req client.StartRequest, _ client.EventSink) (*client.Session, error) {
	r.req = req
	return nil, r.startErr
}

type sessionPoolWorkspace struct {
	client *bridge.Client
	info   bridge.WorkspaceInfo
}

func (w sessionPoolWorkspace) MCPClient(context.Context, string) (*bridge.Client, error) {
	return w.client, nil
}

func (w sessionPoolWorkspace) WorkspaceInfo(context.Context, string) (bridge.WorkspaceInfo, error) {
	return w.info, nil
}

func enabledACPBot(id, mode string, managed map[string]any) bots.Bot {
	return enabledACPAgentBot(id, acpprofile.AgentCodexID, mode, managed)
}

func enabledACPAgentBot(id, agentID, mode string, managed map[string]any) bots.Bot {
	if managed == nil {
		managed = map[string]any{}
	}
	return bots.Bot{
		ID: id,
		Metadata: map[string]any{
			"acp": map[string]any{
				"agents": map[string]any{
					agentID: map[string]any{
						"enabled":    true,
						"setup_mode": mode,
						"managed":    managed,
					},
				},
			},
		},
	}
}

func startRequestEnvHas(env []string, key, want string) bool {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix) == want
		}
	}
	return false
}

func hasString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func newSessionPoolBridgeClient(t *testing.T, root string) *bridge.Client {
	t.Helper()
	listener := bufconn.Listen(16 * 1024 * 1024)
	server := grpc.NewServer(
		grpc.MaxRecvMsgSize(16*1024*1024),
		grpc.MaxSendMsgSize(16*1024*1024),
	)
	bridgeServer := bridgesvc.New(bridgesvc.Options{
		DefaultWorkDir:    root,
		WorkspaceRoot:     root,
		DataMount:         config.DefaultDataMount,
		AllowHostAbsolute: true,
	})
	pb.RegisterContainerServiceServer(server, &sessionPoolBridgeServer{
		Server: bridgeServer,
		binDir: filepath.Join(root, "bin"),
	})
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	conn, err := grpc.NewClient("passthrough:///acpagent-sessionpool-test",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(16*1024*1024),
			grpc.MaxCallSendMsgSize(16*1024*1024),
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return bridge.NewClientFromConn(conn)
}

type sessionPoolBridgeServer struct {
	*bridgesvc.Server
	binDir string
}

func (s *sessionPoolBridgeServer) Exec(stream pb.ContainerService_ExecServer) error {
	return s.Server.Exec(&sessionPoolExecStream{
		ContainerService_ExecServer: stream,
		binDir:                      s.binDir,
	})
}

type sessionPoolExecStream struct {
	pb.ContainerService_ExecServer
	binDir string
	first  bool
}

func (s *sessionPoolExecStream) Recv() (*pb.ExecInput, error) {
	input, err := s.ContainerService_ExecServer.Recv()
	if err != nil || s.first {
		return input, err
	}
	s.first = true
	input.Command = strings.ReplaceAll(input.Command, "/opt/memoh/toolkit/bin", s.binDir)
	for index, item := range input.Env {
		input.Env[index] = strings.ReplaceAll(item, "/opt/memoh/toolkit/bin", s.binDir)
	}
	return input, nil
}

func newCABundleStatClient(t *testing.T) (*bridge.Client, *caBundleStatServer) {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	statServer := &caBundleStatServer{}
	pb.RegisterContainerServiceServer(server, statServer)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	conn, err := grpc.NewClient("passthrough:///acpagent-ca-bundle-test",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return bridge.NewClientFromConn(conn), statServer
}

func readSessionPoolFile(t *testing.T, root string, parts ...string) string {
	t.Helper()
	pathParts := append([]string{root}, parts...)
	content, err := os.ReadFile(filepath.Join(pathParts...)) //nolint:gosec // reads from t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func waitForSessionPoolFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func writeSessionPoolFakeAgentScript(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	script := fmt.Sprintf("#!/bin/sh\nif [ -n \"${MEMOH_ACP_START_LOG:-}\" ]; then printf 'start\\n' >> \"$MEMOH_ACP_START_LOG\"; fi\nMEMOH_ACP_SESSION_POOL_FAKE_AGENT=1 exec %s -test.run '^TestSessionPoolFakeAgentHelper$' --\n", sessionPoolShellArg(os.Args[0]))
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil { //nolint:gosec // test helper must be executable.
		t.Fatal(err)
	}
	return path
}

func TestSessionPoolFakeAgentHelper(_ *testing.T) {
	if os.Getenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT") != "1" {
		return
	}
	agent := &sessionPoolFakeAgent{}
	conn := acp.NewAgentSideConnection(agent, os.Stdout, os.Stdin)
	agent.conn = conn
	<-conn.Done()
	os.Exit(0)
}

type sessionPoolFakeAgent struct {
	conn            *acp.AgentSideConnection
	modelID         string
	reasoningEffort string
}

func (*sessionPoolFakeAgent) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

func (*sessionPoolFakeAgent) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, nil
}

func (*sessionPoolFakeAgent) Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error) {
	capabilities := acp.AgentCapabilities{LoadSession: false}
	if os.Getenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_IMAGE") == "1" {
		capabilities.PromptCapabilities.Image = true
	}
	return acp.InitializeResponse{
		ProtocolVersion:   acp.ProtocolVersionNumber,
		AgentCapabilities: capabilities,
	}, nil
}

func (*sessionPoolFakeAgent) Cancel(context.Context, acp.CancelNotification) error { return nil }

func (*sessionPoolFakeAgent) CloseSession(context.Context, acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, nil
}

func (*sessionPoolFakeAgent) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, nil
}

func (a *sessionPoolFakeAgent) NewSession(context.Context, acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	resp := acp.NewSessionResponse{SessionId: acp.SessionId("session-pool-fake-session")}
	if os.Getenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_MODELS") == "1" {
		a.modelID = "gpt-5.1-codex"
	}
	if os.Getenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_REASONING") == "1" {
		a.reasoningEffort = "high"
	}
	resp.ConfigOptions = a.configOptions()
	return resp, nil
}

func (a *sessionPoolFakeAgent) Prompt(ctx context.Context, p acp.PromptRequest) (acp.PromptResponse, error) {
	if os.Getenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_HANG_PROMPT") == "1" {
		if path := os.Getenv("MEMOH_ACP_PROMPT_STARTED_FILE"); path != "" {
			_ = os.WriteFile(path, []byte("started"), 0o600) //nolint:gosec // test helper writes to env-provided temp path.
		}
		<-ctx.Done()
		if path := os.Getenv("MEMOH_ACP_PROMPT_CANCELLED_FILE"); path != "" {
			_ = os.WriteFile(path, []byte("cancelled"), 0o600) //nolint:gosec // test helper writes to env-provided temp path.
		}
		return acp.PromptResponse{}, ctx.Err()
	}
	if os.Getenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_EXPECT_IMAGE") == "1" {
		if len(p.Prompt) != 1 || p.Prompt[0].Image == nil {
			return acp.PromptResponse{}, fmt.Errorf("prompt blocks = %#v, want one image", p.Prompt)
		}
		image := p.Prompt[0].Image
		if image.Data != "aW1hZ2U=" || image.MimeType != "image/png" {
			return acp.PromptResponse{}, fmt.Errorf("image block = %#v, want inline PNG", image)
		}
	}
	a.appendConfigLog(fmt.Sprintf("prompt:model=%s,reasoning=%s", a.modelID, a.reasoningEffort))
	_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{
		SessionId: p.SessionId,
		Update:    acp.UpdateAgentMessageText("session-pool-ok"),
	})
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func (*sessionPoolFakeAgent) ResumeSession(context.Context, acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, nil
}

func (a *sessionPoolFakeAgent) SetSessionConfigOption(_ context.Context, p acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	if p.ValueId == nil || p.ValueId.SessionId != acp.SessionId("session-pool-fake-session") {
		return acp.SetSessionConfigOptionResponse{}, errors.New("unexpected config request")
	}
	value := string(p.ValueId.Value)
	if strings.TrimSpace(os.Getenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_CONFIG_FAIL")) == string(p.ValueId.ConfigId) {
		return acp.SetSessionConfigOptionResponse{}, errors.New("injected config transport failure")
	}
	switch string(p.ValueId.ConfigId) {
	case "model":
		if value != "gpt-5.1-codex" && value != "gpt-5.1-codex-high" {
			return acp.SetSessionConfigOptionResponse{}, errors.New("unsupported model")
		}
		a.modelID = value
		if os.Getenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_MODEL_RESETS_REASONING") == "1" {
			a.reasoningEffort = "low"
		}
	case "thinking":
		if value != "low" && value != "high" && value != "xhigh" {
			return acp.SetSessionConfigOptionResponse{}, errors.New("unsupported reasoning effort")
		}
		a.reasoningEffort = value
	default:
		return acp.SetSessionConfigOptionResponse{}, errors.New("unexpected config id")
	}
	a.appendConfigLog(fmt.Sprintf("config:%s=%s", p.ValueId.ConfigId, value))
	return acp.SetSessionConfigOptionResponse{ConfigOptions: a.configOptions()}, nil
}

func (*sessionPoolFakeAgent) appendConfigLog(line string) {
	path := strings.TrimSpace(os.Getenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_CONFIG_LOG"))
	if path == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // test helper writes to a temp path supplied by the test.
	if err != nil {
		return
	}
	_, _ = fmt.Fprintln(file, line)
	_ = file.Close()
}

func (a *sessionPoolFakeAgent) reasoningConfigOptions() []acp.SessionConfigOption {
	category := acp.SessionConfigOptionCategoryThoughtLevel
	options := acp.SessionConfigSelectOptionsUngrouped{
		{Value: acp.SessionConfigValueId("low"), Name: "Low"},
		{Value: acp.SessionConfigValueId("high"), Name: "High"},
		{Value: acp.SessionConfigValueId("xhigh"), Name: "X-High"},
	}
	return []acp.SessionConfigOption{{Select: &acp.SessionConfigOptionSelect{
		Id:           acp.SessionConfigId("thinking"),
		Name:         "Reasoning",
		Type:         "select",
		Category:     &category,
		CurrentValue: acp.SessionConfigValueId(a.reasoningEffort),
		Options:      acp.SessionConfigSelectOptions{Ungrouped: &options},
	}}}
}

func (a *sessionPoolFakeAgent) configOptions() []acp.SessionConfigOption {
	var options []acp.SessionConfigOption
	if os.Getenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_MODELS") == "1" {
		category := acp.SessionConfigOptionCategoryModel
		description := "Highest reasoning"
		models := acp.SessionConfigSelectOptionsUngrouped{
			{Value: acp.SessionConfigValueId("gpt-5.1-codex"), Name: "GPT-5.1 Codex"},
			{Value: acp.SessionConfigValueId("gpt-5.1-codex-high"), Name: "GPT-5.1 Codex High", Description: &description},
		}
		options = append(options, acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
			Id:           acp.SessionConfigId("model"),
			Name:         "Model",
			Type:         "select",
			Category:     &category,
			CurrentValue: acp.SessionConfigValueId(a.modelID),
			Options:      acp.SessionConfigSelectOptions{Ungrouped: &models},
		}})
	}
	if os.Getenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_REASONING") == "1" {
		options = append(options, a.reasoningConfigOptions()...)
	}
	return options
}

func (*sessionPoolFakeAgent) SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}

func sessionPoolShellArg(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
