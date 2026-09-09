package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/decision/feedback"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/acp/client"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/agent/runtime/agentstate"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/runtimefence"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspace/bridgesvc"
)

// injectRuntime registers a hand-built handle for tests that exercise
// internal state without booting a real agent process.
func injectRuntime(p *SessionPool, h *runtimeHandle) {
	if h.ownerCtx == nil {
		h.ownerCtx = context.Background()
	}
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
		AgentID:               acpprofile.AgentACPID,
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
		AgentID:               acpprofile.AgentACPID,
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

func TestSessionPoolRecordsResetHeadAfterSuccessfulPrompt(t *testing.T) {
	pool := newFakeScriptPool(t)
	pool.timeout = time.Hour
	store := &recordingSessionStateStore{}
	pool.SetSessionStateStore(store)

	runID := uuid.NewString()
	if _, err := pool.Prompt(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		RunID:                 runID,
		AgentID:               acpprofile.AgentACPID,
		ProjectPath:           "/data/project",
		Prompt:                "advance the native session",
		RuntimeOwnerAccountID: "user-1",
	}); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	handle := pool.sessionHandle("session-1")
	if handle == nil {
		t.Fatal("prompt did not bind a runtime")
	}
	handle.state.Lock()
	nativeHead, nativeHeadFound := handle.nativeHead, handle.nativeHeadFound
	handle.state.Unlock()
	// No snapshots are captured: every completed turn records a reset head so
	// warm-handle fencing still tracks canonical history per turn.
	if !nativeHeadFound || nativeHead.RunID != runID || nativeHead.Kind != agentstate.SessionPublicationReset {
		t.Fatalf("native head = %#v found=%v, want reset head for run", nativeHead, nativeHeadFound)
	}
	store.mu.Lock()
	replaceCalls := store.replaceCalls
	store.mu.Unlock()
	if replaceCalls != 0 {
		t.Fatalf("store.Replace was called %d times, want none", replaceCalls)
	}
}

func TestSessionPoolPromptSupportsImageOnly(t *testing.T) {
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_IMAGE", "1")
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_EXPECT_IMAGE", "1")
	pool := newFakeScriptPool(t)

	result, err := pool.Prompt(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               acpprofile.AgentACPID,
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
		AgentID:               acpprofile.AgentACPID,
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
		AgentID:                  acpprofile.AgentACPID,
		ProjectPath:              "/data/project",
		Prompt:                   "inspect the image",
		Images:                   []client.PromptImage{{Data: "aW1hZ2U=", MimeType: "image/png"}},
		AttachmentReferences:     []string{"/data/.memoh/media/aa/image.png"},
		CanFallbackImagesToFiles: true,
		ContextURI:               "memoh://context/current-turn",
		ContextMarkdown:          "Attachment path: /data/.memoh/media/aa/image.png",
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
		AgentID:               acpprofile.AgentACPID,
		ProjectPath:           "/data/project",
		AttachmentReferences:  []string{"/data/.memoh/media/aa/pasted-text.txt"},
		ContextURI:            "memoh://context/current-turn",
		ContextMarkdown:       "Attachment path: /data/.memoh/media/aa/pasted-text.txt",
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
		AgentID:   acpprofile.AgentACPID,
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
		AgentID:               acpprofile.AgentACPID,
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

func TestSessionPoolCreateRuntimeGeneratesIDAndReportsModels(t *testing.T) {
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_MODELS", "1")
	pool := newFakeScriptPool(t)

	status, err := pool.CreateRuntime(context.Background(), CreateRuntimeInput{
		BotID:                 "bot-1",
		AgentID:               acpprofile.AgentACPID,
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
	type contextKey struct{}

	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_MODELS", "1")
	pool := newFakeScriptPool(t)

	created, err := pool.CreateRuntime(context.Background(), CreateRuntimeInput{
		BotID:                 "bot-1",
		AgentID:               acpprofile.AgentACPID,
		ProjectPath:           "/data/project",
		RuntimeOwnerAccountID: "user-1",
	})
	if err != nil {
		t.Fatalf("CreateRuntime() error = %v", err)
	}
	if _, err := pool.SetRuntimeModel(context.Background(), "bot-1", created.RuntimeID, "gpt-5.1-codex-high"); err != nil {
		t.Fatalf("SetRuntimeModel() error = %v", err)
	}

	bindCtx, cancelBind := context.WithCancel(
		context.WithValue(context.Background(), contextKey{}, "bind-scope"),
	)
	defer cancelBind()
	if err := pool.BindRuntime(bindCtx, "bot-1", created.RuntimeID, "session-1", acpprofile.AgentACPID, "/data/project", "user-1"); err != nil {
		t.Fatalf("BindRuntime() error = %v", err)
	}
	cancelBind()
	h := pool.sessionHandle("session-1")
	if h == nil || h.id != created.RuntimeID {
		t.Fatalf("session index does not point at the bound runtime")
	}
	h.state.Lock()
	ownerCtx := h.ownerCtx
	h.state.Unlock()
	if ownerCtx == nil {
		t.Fatal("bound runtime owner context is nil")
	}
	if got := ownerCtx.Value(contextKey{}); got != "bind-scope" {
		t.Fatalf("bound runtime owner context value = %v, want bind-scope", got)
	}
	if err := ownerCtx.Err(); err != nil {
		t.Fatalf("bound runtime owner context error = %v, want request cancellation detached", err)
	}

	// The bound session reuses the warm process - including its model.
	status, err := pool.Ensure(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               acpprofile.AgentACPID,
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
	if err := pool.BindRuntime(context.Background(), "bot-1", created.RuntimeID, "session-2", acpprofile.AgentACPID, "/data/project", "user-1"); !errors.Is(err, ErrRuntimeBindRejected) {
		t.Fatalf("second BindRuntime() error = %v, want ErrRuntimeBindRejected", err)
	}
}

func TestSessionPoolSetRuntimeModelEmptyResetsToDefault(t *testing.T) {
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_MODELS", "1")
	pool := newFakeScriptPool(t)

	created, err := pool.CreateRuntime(context.Background(), CreateRuntimeInput{
		BotID:                 "bot-1",
		AgentID:               acpprofile.AgentACPID,
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
		AgentID:               acpprofile.AgentACPID,
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
		agentID:               acpprofile.AgentACPID,
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
		{"cross bot", "bot-1", "real", acpprofile.AgentACPID, "/data", ErrRuntimeNotFound},
		{"wrong agent", "bot-2", "real", "other-agent", "/data", ErrRuntimeBindRejected},
		{"wrong project", "bot-2", "real", acpprofile.AgentACPID, "/other", ErrRuntimeBindRejected},
	}
	for _, tc := range cases {
		if err := pool.BindRuntime(context.Background(), tc.botID, pending.id, tc.sessionID, tc.agent, tc.path, "user-1"); !errors.Is(err, tc.wantErr) {
			t.Fatalf("%s: BindRuntime() error = %v, want %v", tc.name, err, tc.wantErr)
		}
	}
	if err := pool.BindRuntime(context.Background(), "bot-2", "rt_missing", "real", acpprofile.AgentACPID, "/data", "user-1"); !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("missing runtime: BindRuntime() error = %v, want ErrRuntimeNotFound", err)
	}

	// Session already served by another runtime.
	other := &runtimeHandle{id: newRuntimeID(), botID: "bot-2", boundSession: "real", status: stateIdle}
	injectRuntime(pool, other)
	if err := pool.BindRuntime(context.Background(), "bot-2", pending.id, "real", acpprofile.AgentACPID, "/data", "user-1"); !errors.Is(err, ErrRuntimeBindRejected) {
		t.Fatalf("occupied session: BindRuntime() error = %v, want ErrRuntimeBindRejected", err)
	}

	// A still-starting runtime (no live process yet) is not bindable.
	starting := &runtimeHandle{id: newRuntimeID(), botID: "bot-2", agentID: acpprofile.AgentACPID, projectPath: "/data", status: stateStarting}
	injectRuntime(pool, starting)
	if err := pool.BindRuntime(context.Background(), "bot-2", starting.id, "real-2", acpprofile.AgentACPID, "/data", "user-1"); !errors.Is(err, ErrRuntimeBindRejected) {
		t.Fatalf("starting runtime: BindRuntime() error = %v, want ErrRuntimeBindRejected", err)
	}

	// Everything matching succeeds.
	if err := pool.BindRuntime(context.Background(), "bot-2", pending.id, "real-2", acpprofile.AgentACPID, "/data", "user-1"); err != nil {
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
		agentID:      acpprofile.AgentACPID,
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
	if err := pool.BindRuntime(context.Background(), "bot-1", foreign.id, "my-session", acpprofile.AgentACPID, "/data", "user-1"); !errors.Is(err, ErrRuntimeNotFound) {
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
		agentID:      acpprofile.AgentACPID,
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
		done <- pool.CloseBotAgentRuntimes("bot-1", acpprofile.AgentACPID)
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
			agentID:    acpprofile.AgentACPID,
			status:     stateIdle,
			lastActive: now.Add(-time.Duration(i+1) * time.Minute),
		})
	}
	// Bound and other-bot runtimes must not count toward the cap.
	injectRuntime(pool, &runtimeHandle{id: "rt_bound", botID: "bot-1", boundSession: "session-9", status: stateIdle, lastActive: now})
	injectRuntime(pool, &runtimeHandle{id: "rt_other-bot", botID: "bot-9", status: stateIdle, lastActive: now.Add(-time.Minute)})

	created, err := pool.CreateRuntime(context.Background(), CreateRuntimeInput{
		BotID:                 "bot-1",
		AgentID:               acpprofile.AgentACPID,
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
		AgentID:               acpprofile.AgentACPID,
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
		agentID:      acpprofile.AgentACPID,
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
			AgentID:               acpprofile.AgentACPID,
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
	if replaced == nil || replaced.session == nil || replaced.agentID != acpprofile.AgentACPID {
		t.Fatalf("replaced runtime = %#v, want fresh codex runtime", replaced)
	}
}

func TestSessionPoolSetModelUpdatesRuntimeModel(t *testing.T) {
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_MODELS", "1")
	pool := newFakeScriptPool(t)

	status, err := pool.SetModel(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               acpprofile.AgentACPID,
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
		AgentID:               acpprofile.AgentACPID,
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
		AgentID:               acpprofile.AgentACPID,
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
	}; !slices.Equal(got, want) {
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
	}; !slices.Equal(got, want) {
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
	}; !slices.Equal(got, want) {
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
		AgentID:               acpprofile.AgentACPID,
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
		AgentID:               acpprofile.AgentACPID,
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
		agentID:      acpprofile.AgentACPID,
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
		agentID:      acpprofile.AgentACPID,
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
			AgentID:               acpprofile.AgentACPID,
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
	status = pool.RuntimeStatus("session-1", acpprofile.AgentACPID, "/data/project")
	if status.State != "idle" || status.ACPSession != "" {
		t.Fatalf("RuntimeStatus after failed start = %#v, want idle without process", status)
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
			AgentID:               acpprofile.AgentACPID,
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
			AgentID:               acpprofile.AgentACPID,
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
			AgentID:               acpprofile.AgentACPID,
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
				AgentID:               acpprofile.AgentACPID,
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
	// Managed-mode validation is declaration-driven: the generic profile
	// requires an explicit command before any process may start.
	missingCommand := newSessionPool(nil, &recordingRunner{
		info: bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"},
	}, fakeBotGetter{bot: enabledACPAgentBot("bot-1", acpprofile.AgentACPID, "api_key", map[string]any{"command": ""})})
	_, err := missingCommand.Prompt(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               acpprofile.AgentACPID,
		ProjectPath:           "/data/project",
		Prompt:                "run",
		RuntimeOwnerAccountID: "user-1",
	})
	var feedbackErr *feedback.Error
	if !errors.As(err, &feedbackErr) || feedbackErr.Code != feedback.CodeAgentNotConfigured || !strings.Contains(feedbackErr.Message, "command required") {
		t.Fatalf("missing command error = %v", err)
	}

	configuredRunner := &recordingRunner{
		info:     bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"},
		startErr: errors.New("started"),
	}
	configured := newSessionPool(nil, configuredRunner, fakeBotGetter{bot: enabledACPAgentBot("bot-1", acpprofile.AgentACPID, "api_key", map[string]any{"command": "my-agent-acp"})})
	if _, err := configured.Prompt(context.Background(), PromptInput{
		BotID:                 "bot-1",
		SessionID:             "session-1",
		AgentID:               acpprofile.AgentACPID,
		ProjectPath:           "/data/project",
		Prompt:                "run",
		RuntimeOwnerAccountID: "user-1",
	}); err == nil || err.Error() != "started" {
		t.Fatalf("configured generic agent error = %v, want runner start error", err)
	}
	if configuredRunner.req.Command != "my-agent-acp" {
		t.Fatalf("resolved command = %q, want managed command", configuredRunner.req.Command)
	}
}

func TestSessionPoolRejectsUnsupportedSetupMode(t *testing.T) {
	runner := &recordingRunner{
		info:     bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"},
		startErr: errors.New("started"),
	}
	pool := newSessionPool(nil, runner, fakeBotGetter{bot: enabledACPAgentBot("bot-1", acpprofile.AgentACPID, "oauth", map[string]any{
		"oauth_token": "fake",
	})})
	_, err := pool.Prompt(context.Background(), PromptInput{
		BotID:     "bot-1",
		SessionID: "session-1",
		AgentID:   acpprofile.AgentACPID,
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
	pool := newSessionPool(nil, runner, fakeBotGetter{bot: enabledACPAgentBot("bot-1", acpprofile.AgentACPID, "api_key", nil)})
	_, err := pool.Prompt(context.Background(), PromptInput{
		BotID:     "bot-1",
		SessionID: "session-1",
		AgentID:   acpprofile.AgentACPID,
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
				"acp_agent_id":             acpprofile.AgentACPID,
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
	if runner.req.AgentID != acpprofile.AgentACPID {
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
		AgentID:               acpprofile.AgentACPID,
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
	policy := &contextfrag.ToolExchangePolicy{MinMessages: 7}
	wantFence := runtimefence.Fence{BotID: "bot-1", SessionID: "session-1", Token: 29}
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	guardCalls := 0
	active := client.ToolSessionContext{
		ChatID:                    "chat-1",
		SessionID:                 "session-1",
		RunID:                     "stream-7",
		SessionToken:              "token-7",
		CurrentPlatform:           "web",
		ReplyTarget:               "reply-7",
		ConversationType:          "private",
		ReasoningStoredEffort:     "low",
		ReasoningRequestedEffort:  "high",
		SupportsImageInput:        true,
		ContextBudgetMaxTokens:    128000,
		ContextToolExchangePolicy: policy,
		RuntimeFence:              wantFence,
		RunContext:                runCtx,
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
	if ctx.ReasoningStoredEffort != "low" || ctx.ReasoningRequestedEffort != "high" {
		t.Fatalf("active tool context reasoning intent = stored %q, requested %q",
			ctx.ReasoningStoredEffort, ctx.ReasoningRequestedEffort)
	}
	if ctx.ContextBudgetMaxTokens != 128000 {
		t.Fatalf("active tool context lost context budget: %#v", ctx)
	}
	if ctx.ContextToolExchangePolicy != policy {
		t.Fatalf("active tool context lost tool exchange policy: %#v", ctx)
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
	if ctx.RunID != "" || ctx.SessionToken != "" || ctx.ChatID != "bot-1" || ctx.RuntimeActive || ctx.SupportsImageInput || !ctx.CanListUserInput || ctx.RunContext != nil || ctx.RuntimeGuard != nil || ctx.ReasoningStoredEffort != "" || ctx.ReasoningRequestedEffort != "" {
		t.Fatalf("cleared tool context = %#v", ctx)
	}
	if ctx.ContextBudgetMaxTokens != 0 || ctx.ContextToolExchangePolicy != nil {
		t.Fatalf("cleared tool context leaks context budget fields: %#v", ctx)
	}
}

func TestToolSessionContextCopiesContextBudgetFields(t *testing.T) {
	h := &runtimeHandle{id: "rt_test", botID: "bot-1"}
	policy := &contextfrag.ToolExchangePolicy{MinMessages: 3}

	got := toolSessionContext(context.Background(), PromptInput{
		BotID:                     "bot-1",
		ContextBudgetMaxTokens:    5000,
		ContextToolExchangePolicy: policy,
	}, h)

	if got.ContextBudgetMaxTokens != 5000 {
		t.Fatalf("ContextBudgetMaxTokens = %d, want 5000", got.ContextBudgetMaxTokens)
	}
	if got.ContextToolExchangePolicy != policy {
		t.Fatalf("ContextToolExchangePolicy = %#v, want %#v", got.ContextToolExchangePolicy, policy)
	}
}

func TestToolSessionContextCarriesPromptRuntimeFence(t *testing.T) {
	want := runtimefence.Fence{BotID: "bot-1", SessionID: "session-1", Token: 31}
	ctx := runtimefence.WithContext(context.Background(), want)
	guard := func(context.Context) error { return nil }
	got := toolSessionContext(ctx, PromptInput{
		SessionID:       want.SessionID,
		RunID:           "run-1",
		ReasoningEffort: " high ",
		RuntimeGuard:    guard,
	}, &runtimeHandle{id: "rt-1", botID: want.BotID})
	if got.RuntimeFence != want {
		t.Fatalf("tool session fence = %#v, want %#v", got.RuntimeFence, want)
	}
	if got.RunContext != ctx || got.RuntimeGuard == nil {
		t.Fatalf("tool session runtime lifecycle = context:%v guard:%v", got.RunContext, got.RuntimeGuard != nil)
	}
	if got.ReasoningStoredEffort != "" || got.ReasoningRequestedEffort != "high" {
		t.Fatalf("tool session reasoning intent = stored %q, requested %q",
			got.ReasoningStoredEffort, got.ReasoningRequestedEffort)
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
		agentID:               acpprofile.AgentACPID,
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
		AgentID:               acpprofile.AgentACPID,
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
	type contextKey struct{}

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
		ownerCtx:     context.WithValue(context.Background(), contextKey{}, "runtime-scope"),
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
	if got := approval.cancelCtx.Value(contextKey{}); got != "runtime-scope" {
		t.Fatalf("approval cleanup context value = %v, want runtime-scope", got)
	}
	if got := userInput.cancelCtx.Value(contextKey{}); got != "runtime-scope" {
		t.Fatalf("user input cleanup context value = %v, want runtime-scope", got)
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

// A handle built without an owner context still has pending approvals and
// questions to release. Cleanup degrades to a value-less context rather than
// skipping, which would strand those decisions in the UI.
func TestCloseSessionCancelsPendingDecisionsWithoutOwnerContext(t *testing.T) {
	t.Parallel()

	approval := &fakeToolApprovalService{}
	userInput := &fakeUserInputCanceller{}
	logs := &countingLogHandler{}
	pool := newSessionPool(slog.New(logs), nil, fakeBotGetter{})
	pool.SetToolApprovalService(approval)
	pool.SetUserInputService(userInput)
	h := &runtimeHandle{
		id: "rt-no-owner-ctx", botID: "bot-1", status: stateIdle,
		boundSession: "session-1", lastActive: time.Now(), hadPrompt: true,
	}
	pool.mu.Lock()
	pool.runtimes[h.id] = h
	pool.bySession[h.boundSession] = h.id
	pool.mu.Unlock()

	if err := pool.CloseSession("session-1"); err != nil {
		t.Fatalf("CloseSession() error = %v", err)
	}
	if approval.cancelCount != 2 || userInput.cancelCount != 2 {
		t.Fatalf("decision cleanup count = approval:%d user_input:%d, want pre and final cleanup", approval.cancelCount, userInput.cancelCount)
	}
	// closeHandle and teardown both reach the cleanup path; the malformed
	// handle is still reported once.
	if got := logs.count(slog.LevelError); got != 1 {
		t.Fatalf("fallback error logs = %d, want 1", got)
	}
}

// countingLogHandler records log levels so tests can assert how often a
// condition is reported.
type countingLogHandler struct {
	mu     sync.Mutex
	levels []slog.Level
}

func (*countingLogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *countingLogHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.levels = append(h.levels, r.Level)
	return nil
}

func (h *countingLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *countingLogHandler) WithGroup(string) slog.Handler { return h }

func (h *countingLogHandler) count(level slog.Level) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, l := range h.levels {
		if l == level {
			n++
		}
	}
	return n
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

type recordingSessionStateStore struct {
	mu              sync.Mutex
	head            agentstate.SessionPublicationHead
	headSet         bool
	headFound       bool
	headErr         error
	headCalls       int
	epoch           agentstate.RuntimeConfigEpoch
	epochErr        error
	epochCalls      int
	state           agentstate.PersistedSessionState
	records         []agentstate.SessionStateRecord
	found           bool
	loadErr         error
	replaceErr      error
	loadCalls       int
	replaceCalls    int
	replaced        agentstate.PersistedSessionState
	replacedRecords []agentstate.SessionStateRecord
	replaceFence    runtimefence.Fence
	guardCalls      int
	guardErr        error
}

func (s *recordingSessionStateStore) RuntimeConfigEpoch(context.Context, string, string) (agentstate.RuntimeConfigEpoch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epochCalls++
	return s.epoch, s.epochErr
}

func (s *recordingSessionStateStore) GuardRuntimeSync(ctx context.Context, _ string, _ int64, fn func(context.Context) error) error {
	s.mu.Lock()
	s.guardCalls++
	err := s.guardErr
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return fn(ctx)
}

func (s *recordingSessionStateStore) CanonicalShape(context.Context, string, string) (map[string]agentstate.SessionStateFileShape, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.found {
		return nil, false, nil
	}
	shapes := make(map[string]agentstate.SessionStateFileShape, len(s.state.Files))
	for _, file := range s.state.Files {
		shapes[file.Path] = file.SessionStateFileShape
	}
	return shapes, true, nil
}

func (s *recordingSessionStateStore) Head(context.Context, string, string) (agentstate.SessionPublicationHead, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.headCalls++
	if s.headSet {
		return s.head, s.headFound, s.headErr
	}
	if s.found {
		return agentstate.SessionPublicationHead{
			RunID: s.state.ThroughRunID,
			Kind:  agentstate.SessionPublicationCheckpoint,
		}, true, s.headErr
	}
	return agentstate.SessionPublicationHead{}, false, s.headErr
}

func (s *recordingSessionStateStore) Load(ctx context.Context, _, _ string, consume agentstate.SessionStateRecordConsumer) (bool, error) {
	s.mu.Lock()
	s.loadCalls++
	state, found, loadErr := s.state, s.found, s.loadErr
	records := append([]agentstate.SessionStateRecord(nil), s.records...)
	s.mu.Unlock()
	if loadErr != nil || !found {
		return found, loadErr
	}
	index := 0
	reader := func(context.Context) (agentstate.SessionStateRecord, error) {
		if index == len(records) {
			return agentstate.SessionStateRecord{}, io.EOF
		}
		record := records[index]
		index++
		return record, nil
	}
	return true, consume(ctx, state, reader)
}

func (s *recordingSessionStateStore) Replace(ctx context.Context, _, _ string, state agentstate.PersistedSessionState, reader agentstate.SessionStateRecordReader) error {
	var records []agentstate.SessionStateRecord
	for {
		record, err := reader(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		records = append(records, record)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replaceCalls++
	s.replaced = state
	s.replacedRecords = records
	s.replaceFence, _ = runtimefence.FromContext(ctx)
	return s.replaceErr
}

type fakeToolApprovalService struct {
	cancelCtx       context.Context
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
	f.cancelCtx = ctx
	f.cancelBotID = botID
	f.cancelSessionID = sessionID
	f.cancelReason = reason
	f.cancelCount++
	f.cancelFence, _ = runtimefence.FromContext(ctx)
	return nil, nil
}

type fakeUserInputCanceller struct {
	cancelCtx       context.Context
	cancelBotID     string
	cancelSessionID string
	cancelReason    string
	cancelCount     int
	cancelFence     runtimefence.Fence
	cancelStarted   chan<- struct{}
}

func (*fakeUserInputCanceller) CreatePending(context.Context, userinput.CreatePendingInput) (userinput.Request, error) {
	return userinput.Request{}, nil
}

func (*fakeUserInputCanceller) Cancel(context.Context, userinput.CancelInput) (userinput.Request, error) {
	return userinput.Request{}, nil
}

func (*fakeUserInputCanceller) WaitForRegisteredResponse(context.Context, string) (userinput.Request, error) {
	return userinput.Request{}, nil
}

func (*fakeUserInputCanceller) RegisterWaiter(string) func() {
	return func() {}
}

func (f *fakeUserInputCanceller) CancelPendingForSession(ctx context.Context, botID, sessionID, reason string) ([]userinput.Request, error) {
	if f.cancelStarted != nil {
		f.cancelStarted <- struct{}{}
	}
	f.cancelCtx = ctx
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
	return enabledACPAgentBot(id, acpprofile.AgentACPID, mode, managed)
}

func enabledACPAgentBot(id, agentID, mode string, managed map[string]any) bots.Bot {
	if managed == nil {
		managed = map[string]any{}
	}
	if _, ok := managed["command"]; !ok {
		// The generic profile requires an explicit launch command; the fake
		// agent script ships under this name. Pass command: "" to model a
		// deliberately unconfigured agent.
		managed["command"] = "codex-acp"
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
	if os.Getenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_WRITE_STATE") == "1" {
		home := strings.TrimSpace(os.Getenv("CODEX_HOME"))
		if home == "" {
			return acp.PromptResponse{}, errors.New("CODEX_HOME is missing")
		}
		dir := filepath.Join(home, "sessions", "2026", "08", "12")
		if err := os.MkdirAll(dir, 0o700); err != nil { //nolint:gosec // fake agent writes beneath its process-owned test home.
			return acp.PromptResponse{}, err
		}
		var promptText strings.Builder
		for _, block := range p.Prompt {
			if block.Text != nil {
				promptText.WriteString(block.Text.Text)
			}
		}
		meta, err := json.Marshal(map[string]any{
			"type":    "session_meta",
			"payload": map[string]string{"id": string(p.SessionId)},
		})
		if err != nil {
			return acp.PromptResponse{}, err
		}
		line, err := json.Marshal(map[string]string{"type": "message", "prompt": promptText.String()})
		if err != nil {
			return acp.PromptResponse{}, err
		}
		transcript := filepath.Join(dir, "rollout-"+string(p.SessionId)+".jsonl")
		terminal, err := json.Marshal(map[string]any{
			"type": "event_msg",
			"payload": map[string]string{
				"type": "task_complete",
			},
		})
		if err != nil {
			return acp.PromptResponse{}, err
		}
		data := make([]byte, 0, len(meta)+len(line)+len(terminal)+3)
		if _, statErr := os.Stat(transcript); errors.Is(statErr, os.ErrNotExist) { //nolint:gosec // fake session ID is generated by the in-process test agent.
			data = append(data, meta...)
			data = append(data, '\n')
		} else if statErr != nil {
			return acp.PromptResponse{}, statErr
		}
		data = append(data, line...)
		data = append(data, '\n')
		data = append(data, terminal...)
		data = append(data, '\n')
		file, err := os.OpenFile(transcript, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // fake agent writes its process-owned test transcript.
		if err != nil {
			return acp.PromptResponse{}, err
		}
		if _, err := file.Write(data); err != nil {
			_ = file.Close()
			return acp.PromptResponse{}, err
		}
		if err := file.Close(); err != nil {
			return acp.PromptResponse{}, err
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
