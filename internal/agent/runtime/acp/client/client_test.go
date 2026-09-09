package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	sdk "github.com/felinics/twilight/sdk"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/event"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/runtimefence"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspace/bridgesvc"
)

type testWorkspace struct {
	client *bridge.Client
	info   bridge.WorkspaceInfo
}

func (w testWorkspace) MCPClient(context.Context, string) (*bridge.Client, error) {
	return w.client, nil
}

func (w testWorkspace) WorkspaceInfo(context.Context, string) (bridge.WorkspaceInfo, error) {
	return w.info, nil
}

type rotatingTestWorkspace struct {
	info    bridge.WorkspaceInfo
	clients []*bridge.Client
	calls   int
}

func (w *rotatingTestWorkspace) MCPClient(context.Context, string) (*bridge.Client, error) {
	if w.calls >= len(w.clients) {
		return nil, errors.New("no more test clients")
	}
	client := w.clients[w.calls]
	w.calls++
	return client, nil
}

func (w *rotatingTestWorkspace) WorkspaceInfo(context.Context, string) (bridge.WorkspaceInfo, error) {
	return w.info, nil
}

func TestRunnerRequiresACPCommand(t *testing.T) {
	root := t.TempDir()
	client := newTestBridgeClient(t, root)
	runner := NewRunner(nil, testWorkspace{
		client: client,
		info: bridge.WorkspaceInfo{
			Backend:        bridge.WorkspaceBackendContainer,
			DefaultWorkDir: root,
		},
	})

	_, err := runner.Run(context.Background(), RunRequest{
		BotID:   "bot-1",
		AgentID: acpprofile.AgentACPID,
		Task:    "fix tests",
		Timeout: 2 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "ACP command is required") {
		t.Fatalf("Run() error = %v, want missing command error", err)
	}
}

func TestRunnerStartSessionStreamsEvents(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "input.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	client := newTestBridgeClient(t, root)
	agentPath := writeFakeAgentScript(t, root)
	runner := NewRunner(nil, testWorkspace{
		client: client,
		info: bridge.WorkspaceInfo{
			Backend:        bridge.WorkspaceBackendContainer,
			DefaultWorkDir: root,
		},
	})

	var streamedMu sync.Mutex
	var streamed strings.Builder
	var streamedEvents []event.StreamEvent
	startupCtx, cancelStartup := context.WithCancel(context.Background())
	sess, err := runner.StartSession(startupCtx, StartRequest{
		BotID:       "bot-1",
		AgentID:     acpprofile.AgentACPID,
		ProjectPath: "/data/project",
		Command:     agentPath,
		Timeout:     10 * time.Second,
		ToolSession: ToolSessionContext{BotID: "bot-1", SessionID: "session-1"},
		ToolApproval: &fakeACPToolApproval{
			evaluation: toolapproval.Evaluation{Decision: toolapproval.DecisionBypass},
		},
	}, EventSinkFunc(func(ev event.StreamEvent) {
		streamedMu.Lock()
		defer streamedMu.Unlock()
		streamedEvents = append(streamedEvents, ev)
		if ev.Type == event.TextDelta {
			streamed.WriteString(ev.Delta)
		}
	}))
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer func() { _ = sess.Close() }()
	cancelStartup()

	result, err := sess.Prompt(context.Background(), "touch the project")
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if result.StopReason != string(acp.StopReasonEndTurn) {
		t.Fatalf("StopReason = %q, want %q", result.StopReason, acp.StopReasonEndTurn)
	}
	streamedMu.Lock()
	streamedText := streamed.String()
	streamedEventsSnapshot := append([]event.StreamEvent(nil), streamedEvents...)
	streamedMu.Unlock()
	if !strings.Contains(streamedText, "read: hello") {
		t.Fatalf("streamed text = %q", streamedText)
	}
	for _, want := range []string{"read", "write", "exec"} {
		if !hasStreamedToolEvent(streamedEventsSnapshot, event.ToolCallEnd, want) {
			t.Fatalf("streamed events missing %s tool end: %#v", want, streamedEventsSnapshot)
		}
	}
	for _, want := range []string{"read", "write", "exec"} {
		if !hasStreamedToolEvent(result.Events, event.ToolCallEnd, want) {
			t.Fatalf("result events missing %s tool end: %#v", want, result.Events)
		}
	}
	writeEvent := findStreamedToolEvent(streamedEventsSnapshot, event.ToolCallEnd, "write")
	if writeEvent == nil {
		t.Fatalf("streamed events missing write tool end: %#v", streamedEventsSnapshot)
		return
	}
	writeInput, ok := writeEvent.Input.(map[string]any)
	if !ok {
		t.Fatalf("write input = %#v, want object", writeEvent.Input)
	}
	if writeInput["path"] == "" || writeInput["content"] != "written by fake agent\n" {
		t.Fatalf("write input = %#v, want path and content", writeInput)
	}
}

func TestRunnerStartSessionRejectsUnsupportedProtocolVersion(t *testing.T) {
	runner, agentPath := newStartSessionTestRunner(t)
	t.Setenv("MEMOH_ACP_FAKE_AGENT_PROTOCOL_VERSION", "2")

	sess, err := runner.StartSession(context.Background(), StartRequest{
		AgentID:     acpprofile.AgentACPID,
		BotID:       "bot-1",
		ProjectPath: "/data/project",
		Command:     agentPath,
		Timeout:     10 * time.Second,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported protocol version 2") {
		t.Fatalf("StartSession() = (%#v, %v), want unsupported protocol version", sess, err)
	}
}

func TestRunnerStartSessionRejectsEmptyNewSessionID(t *testing.T) {
	runner, agentPath := newStartSessionTestRunner(t)
	t.Setenv("MEMOH_ACP_FAKE_AGENT_EMPTY_SESSION_ID", "1")

	sess, err := runner.StartSession(context.Background(), StartRequest{
		AgentID:     acpprofile.AgentACPID,
		BotID:       "bot-1",
		ProjectPath: "/data/project",
		Command:     agentPath,
		Timeout:     10 * time.Second,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "empty session id") {
		t.Fatalf("StartSession() = (%#v, %v), want empty session id failure", sess, err)
	}
}

func TestSessionCloseRequiresAdvertisedCapability(t *testing.T) {
	runner, agentPath := newStartSessionTestRunner(t)
	capturePath := filepath.Join(t.TempDir(), "close")
	t.Setenv("MEMOH_ACP_FAKE_AGENT_CAPTURE_CLOSE_FILE", capturePath)

	sess, err := runner.StartSession(context.Background(), StartRequest{
		AgentID:     acpprofile.AgentACPID,
		BotID:       "bot-1",
		ProjectPath: "/data/project",
		Command:     agentPath,
		Timeout:     10 * time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := os.Stat(capturePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session/close was called without the capability: %v", err)
	}
}

func TestSessionCloseUsesAdvertisedCapability(t *testing.T) {
	type contextKey struct{}

	runner, agentPath := newStartSessionTestRunner(t)
	capturePath := filepath.Join(t.TempDir(), "close")
	t.Setenv("MEMOH_ACP_FAKE_AGENT_CLOSE", "1")
	t.Setenv("MEMOH_ACP_FAKE_AGENT_CAPTURE_CLOSE_FILE", capturePath)

	startCtx := context.WithValue(context.Background(), contextKey{}, "session-scope")
	sess, err := runner.StartSession(startCtx, StartRequest{
		AgentID:     acpprofile.AgentACPID,
		BotID:       "bot-1",
		ProjectPath: "/data/project",
		Command:     agentPath,
		Timeout:     10 * time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	if got := sess.lifecycleCtx.Value(contextKey{}); got != "session-scope" {
		t.Fatalf("session lifecycle context value = %v, want session-scope", got)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if data, err := os.ReadFile(capturePath); err != nil || string(data) != "fake-session" { //nolint:gosec // test helper reads its own temporary capture.
		t.Fatalf("session/close capture = %q, %v", data, err)
	}
}

func newStartSessionTestRunner(t *testing.T) (*Runner, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "project"), 0o750); err != nil {
		t.Fatal(err)
	}
	client := newTestBridgeClient(t, root)
	agentPath := writeFakeAgentScript(t, root)
	return NewRunner(nil, testWorkspace{
		client: client,
		info: bridge.WorkspaceInfo{
			Backend:        bridge.WorkspaceBackendContainer,
			DefaultWorkDir: root,
		},
	}), agentPath
}

func TestWriteToolInputTruncatesLargeContent(t *testing.T) {
	content := strings.Repeat("a", maxWriteToolContentPreview+1) + "\n"
	input := writeToolInput("/data/large.txt", content)

	if input["path"] != "/data/large.txt" {
		t.Fatalf("path = %#v", input["path"])
	}
	if input["content_truncated"] != true {
		t.Fatalf("content_truncated = %#v, want true", input["content_truncated"])
	}
	if input["content_bytes"] != len(content) {
		t.Fatalf("content_bytes = %#v, want %d", input["content_bytes"], len(content))
	}
	if input["content_line_count"] != 2 {
		t.Fatalf("content_line_count = %#v, want 2", input["content_line_count"])
	}
	preview, ok := input["content"].(string)
	if !ok {
		t.Fatalf("content = %#v, want string", input["content"])
	}
	if len(preview) > maxWriteToolContentPreview {
		t.Fatalf("preview length = %d, want <= %d", len(preview), maxWriteToolContentPreview)
	}
	if preview == content {
		t.Fatalf("preview should be truncated")
	}
}

func TestSessionPromptBuildsEmbeddedContextResource(t *testing.T) {
	t.Parallel()

	markdown := "# Memoh Context\n\nRemember the project preference."
	sess := &Session{embeddedContext: true}
	blocks := sess.promptBlocks("inspect the app", []PromptResource{{
		URI:      "memoh://context/current-turn",
		MimeType: "text/markdown",
		Text:     markdown,
	}}, nil)
	if len(blocks) != 2 {
		t.Fatalf("prompt blocks = %d, want text + resource", len(blocks))
	}
	if blocks[0].Resource == nil || blocks[0].Resource.Resource.TextResourceContents == nil {
		t.Fatalf("first block = %#v, want embedded text resource", blocks[0])
	}
	resource := blocks[0].Resource.Resource.TextResourceContents
	if resource.Uri != "memoh://context/current-turn" || resource.MimeType == nil || *resource.MimeType != "text/markdown" || resource.Text != markdown {
		t.Fatalf("resource = %#v, want Memoh markdown context", resource)
	}
	if blocks[1].Text == nil || blocks[1].Text.Text != "inspect the app" {
		t.Fatalf("second block = %#v, want user text", blocks[1])
	}
}

func TestSessionPromptFallsBackToTextContextWhenEmbeddedContextUnsupported(t *testing.T) {
	t.Parallel()

	sess := &Session{}
	blocks := sess.promptBlocks("inspect the app", []PromptResource{{
		URI:      "memoh://context/current-turn",
		MimeType: "text/markdown",
		Text:     "Memoh context",
	}}, nil)
	if len(blocks) != 1 || blocks[0].Text == nil {
		t.Fatalf("prompt blocks = %#v, want single text fallback", blocks)
	}
	text := blocks[0].Text.Text
	if !strings.Contains(text, `<context ref="memoh://context/current-turn">`) || !strings.Contains(text, "Memoh context") || !strings.Contains(text, "inspect the app") {
		t.Fatalf("fallback text = %q, want context and prompt", text)
	}
}

func TestSessionPromptBuildsImageOnlyContent(t *testing.T) {
	t.Parallel()

	sess := &Session{imagePromptSupported: true}
	blocks := sess.promptBlocks("", nil, []PromptImage{{
		Data:     "aW1hZ2U=",
		MimeType: "image/png",
	}})
	if len(blocks) != 1 || blocks[0].Image == nil {
		t.Fatalf("prompt blocks = %#v, want single image block", blocks)
	}
	image := blocks[0].Image
	if image.Data != "aW1hZ2U=" || image.MimeType != "image/png" {
		t.Fatalf("image block = %#v, want inline PNG", image)
	}
}

func TestSessionPromptRejectsImagesWithoutCapability(t *testing.T) {
	t.Parallel()

	sess := &Session{conn: &clientConnection{}}
	_, err := sess.PromptWithToolContextOptions(context.Background(), "inspect", nil, ToolSessionContext{}, PromptOptions{
		Images: []PromptImage{{Data: "aW1hZ2U=", MimeType: "image/png"}},
	})
	if !errors.Is(err, ErrImagePromptUnsupported) {
		t.Fatalf("PromptWithToolContextOptions() error = %v, want ErrImagePromptUnsupported", err)
	}
}

func TestNormalizePromptImagesRejectsNonImageMIME(t *testing.T) {
	t.Parallel()

	_, err := NormalizePromptImages([]PromptImage{{Data: "dGV4dA==", MimeType: "text/plain"}})
	if !errors.Is(err, ErrInvalidPromptImage) {
		t.Fatalf("NormalizePromptImages() error = %v, want ErrInvalidPromptImage", err)
	}
}

func TestNormalizePromptImagesRejectsMalformedBase64(t *testing.T) {
	t.Parallel()

	_, err := NormalizePromptImages([]PromptImage{{Data: "not-valid***", MimeType: "image/png"}})
	if !errors.Is(err, ErrInvalidPromptImage) {
		t.Fatalf("NormalizePromptImages() error = %v, want ErrInvalidPromptImage", err)
	}
}

func TestStartMemohToolsBridgeRetriesClosingWorkspaceClient(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	stale := newTestBridgeClient(t, root)
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	fresh := newTestBridgeClient(t, root)
	workspace := &rotatingTestWorkspace{
		info: bridge.WorkspaceInfo{
			Backend:         bridge.WorkspaceBackendContainer,
			DefaultWorkDir:  "/data",
			ACPToolsHTTPURL: "http://127.0.0.1:18732/mcp",
		},
		clients: []*bridge.Client{fresh},
	}
	runner := NewRunner(nil, workspace)

	gotClient, stop, err := runner.startMemohToolsBridge(context.Background(), "bot-1", stale, "/mcp/test", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	if err != nil {
		t.Fatalf("startMemohToolsBridge() error = %v", err)
	}
	defer stop()
	if gotClient != fresh {
		t.Fatalf("startMemohToolsBridge() client = %#v, want fresh client", gotClient)
	}
	if workspace.calls != 1 {
		t.Fatalf("workspace MCPClient calls = %d, want retry once", workspace.calls)
	}
}

func TestRunnerStartSessionSupportsReleaseTerminalWithoutWait(t *testing.T) {
	t.Setenv("MEMOH_ACP_FAKE_AGENT_RELEASE_TERMINAL_WITHOUT_WAIT", "1")
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}

	client := newTestBridgeClient(t, root)
	agentPath := writeFakeAgentScript(t, root)
	runner := NewRunner(nil, testWorkspace{
		client: client,
		info: bridge.WorkspaceInfo{
			Backend:        bridge.WorkspaceBackendContainer,
			DefaultWorkDir: root,
		},
	})

	sess, err := runner.StartSession(context.Background(), StartRequest{
		BotID:       "bot-1",
		AgentID:     acpprofile.AgentACPID,
		ProjectPath: "/data/project",
		Command:     agentPath,
		Timeout:     10 * time.Second,
		ToolSession: ToolSessionContext{BotID: "bot-1", SessionID: "session-1"},
		ToolApproval: &fakeACPToolApproval{
			evaluation: toolapproval.Evaluation{Decision: toolapproval.DecisionBypass},
		},
	}, nil)
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer func() { _ = sess.Close() }()

	result, err := sess.Prompt(context.Background(), "check time")
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if !strings.Contains(result.Text, "term: terminal-ok") {
		t.Fatalf("result text = %q, want terminal output", result.Text)
	}
}

func hasStreamedToolEvent(events []event.StreamEvent, typ event.StreamEventType, toolName string) bool {
	return findStreamedToolEvent(events, typ, toolName) != nil
}

func findStreamedToolEvent(events []event.StreamEvent, typ event.StreamEventType, toolName string) *event.StreamEvent {
	for i := range events {
		if events[i].Type == typ && events[i].ToolName == toolName {
			return &events[i]
		}
	}
	return nil
}

func TestRunnerStartSessionReadsProtocolModelsAndSetsModel(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEMOH_ACP_FAKE_AGENT_MODELS", "1")

	client := newTestBridgeClient(t, root)
	agentPath := writeFakeAgentScript(t, root)
	runner := NewRunner(nil, testWorkspace{
		client: client,
		info: bridge.WorkspaceInfo{
			Backend:        bridge.WorkspaceBackendContainer,
			DefaultWorkDir: root,
		},
	})

	sess, err := runner.StartSession(context.Background(), StartRequest{
		BotID:       "bot-1",
		AgentID:     acpprofile.AgentACPID,
		ProjectPath: "/data/project",
		Command:     agentPath,
		Timeout:     10 * time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer func() { _ = sess.Close() }()

	state := sess.ModelState()
	if !state.Supported || state.CurrentModelID != "gpt-5.1-codex" {
		t.Fatalf("ModelState() = %#v, want protocol model state", state)
	}
	if len(state.Available) != 2 || state.Available[1].ID != "gpt-5.1-codex-high" {
		t.Fatalf("available models = %#v", state.Available)
	}
	if state.Available[1].Description != "Highest reasoning" {
		t.Fatalf("model description = %q, want protocol description", state.Available[1].Description)
	}
	state.Available[0].Name = "mutated"
	if got := sess.ModelState().Available[0].Name; got == "mutated" {
		t.Fatalf("ModelState returned mutable slice")
	}

	state, err = sess.SetModel(context.Background(), "gpt-5.1-codex-high")
	if err != nil {
		t.Fatalf("SetModel() error = %v", err)
	}
	if state.CurrentModelID != "gpt-5.1-codex-high" {
		t.Fatalf("SetModel state = %#v, want selected model", state)
	}
	if _, err := sess.SetModel(context.Background(), "gpt-5.1-codex-missing"); !errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("SetModel(missing) error = %v, want ErrModelUnavailable", err)
	}
}

func TestRunnerStartSessionWithoutProtocolModelsDoesNotInventFallback(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}

	client := newTestBridgeClient(t, root)
	agentPath := writeFakeAgentScript(t, root)
	runner := NewRunner(nil, testWorkspace{
		client: client,
		info: bridge.WorkspaceInfo{
			Backend:        bridge.WorkspaceBackendContainer,
			DefaultWorkDir: root,
		},
	})

	sess, err := runner.StartSession(context.Background(), StartRequest{
		BotID:       "bot-1",
		AgentID:     acpprofile.AgentACPID,
		ProjectPath: "/data/project",
		Command:     agentPath,
		Timeout:     10 * time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer func() { _ = sess.Close() }()

	state := sess.ModelState()
	if state.Supported || state.CurrentModelID != "" || len(state.Available) != 0 {
		t.Fatalf("ModelState() = %#v, want unsupported with no fallback models", state)
	}
	if _, err := sess.SetModel(context.Background(), "gpt-5.1-codex"); !errors.Is(err, ErrModelSelectionUnsupported) {
		t.Fatalf("SetModel() error = %v, want ErrModelSelectionUnsupported", err)
	}
}

func TestRunnerStartSessionAppliesAndUpdatesReasoningConfig(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEMOH_ACP_FAKE_AGENT_REASONING", "category")

	client := newTestBridgeClient(t, root)
	agentPath := writeFakeAgentScript(t, root)
	runner := NewRunner(nil, testWorkspace{
		client: client,
		info: bridge.WorkspaceInfo{
			Backend:        bridge.WorkspaceBackendContainer,
			DefaultWorkDir: root,
		},
	})

	sess, err := runner.StartSession(context.Background(), StartRequest{
		BotID:                  "bot-1",
		AgentID:                acpprofile.AgentACPID,
		ProjectPath:            "/data/project",
		Command:                agentPath,
		DefaultReasoningEffort: "high",
		Timeout:                10 * time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer func() { _ = sess.Close() }()

	state := sess.ReasoningState()
	if !state.Supported || state.CurrentEffort != "high" || len(state.Available) != 3 {
		t.Fatalf("ReasoningState() = %#v, want applied agent config", state)
	}
	state, err = sess.SetReasoningEffort(context.Background(), "low")
	if err != nil {
		t.Fatalf("SetReasoningEffort() error = %v", err)
	}
	if state.CurrentEffort != "low" {
		t.Fatalf("SetReasoningEffort() state = %#v", state)
	}
	if _, err := sess.SetReasoningEffort(context.Background(), "ultra"); !errors.Is(err, ErrReasoningEffortUnavailable) {
		t.Fatalf("SetReasoningEffort(ultra) error = %v, want ErrReasoningEffortUnavailable", err)
	}
}

func TestRunnerRejectsUnconfirmedSessionConfigUpdates(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEMOH_ACP_FAKE_AGENT_MODELS", "1")
	t.Setenv("MEMOH_ACP_FAKE_AGENT_REASONING", "category")
	t.Setenv("MEMOH_ACP_FAKE_AGENT_CONFIG_NOOP", "1")

	runner := NewRunner(nil, testWorkspace{
		client: newTestBridgeClient(t, root),
		info: bridge.WorkspaceInfo{
			Backend:        bridge.WorkspaceBackendContainer,
			DefaultWorkDir: root,
		},
	})
	sess, err := runner.StartSession(context.Background(), StartRequest{
		BotID:       "bot-1",
		AgentID:     acpprofile.AgentACPID,
		ProjectPath: "/data/project",
		Command:     writeFakeAgentScript(t, root),
		Timeout:     10 * time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer func() { _ = sess.Close() }()

	if _, err := sess.SetModel(context.Background(), "gpt-5.1-codex-high"); !errors.Is(err, ErrSessionConfigUpdateUnconfirmed) {
		t.Fatalf("SetModel() error = %v, want ErrSessionConfigUpdateUnconfirmed", err)
	}
	if _, err := sess.SetReasoningEffort(context.Background(), "high"); !errors.Is(err, ErrSessionConfigUpdateUnconfirmed) {
		t.Fatalf("SetReasoningEffort() error = %v, want ErrSessionConfigUpdateUnconfirmed", err)
	}
	if got := sess.ModelState().CurrentModelID; got != "gpt-5.1-codex" {
		t.Fatalf("ModelState() current = %q, want pre-update state", got)
	}
	if got := sess.ReasoningState().CurrentEffort; got != "medium" {
		t.Fatalf("ReasoningState() current = %q, want pre-update state", got)
	}
}

func TestRunnerRejectsInvalidSessionConfigResponse(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEMOH_ACP_FAKE_AGENT_REASONING", "category")
	t.Setenv("MEMOH_ACP_FAKE_AGENT_CONFIG_OMIT_OPTIONS", "1")

	runner := NewRunner(nil, testWorkspace{
		client: newTestBridgeClient(t, root),
		info: bridge.WorkspaceInfo{
			Backend:        bridge.WorkspaceBackendContainer,
			DefaultWorkDir: root,
		},
	})
	sess, err := runner.StartSession(context.Background(), StartRequest{
		BotID:       "bot-1",
		AgentID:     acpprofile.AgentACPID,
		ProjectPath: "/data/project",
		Command:     writeFakeAgentScript(t, root),
		Timeout:     10 * time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer func() { _ = sess.Close() }()

	_, err = sess.SetReasoningEffort(context.Background(), "high")
	if err == nil || !strings.Contains(err.Error(), "validate session config response") {
		t.Fatalf("SetReasoningEffort() error = %v, want response validation failure", err)
	}
}

func TestRunnerStartSessionRejectsUnknownDefaultReasoningState(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEMOH_ACP_FAKE_AGENT_REASONING", "category")
	t.Setenv("MEMOH_ACP_FAKE_AGENT_CONFIG_ERROR_AFTER_APPLY", "1")

	runner := NewRunner(nil, testWorkspace{
		client: newTestBridgeClient(t, root),
		info: bridge.WorkspaceInfo{
			Backend:        bridge.WorkspaceBackendContainer,
			DefaultWorkDir: root,
		},
	})
	sess, err := runner.StartSession(context.Background(), StartRequest{
		BotID:                  "bot-1",
		AgentID:                acpprofile.AgentACPID,
		ProjectPath:            "/data/project",
		Command:                writeFakeAgentScript(t, root),
		DefaultReasoningEffort: "high",
		Timeout:                10 * time.Second,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "apply default ACP reasoning effort") {
		t.Fatalf("StartSession() = (%#v, %v), want default reasoning failure", sess, err)
	}
	if sess != nil {
		t.Fatalf("StartSession() session = %#v, want nil after uncertain mutation", sess)
	}
}

func TestRunnerModelSwitchConsumesReasoningConfigUpdate(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEMOH_ACP_FAKE_AGENT_MODELS", "1")
	t.Setenv("MEMOH_ACP_FAKE_AGENT_REASONING", "category")
	t.Setenv("MEMOH_ACP_FAKE_AGENT_REASONING_MODEL_NOTIFY", "1")

	runner := NewRunner(nil, testWorkspace{
		client: newTestBridgeClient(t, root),
		info: bridge.WorkspaceInfo{
			Backend:        bridge.WorkspaceBackendContainer,
			DefaultWorkDir: root,
		},
	})
	sess, err := runner.StartSession(context.Background(), StartRequest{
		BotID:       "bot-1",
		AgentID:     acpprofile.AgentACPID,
		ProjectPath: "/data/project",
		Command:     writeFakeAgentScript(t, root),
		Timeout:     10 * time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer func() { _ = sess.Close() }()

	if _, err := sess.SetModel(context.Background(), "gpt-5.1-codex-high"); err != nil {
		t.Fatalf("SetModel() error = %v", err)
	}
	state := sess.ReasoningState()
	if state.CurrentEffort != "max" || len(state.Available) != 2 || state.Available[0].ID != "balanced" {
		t.Fatalf("ReasoningState() = %#v, want model-specific config update", state)
	}
}

func TestRunnerStartSessionSendsNoMCPServers(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
	capturePath := filepath.Join(root, "mcp-servers.json")
	t.Setenv("MEMOH_ACP_FAKE_AGENT_MCP_HTTP", "1")
	t.Setenv("MEMOH_ACP_FAKE_AGENT_CAPTURE_MCP_FILE", capturePath)

	client := newTestBridgeClient(t, root)
	agentPath := writeFakeAgentScript(t, root)
	runner := NewRunner(nil, testWorkspace{
		client: client,
		info: bridge.WorkspaceInfo{
			Backend:        bridge.WorkspaceBackendContainer,
			DefaultWorkDir: root,
		},
	})

	sess, err := runner.StartSession(context.Background(), StartRequest{
		BotID:       "bot-1",
		AgentID:     acpprofile.AgentACPID,
		ProjectPath: "/data/project",
		Command:     agentPath,
		Timeout:     10 * time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer func() { _ = sess.Close() }()

	raw, err := os.ReadFile(capturePath) //nolint:gosec // test path is under t.TempDir.
	if err != nil {
		t.Fatalf("read captured MCP servers: %v", err)
	}
	var servers []map[string]any
	if err := json.Unmarshal(raw, &servers); err != nil {
		t.Fatalf("decode captured MCP servers: %v", err)
	}
	if len(servers) != 0 {
		t.Fatalf("captured MCP servers = %#v, want none for basic ACP runtime", servers)
	}
}

func TestRunnerStartGenericACPSessionInjectsHTTPToolServer(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
	capturePath := filepath.Join(root, "mcp-servers.json")
	t.Setenv("MEMOH_ACP_FAKE_AGENT_MCP_HTTP", "1")
	t.Setenv("MEMOH_ACP_FAKE_AGENT_CAPTURE_MCP_FILE", capturePath)

	client := newTestBridgeClient(t, root)
	agentPath := writeFakeAgentScript(t, root)
	runner := NewRunner(nil, testWorkspace{
		client: client,
		info: bridge.WorkspaceInfo{
			Backend:         bridge.WorkspaceBackendContainer,
			DefaultWorkDir:  root,
			ACPToolsHTTPURL: "http://memoh.test/bots/bot-1/tools",
		},
	})

	sess, err := runner.StartSession(context.Background(), StartRequest{
		AgentID:     acpprofile.AgentACPID,
		BotID:       "bot-1",
		ProjectPath: "/data/project",
		Command:     agentPath,
		Timeout:     10 * time.Second,
		ToolHTTPURL: "http://memoh.test/bots/bot-1/tools",
		ToolHTTPHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{}}`))
		}),
		ToolSession: ToolSessionContext{
			BotID:             "bot-1",
			ChatID:            "chat-1",
			SessionID:         "session-1",
			RunID:             "run-1",
			SessionType:       "acp_agent",
			ChannelIdentityID: "user-1",
			SessionToken:      "token-1",
			ConversationType:  "private",
		},
	}, nil)
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer func() { _ = sess.Close() }()

	raw, err := os.ReadFile(capturePath) //nolint:gosec // test path is under t.TempDir.
	if err != nil {
		t.Fatalf("read captured MCP servers: %v", err)
	}
	var servers []map[string]any
	if err := json.Unmarshal(raw, &servers); err != nil {
		t.Fatalf("decode captured MCP servers: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("captured MCP servers = %#v, want one Memoh tools server", servers)
	}
	rawURL, _ := servers[0]["url"].(string)
	if servers[0]["type"] != "http" || !strings.HasPrefix(rawURL, "http://memoh.test/bots/bot-1/tools/") || servers[0]["name"] != "Memoh Tools" {
		t.Fatalf("captured MCP server = %#v", servers[0])
	}
	headers, _ := servers[0]["headers"].([]any)
	if hasCapturedHeaderName(headers, "Authorization") || hasCapturedHeaderName(headers, "X-Memoh-Session-Token") {
		t.Fatalf("captured credentials in MCP headers: %#v", headers)
	}
	if !hasCapturedHeader(headers, "X-Memoh-Session-Id", "session-1") {
		t.Fatalf("missing session id header in %#v", headers)
	}
	if !hasCapturedHeader(headers, "X-Memoh-Run-Id", "run-1") {
		t.Fatalf("missing run id header in %#v", headers)
	}
	if !hasCapturedHeader(headers, "X-Memoh-Channel-Identity-Id", "user-1") {
		t.Fatalf("missing channel identity header in %#v", headers)
	}
	if !hasCapturedHeader(headers, "X-Memoh-Conversation-Type", "private") {
		t.Fatalf("missing conversation type header in %#v", headers)
	}
}

func TestRunnerStartSessionInjectsHTTPToolServerForForcedProfile(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
	capturePath := filepath.Join(root, "mcp-servers.json")
	t.Setenv("MEMOH_ACP_FAKE_AGENT_CAPTURE_MCP_FILE", capturePath)

	client := newTestBridgeClient(t, root)
	agentPath := writeFakeAgentScript(t, root)
	runner := NewRunner(nil, testWorkspace{
		client: client,
		info: bridge.WorkspaceInfo{
			Backend:         bridge.WorkspaceBackendContainer,
			DefaultWorkDir:  root,
			ACPToolsHTTPURL: "http://memoh.test/bots/bot-custom/tools",
		},
	})

	sess, err := runner.StartSession(context.Background(), StartRequest{
		AgentID:     acpprofile.AgentACPID,
		BotID:       "bot-custom",
		ProjectPath: "/data/project",
		Command:     agentPath,
		Timeout:     10 * time.Second,
		ToolHTTPURL: "http://memoh.test/bots/bot-custom/tools",
		ToolHTTPHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{}}`))
		}),
		ToolSession: ToolSessionContext{
			BotID:       "bot-custom",
			SessionID:   "session-custom",
			SessionType: "acp_agent",
		},
	}, nil)
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer func() { _ = sess.Close() }()

	servers := readCapturedMCPServers(t, capturePath)
	if len(servers) != 1 {
		t.Fatalf("captured MCP servers = %#v, want one forced Memoh tools server", servers)
	}
	rawURL, _ := servers[0]["url"].(string)
	if servers[0]["type"] != "http" || servers[0]["name"] != "Memoh Tools" || !strings.HasPrefix(rawURL, "http://memoh.test/bots/bot-custom/tools/") {
		t.Fatalf("captured MCP server = %#v", servers[0])
	}
}

func TestRedactedToolHTTPURLHidesRouteSecrets(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "bot route",
			raw:  "http://memoh.test/bots/11111111-1111-1111-1111-111111111111/tools?token=secret#fragment",
			want: "http://memoh.test/bots/redacted/tools",
		},
		{
			name: "guard route",
			raw:  "http://127.0.0.1:12345/mcp/22222222-2222-2222-2222-222222222222",
			want: "http://127.0.0.1:12345/mcp/redacted",
		},
		{
			name: "non uuid route",
			raw:  "http://127.0.0.1:12345/mcp/local-secret",
			want: "http://127.0.0.1:12345/mcp/redacted",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactedToolHTTPURL(tc.raw); got != tc.want {
				t.Fatalf("redactedToolHTTPURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

func readCapturedMCPServers(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // test path is under t.TempDir.
	if err != nil {
		t.Fatalf("read captured MCP servers: %v", err)
	}
	var servers []map[string]any
	if err := json.Unmarshal(raw, &servers); err != nil {
		t.Fatalf("decode captured MCP servers: %v", err)
	}
	return servers
}

func hasCapturedHeader(headers []any, name, value string) bool {
	for _, raw := range headers {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if item["name"] == name && item["value"] == value {
			return true
		}
	}
	return false
}

func hasCapturedHeaderName(headers []any, name string) bool {
	for _, raw := range headers {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if item["name"] == name {
			return true
		}
	}
	return false
}

func TestSessionCloseCancelsActivePrompt(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
	startedFile := filepath.Join(root, "prompt-started")
	cancelledFile := filepath.Join(root, "prompt-cancelled")
	t.Setenv("MEMOH_ACP_FAKE_AGENT_HANG_PROMPT", "1")
	t.Setenv("MEMOH_ACP_PROMPT_STARTED_FILE", startedFile)
	t.Setenv("MEMOH_ACP_PROMPT_CANCELLED_FILE", cancelledFile)

	client := newTestBridgeClient(t, root)
	agentPath := writeFakeAgentScript(t, root)
	runner := NewRunner(nil, testWorkspace{
		client: client,
		info: bridge.WorkspaceInfo{
			Backend:        bridge.WorkspaceBackendContainer,
			DefaultWorkDir: root,
		},
	})

	sess, err := runner.StartSession(context.Background(), StartRequest{
		BotID:       "bot-1",
		AgentID:     acpprofile.AgentACPID,
		ProjectPath: "/data/project",
		Command:     agentPath,
		Timeout:     10 * time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	promptErrCh := make(chan error, 1)
	go func() {
		_, err := sess.Prompt(context.Background(), "hang until close")
		promptErrCh <- err
	}()
	waitForFile(t, startedFile, 2*time.Second)

	closeErrCh := make(chan error, 1)
	go func() {
		closeErrCh <- sess.Close()
	}()
	select {
	case err := <-closeErrCh:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close blocked behind active Prompt")
	}
	waitForFile(t, cancelledFile, 2*time.Second)
	select {
	case err := <-promptErrCh:
		if err == nil {
			t.Fatal("Prompt returned nil error after Close cancelled it")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt did not return after Close")
	}
}

//nolint:contextcheck // Close uses its own bounded cleanup context after startup cancellation.
func TestRunnerStartSessionCancellationStopsStartupProcess(t *testing.T) {
	root := t.TempDir()
	server := &startupCancelBridgeServer{
		Server: bridgesvc.New(bridgesvc.Options{
			DefaultWorkDir:    root,
			WorkspaceRoot:     root,
			DataMount:         config.DefaultDataMount,
			AllowHostAbsolute: true,
		}),
		processStarted:   make(chan struct{}),
		processCancelled: make(chan struct{}),
	}
	client := newStartupCancelBridgeClient(t, server)
	runner := NewRunner(nil, testWorkspace{
		client: client,
		info: bridge.WorkspaceInfo{
			Backend:        bridge.WorkspaceBackendContainer,
			DefaultWorkDir: root,
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		sess, err := runner.StartSession(ctx, StartRequest{
			BotID:       "bot-1",
			AgentID:     acpprofile.AgentACPID,
			ProjectPath: "/data",
			Command:     "sh",
			Timeout:     time.Minute,
		}, nil)
		if sess != nil {
			_ = sess.Close()
		}
		errCh <- err
	}()

	select {
	case <-server.processStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("bridge process did not start")
	}
	cancel()
	select {
	case <-server.processCancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("bridge process context was not cancelled during startup")
	}
	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "initialize ACP agent") {
			t.Fatalf("StartSession() error = %v, want initialize failure after cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StartSession did not return after startup cancellation")
	}
}

func TestRunnerMissingCommandIncludesStderr(t *testing.T) {
	root := t.TempDir()
	client := newTestBridgeClient(t, root)
	runner := NewRunner(nil, testWorkspace{
		client: client,
		info: bridge.WorkspaceInfo{
			Backend:        bridge.WorkspaceBackendContainer,
			DefaultWorkDir: root,
		},
	})
	_, err := runner.Run(context.Background(), RunRequest{
		BotID:   "bot-1",
		AgentID: acpprofile.AgentACPID,
		Task:    "fix tests",
		Command: "memoh-definitely-missing-acp-command",
		Timeout: 10 * time.Second,
	})
	if err == nil {
		t.Fatal("expected missing command error")
	}
	if !strings.Contains(err.Error(), "memoh-definitely-missing-acp-command") {
		t.Fatalf("missing command error did not include stderr command detail: %v", err)
	}
	if !strings.Contains(err.Error(), "not available") && !strings.Contains(err.Error(), "pinned adapter") {
		t.Fatalf("missing command error is not actionable: %v", err)
	}
}

func TestRequestPermissionUsesMemohToolApproval(t *testing.T) {
	t.Parallel()

	wantFence := runtimefence.Fence{BotID: "bot-1", SessionID: "session-1", Token: 23}
	approval := &fakeACPToolApproval{
		decision: toolapproval.Request{
			ID:      "approval-1",
			ShortID: 9,
			Status:  toolapproval.StatusApproved,
		},
	}
	callbacks := &clientCallbacks{
		root:     "/data",
		cwd:      "/data",
		approval: approval,
		baseSession: ToolSessionContext{
			BotID:             "bot-1",
			SessionID:         "session-1",
			RunID:             "run-1",
			ChannelIdentityID: "channel-1",
			CurrentPlatform:   "web",
			ConversationType:  "private",
			RuntimeFence:      wantFence,
		},
		events: &toolEventEmitter{},
	}
	collector := newEventCollector()
	callbacks.setPromptState(collector, nil, callbacks.baseSession)

	resp, err := callbacks.RequestPermission(context.Background(), acp.RequestPermissionRequest{
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId("write-1"),
			Title:      acp.Ptr("Write /data/review.txt"),
			Kind:       acp.Ptr(acp.ToolKindEdit),
			Locations:  []acp.ToolCallLocation{{Path: "/data/review.txt"}},
			RawInput: map[string]any{
				"path":    "/data/review.txt",
				"content": "review me\n",
			},
		},
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: acp.PermissionOptionId("allow")},
			{Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: acp.PermissionOptionId("reject")},
		},
	})
	if err != nil {
		t.Fatalf("RequestPermission error = %v", err)
	}
	if resp.Outcome.Selected == nil || resp.Outcome.Selected.OptionId != acp.PermissionOptionId("allow") {
		t.Fatalf("permission outcome = %#v, want allow once", resp.Outcome)
	}
	if approval.created.ToolCallID != "write-1" || approval.created.ToolName != "write" {
		t.Fatalf("approval input = %#v", approval.created)
	}
	if approval.evaluateFence != wantFence {
		t.Fatalf("approval fence = %#v, want %#v", approval.evaluateFence, wantFence)
	}
	if approval.created.BotID != "bot-1" || approval.created.SessionID != "session-1" || approval.created.ChannelIdentityID != "channel-1" {
		t.Fatalf("approval context = %#v", approval.created)
	}
	events := collector.result().Events
	if len(events) != 2 {
		t.Fatalf("events = %#v, want pending and approved approval events", events)
	}
	for i, status := range []string{toolapproval.StatusPending, toolapproval.StatusApproved} {
		if events[i].Type != event.ToolApprovalRequest ||
			events[i].ToolCallID != "write-1" ||
			events[i].ApprovalID != "approval-1" ||
			events[i].Status != status {
			t.Fatalf("approval event %d = %#v, want status %q", i, events[i], status)
		}
	}
	approvalPayload, _ := events[1].Metadata["approval"].(map[string]any)
	if approvalPayload["can_approve"] != false {
		t.Fatalf("approval event = %#v", events[0])
	}
}

func TestReadTextFileUsesPromptToolOutputLimit(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	limit := ToolOutputLimit{MaxBytes: 512, MaxLines: 80}
	cases := []struct {
		name    string
		path    string
		content string
		request acp.ReadTextFileRequest
	}{
		{
			name:    "no requested limit",
			path:    "large.txt",
			content: "HEAD\n" + strings.Repeat("read content\n", 300) + "TAIL\n",
			request: acp.ReadTextFileRequest{Path: "large.txt"},
		},
		{
			name:    "requested limit above configured limit",
			path:    "many-lines.txt",
			content: "HEAD\n" + strings.Repeat("read content\n", 300) + "TAIL\n",
			request: acp.ReadTextFileRequest{Path: "many-lines.txt", Limit: acp.Ptr(1000)},
		},
		{
			name:    "long single line",
			path:    "single-line.txt",
			content: "HEAD " + strings.Repeat("x", 5000) + " TAIL\n",
			request: acp.ReadTextFileRequest{Path: "single-line.txt"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(root, tc.path)
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			callbacks := &clientCallbacks{
				client:   newTestBridgeClient(t, root),
				root:     root,
				cwd:      root,
				events:   &toolEventEmitter{},
				approval: &fakeACPToolApproval{evaluation: toolapproval.Evaluation{Decision: toolapproval.DecisionBypass}},
				baseSession: ToolSessionContext{
					BotID: "bot-1", SessionID: "session-1",
				},
			}
			callbacks.setPromptState(newEventCollector(limit), nil, ToolSessionContext{}, limit)

			resp, err := callbacks.ReadTextFile(context.Background(), tc.request)
			if err != nil {
				t.Fatalf("ReadTextFile returned error: %v", err)
			}
			if len(resp.Content) > limit.MaxBytes {
				t.Fatalf("read content bytes = %d, want <= %d", len(resp.Content), limit.MaxBytes)
			}
			if len(resp.Content) >= len(tc.content) {
				t.Fatalf("read content was not limited")
			}
			for _, want := range []string{"[memoh pruned]", "HEAD", "TAIL"} {
				if !strings.Contains(resp.Content, want) {
					t.Fatalf("read content missing %q:\n%s", want, resp.Content)
				}
			}
		})
	}
}

func TestCallbackToolApprovalRejectionErrorsUsePromptToolOutputLimit(t *testing.T) {
	t.Parallel()

	reason := "HEAD\n" + strings.Repeat("rejection detail ", 300) + "\nTAIL"
	limit := ToolOutputLimit{MaxBytes: 512, MaxLines: 80}
	cases := []struct {
		name string
		run  func(context.Context, *clientCallbacks) error
	}{
		{
			name: "read",
			run: func(ctx context.Context, callbacks *clientCallbacks) error {
				_, err := callbacks.ReadTextFile(ctx, acp.ReadTextFileRequest{Path: "/data/input.txt"})
				return err
			},
		},
		{
			name: "write",
			run: func(ctx context.Context, callbacks *clientCallbacks) error {
				_, err := callbacks.WriteTextFile(ctx, acp.WriteTextFileRequest{Path: "/data/output.txt", Content: "hello\n"})
				return err
			},
		},
		{
			name: "terminal",
			run: func(ctx context.Context, callbacks *clientCallbacks) error {
				_, err := callbacks.CreateTerminal(ctx, acp.CreateTerminalRequest{Command: "pwd"})
				return err
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			approval := &fakeACPToolApproval{
				decision: toolapproval.Request{
					ID:             "approval-" + tc.name,
					Status:         toolapproval.StatusRejected,
					DecisionReason: reason,
					DecidedByUser:  true,
				},
			}
			callbacks := newClientCallbacks(
				context.Background(),
				nil,
				"/data",
				"/data",
				time.Second,
				nil,
				nil,
				nil,
				approval,
				nil,
				ToolSessionContext{
					BotID:     "bot-1",
					SessionID: "session-1",
					RunID:     "run-1",
				},
				acpprofile.DefaultToolQuirks(),
			)
			callbacks.setPromptState(newEventCollector(limit), nil, callbacks.baseSession, limit)

			err := tc.run(context.Background(), callbacks)
			if err == nil {
				t.Fatal("callback error = nil, want rejection")
			}
			message := err.Error()
			if len(message) > limit.MaxBytes {
				t.Fatalf("callback error bytes = %d, want <= %d\n%s", len(message), limit.MaxBytes, message)
			}
			if len(message) >= len(reason) {
				t.Fatalf("callback error was not limited")
			}
			for _, want := range []string{"[memoh pruned]", "HEAD", "TAIL"} {
				if !strings.Contains(message, want) {
					t.Fatalf("callback error missing %q:\n%s", want, message)
				}
			}
		})
	}
}

type fakeACPToolSource struct {
	tools []mcp.ToolDescriptor
}

func (s fakeACPToolSource) ListTools(context.Context, mcp.ToolSessionContext) ([]mcp.ToolDescriptor, error) {
	return append([]mcp.ToolDescriptor(nil), s.tools...), nil
}

func (fakeACPToolSource) CallTool(context.Context, mcp.ToolSessionContext, string, map[string]any) (map[string]any, error) {
	return mcp.BuildToolSuccessResult(map[string]any{"ok": true}), nil
}

func testACPToolGateway(toolNames ...string) *mcp.ToolGatewayService {
	tools := make([]mcp.ToolDescriptor, 0, len(toolNames))
	for _, name := range toolNames {
		tools = append(tools, mcp.ToolDescriptor{
			Name:        name,
			InputSchema: map[string]any{"type": "object"},
		})
	}
	return mcp.NewToolGatewayService(nil, []mcp.ToolSource{fakeACPToolSource{tools: tools}})
}

func TestMCPPermissionPreflightFromStateShapes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		request    acp.RequestPermissionRequest
		want       mcpPermissionPreflight
		wantParsed bool
	}{
		{
			name: "Codex generic title",
			request: acp.RequestPermissionRequest{
				ToolCall: acp.ToolCallUpdate{
					Title: acp.Ptr("Approve MCP tool call"),
					Kind:  acp.Ptr(acp.ToolKindOther),
					RawInput: map[string]any{
						"server_name": memohToolsMCPServerName,
						"method":      "tools/call",
						"params":      map[string]any{"name": "ask_user"},
					},
				},
			},
			want: mcpPermissionPreflight{
				toolName:        "ask_user",
				serverName:      memohToolsMCPServerName,
				hasToolName:     true,
				supportedMethod: true,
				shape:           mcpPermissionShapeGenericTitle,
			},
			wantParsed: true,
		},
		{
			name: "Claude Code structured title",
			request: acp.RequestPermissionRequest{
				ToolCall: acp.ToolCallUpdate{
					Title:    acp.Ptr("mcp__Memoh_Tools__ask_user"),
					Kind:     acp.Ptr(acp.ToolKindOther),
					RawInput: map[string]any{"questions": []any{}},
				},
			},
			want: mcpPermissionPreflight{
				toolName:        "ask_user",
				serverName:      memohToolsMCPServerSlug,
				hasToolName:     true,
				supportedMethod: true,
				shape:           mcpPermissionShapeStructuredTitle,
			},
			wantParsed: true,
		},
		{
			name: "plain unknown tool title",
			request: acp.RequestPermissionRequest{
				ToolCall: acp.ToolCallUpdate{
					Title:    acp.Ptr("some_custom_tool"),
					Kind:     acp.Ptr(acp.ToolKindOther),
					RawInput: map[string]any{"value": "ok"},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			state := &acpToolState{}
			mergePermissionToolUpdate(state, tc.request.ToolCall)
			got, ok := mcpPermissionPreflightFromState(state)
			if ok != tc.wantParsed {
				t.Fatalf("parsed = %v, want %v", ok, tc.wantParsed)
			}
			if !tc.wantParsed {
				return
			}
			if got != tc.want {
				t.Fatalf("preflight = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestRequestPermissionCorrelatesSparseCodexMCPUpdate(t *testing.T) {
	t.Parallel()

	const toolCallID = "exec-ask-user"
	sessionID := acp.SessionId("acp-session-1")
	approval := &fakeACPToolApproval{}
	callbacks := &clientCallbacks{
		root:        "/data",
		cwd:         "/data",
		approval:    approval,
		toolGateway: testACPToolGateway("ask_user"),
		baseSession: ToolSessionContext{
			BotID:             "bot-1",
			SessionID:         "session-1",
			RunID:             "run-1",
			ChannelIdentityID: "channel-1",
		},
		events:     &toolEventEmitter{},
		toolMapper: newACPToolEventMapper(acpprofile.DefaultToolQuirks()),
	}
	collector := newEventCollector()
	callbacks.setPromptState(collector, nil, callbacks.baseSession)

	request := acp.RequestPermissionRequest{
		Meta:      map[string]any{"is_mcp_tool_approval": true},
		SessionId: sessionID,
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId(toolCallID),
			Kind:       acp.Ptr(acp.ToolKindExecute),
			Status:     acp.Ptr(acp.ToolCallStatusPending),
		},
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: acp.PermissionOptionId("allow")},
			{Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: acp.PermissionOptionId("reject")},
		},
	}
	type permissionResult struct {
		response acp.RequestPermissionResponse
		err      error
	}
	resultCh := make(chan permissionResult, 1)
	go func() {
		resp, err := callbacks.RequestPermission(context.Background(), request)
		resultCh <- permissionResult{response: resp, err: err}
	}()

	// Reproduce the SDK ordering race: the inbound request handler starts
	// before the earlier session/update notification is processed.
	key := acpToolStateKey{sessionID: string(sessionID), toolCallID: toolCallID}
	deadline := time.NewTimer(time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		callbacks.toolMapper.mu.Lock()
		state := callbacks.toolMapper.tools[key]
		waiting := state != nil && state.title == "" && state.input == nil
		callbacks.toolMapper.mu.Unlock()
		if waiting {
			break
		}
		select {
		case result := <-resultCh:
			t.Fatalf("RequestPermission returned before matching session update: response=%#v err=%v", result.response, result.err)
		case <-deadline.C:
			t.Fatal("RequestPermission did not register sparse tool state")
		case <-ticker.C:
		}
	}

	err := callbacks.SessionUpdate(context.Background(), acp.SessionNotification{
		SessionId: sessionID,
		Update: acp.StartToolCall(
			acp.ToolCallId(toolCallID),
			"mcp.Memoh_Tools.ask_user",
			acp.WithStartKind(acp.ToolKindExecute),
			acp.WithStartStatus(acp.ToolCallStatusInProgress),
			acp.WithStartRawInput(map[string]any{
				"server":    memohToolsMCPServerSlug,
				"tool":      "ask_user",
				"arguments": map[string]any{"questions": []any{}},
			}),
		),
	})
	if err != nil {
		t.Fatalf("SessionUpdate error = %v", err)
	}
	if events := collector.result().Events; len(events) != 0 {
		t.Fatalf("MCP wrapper events = %#v, want none; the gateway owns the real tool event", events)
	}

	var result permissionResult
	select {
	case result = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("RequestPermission did not resume after matching session update")
	}
	if result.err != nil {
		t.Fatalf("RequestPermission error = %v", result.err)
	}
	resp := result.response
	if resp.Outcome.Selected == nil || resp.Outcome.Selected.OptionId != acp.PermissionOptionId("allow") {
		t.Fatalf("permission outcome = %#v, want allow once", resp.Outcome)
	}
	if got := approval.createdCount(); got != 0 {
		t.Fatalf("pending approvals created = %d, want 0 for scoped MCP gateway preflight", got)
	}

	// After the prompt reset the mapper state is gone, so the stale request
	// routes through the generic permission approval; a rejecting decision
	// with no reject_once option degrades to the cancelled fallback.
	callbacks.setPromptState(nil, nil, ToolSessionContext{})
	approval.mu.Lock()
	approval.decision = toolapproval.Request{Status: toolapproval.StatusRejected, DecidedByUser: true}
	approval.mu.Unlock()
	stale, err := callbacks.RequestPermission(context.Background(), acp.RequestPermissionRequest{
		Meta:      map[string]any{"is_mcp_tool_approval": true},
		SessionId: sessionID,
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId(toolCallID),
			Kind:       acp.Ptr(acp.ToolKindExecute),
			Status:     acp.Ptr(acp.ToolCallStatusPending),
		},
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: acp.PermissionOptionId("allow")},
		},
	})
	if err != nil {
		t.Fatalf("stale RequestPermission error = %v", err)
	}
	if stale.Outcome.Cancelled == nil {
		t.Fatalf("stale permission outcome = %#v, want cancelled after prompt reset", stale.Outcome)
	}
}

// TestRequestPermissionUnmappedToolAllowsWithoutApproval pins the two shapes
// still allowed without a user decision: think-kind permissions (no side
// effect worth gating) and MCP preflights whose structured tools/call payload
// points at Memoh's actual ACP tool gateway. Every other unmapped shape now
// routes through the generic permission approval.
func TestRequestPermissionUnmappedToolAllowsWithoutApproval(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		toolGateway *mcp.ToolGatewayService
		toolCall    acp.ToolCallUpdate
	}{
		{
			name:        "ACP MCP gateway tool",
			toolGateway: testACPToolGateway("native_tool"),
			toolCall: acp.ToolCallUpdate{
				ToolCallId: acp.ToolCallId("unknown-1"),
				Title:      acp.Ptr("Approve MCP tool call"),
				Kind:       acp.Ptr(acp.ToolKind("other")),
				RawInput: map[string]any{
					"server_name": memohToolsMCPServerName,
					"method":      "tools/call",
					"params": map[string]any{
						"name":      "native_tool",
						"arguments": map[string]any{"value": "ok"},
					},
				},
			},
		},
		{
			name:        "ACP MCP gateway wrapped Codex request",
			toolGateway: testACPToolGateway("native_tool"),
			toolCall: acp.ToolCallUpdate{
				ToolCallId: acp.ToolCallId("unknown-wrapped"),
				Title:      acp.Ptr("Approve MCP tool call"),
				Kind:       acp.Ptr(acp.ToolKindOther),
				RawInput: map[string]any{
					"id":          "approval-1",
					"turn_id":     "turn-1",
					"server_name": memohToolsMCPServerName,
					"request": map[string]any{
						"name":      "native_tool",
						"arguments": map[string]any{"value": "ok"},
					},
				},
			},
		},
		{
			name:        "ACP MCP gateway Codex request without tool name",
			toolGateway: testACPToolGateway("ask_user"),
			toolCall: acp.ToolCallUpdate{
				ToolCallId: acp.ToolCallId("unknown-codex-request"),
				Title:      acp.Ptr("Approve MCP tool call"),
				Kind:       acp.Ptr(acp.ToolKindOther),
				RawInput: map[string]any{
					"id":          "approval-2",
					"turn_id":     "turn-1",
					"server_name": memohToolsMCPServerSlug,
					"request": map[string]any{
						"_meta":            map[string]any{},
						"message":          "choose one",
						"mode":             "select",
						"requested_schema": map[string]any{"type": "object"},
					},
				},
			},
		},
		{
			name:        "ACP MCP gateway CallTool params",
			toolGateway: testACPToolGateway("native_tool"),
			toolCall: acp.ToolCallUpdate{
				ToolCallId: acp.ToolCallId("unknown-params"),
				Title:      acp.Ptr("Approve MCP tool call"),
				Kind:       acp.Ptr(acp.ToolKind("other")),
				RawInput: map[string]any{
					"server_name": memohToolsMCPServerName,
					"name":        "native_tool",
					"arguments":   map[string]any{"value": "ok"},
				},
			},
		},
		{
			name:        "Claude Code MCP title with direct tool args",
			toolGateway: testACPToolGateway("ask_user"),
			toolCall: acp.ToolCallUpdate{
				ToolCallId: acp.ToolCallId("claude-ask-user"),
				Title:      acp.Ptr("mcp__Memoh_Tools__ask_user"),
				Kind:       acp.Ptr(acp.ToolKindOther),
				RawInput: map[string]any{
					"questions": []any{
						map[string]any{"id": "answer", "question": "choose one"},
					},
				},
			},
		},
		{
			name: "think kind",
			toolCall: acp.ToolCallUpdate{
				ToolCallId: acp.ToolCallId("unknown-think"),
				Title:      acp.Ptr("Thinking"),
				Kind:       acp.Ptr(acp.ToolKindThink),
				RawInput:   map[string]any{"subject": "planning"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			approval := &fakeACPToolApproval{}
			callbacks := &clientCallbacks{
				root:        "/data",
				cwd:         "/data",
				approval:    approval,
				toolGateway: tc.toolGateway,
				baseSession: ToolSessionContext{
					BotID:             "bot-1",
					SessionID:         "session-1",
					RunID:             "run-1",
					ChannelIdentityID: "channel-1",
				},
				events: &toolEventEmitter{},
			}
			callbacks.setPromptState(newEventCollector(), nil, callbacks.baseSession)

			resp, err := callbacks.RequestPermission(context.Background(), acp.RequestPermissionRequest{
				ToolCall: tc.toolCall,
				Options: []acp.PermissionOption{
					{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: acp.PermissionOptionId("allow")},
					{Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: acp.PermissionOptionId("reject")},
				},
			})
			if err != nil {
				t.Fatalf("RequestPermission error = %v", err)
			}
			if resp.Outcome.Selected == nil || resp.Outcome.Selected.OptionId != acp.PermissionOptionId("allow") {
				t.Fatalf("permission outcome = %#v, want allow once", resp.Outcome)
			}
			if got := approval.createdCount(); got != 0 {
				t.Fatalf("pending approvals created = %d, want 0 (unmapped permissions bypass like native)", got)
			}
		})
	}
}

// ACP dispatches inbound requests on connection-scoped goroutines, so a
// permission callback for a stopped turn's tool call can arrive after the next
// prompt already started. It must resolve as cancelled instead of being
// re-attributed to - and creating an approval row under - the new turn.
func TestRequestPermissionAnswersStoppedTurnToolCallAsCancelled(t *testing.T) {
	t.Parallel()

	approval := &fakeACPToolApproval{}
	callbacks := &clientCallbacks{
		root:       "/data",
		cwd:        "/data",
		approval:   approval,
		toolMapper: newACPToolEventMapper(acpprofile.DefaultToolQuirks()),
		baseSession: ToolSessionContext{
			BotID: "bot-1", SessionID: "session-1", RunID: "run-1",
		},
		events: &toolEventEmitter{},
	}
	// Turn N: the agent advertises a tool call, then the user stops the turn.
	callbacks.setPromptState(newEventCollector(), nil, callbacks.baseSession)
	callbacks.toolMapper.applyToolCall("acp-session", acp.SessionUpdateToolCall{
		ToolCallId: "call-1", Title: "Run command", Kind: acp.ToolKindExecute,
	})
	callbacks.markPromptCancelled()
	callbacks.setPromptState(nil, nil, ToolSessionContext{})
	// Turn N+1 reuses the warm runtime.
	callbacks.setPromptState(newEventCollector(), nil, ToolSessionContext{
		BotID: "bot-1", SessionID: "session-1", RunID: "run-2",
	})

	resp, err := callbacks.RequestPermission(context.Background(), acp.RequestPermissionRequest{
		SessionId: acp.SessionId("acp-session"),
		ToolCall:  acp.ToolCallUpdate{ToolCallId: acp.ToolCallId("call-1")},
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: acp.PermissionOptionId("allow")},
			{Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: acp.PermissionOptionId("reject")},
		},
	})
	if err != nil {
		t.Fatalf("RequestPermission() error = %v", err)
	}
	if resp.Outcome.Cancelled == nil {
		t.Fatalf("permission outcome = %#v, want cancelled", resp.Outcome)
	}
	if got := approval.createdCount(); got != 0 {
		t.Fatalf("approval rows created = %d, want 0 for a stopped turn's tool call", got)
	}
	// The live turn advertising the same ID reclaims it from the tombstones.
	callbacks.toolMapper.applyToolCall("acp-session", acp.SessionUpdateToolCall{
		ToolCallId: "call-1", Title: "Run command", Kind: acp.ToolKindExecute,
	})
	if callbacks.toolMapper.isTombstoned(acp.SessionId("acp-session"), "call-1") {
		t.Fatal("tombstone survived a live tool-call advertisement")
	}
}

// Non-Memoh MCP and unmapped permission shapes go to the user in EVERY policy
// configuration: the callback must mark them ForceReview so a disabled
// approval policy cannot silently select an allow option on the user's behalf.
func TestRequestPermissionForcesReviewOnGenericLane(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		toolCall acp.ToolCallUpdate
	}{
		{
			name: "non-Memoh MCP tool call",
			toolCall: acp.ToolCallUpdate{
				ToolCallId: acp.ToolCallId("other-mcp-1"),
				Title:      acp.Ptr("Approve MCP tool call"),
				Kind:       acp.Ptr(acp.ToolKindOther),
				RawInput: map[string]any{
					"server_name": "Other Tools",
					"name":        "external_tool",
					"arguments":   map[string]any{"value": "ok"},
				},
			},
		},
		{
			name: "unmapped permission shape",
			toolCall: acp.ToolCallUpdate{
				ToolCallId: acp.ToolCallId("network-grant-1"),
				Title:      acp.Ptr("Allow network access"),
				Kind:       acp.Ptr(acp.ToolKindOther),
				RawInput:   map[string]any{"host": "example.test"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			approval := &fakeACPToolApproval{decision: toolapproval.Request{
				Status: toolapproval.StatusApproved, DecidedByUser: true,
			}}
			callbacks := &clientCallbacks{
				root:        "/data",
				cwd:         "/data",
				approval:    approval,
				toolGateway: testACPToolGateway("native_tool"),
				baseSession: ToolSessionContext{
					BotID: "bot-1", SessionID: "session-1", RunID: "run-1",
				},
				events: &toolEventEmitter{},
			}
			callbacks.setPromptState(newEventCollector(), nil, callbacks.baseSession)
			resp, err := callbacks.RequestPermission(context.Background(), acp.RequestPermissionRequest{
				ToolCall: tc.toolCall,
				Options: []acp.PermissionOption{
					{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: acp.PermissionOptionId("allow")},
					{Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: acp.PermissionOptionId("reject")},
				},
			})
			if err != nil {
				t.Fatalf("RequestPermission() error = %v", err)
			}
			if resp.Outcome.Selected == nil || resp.Outcome.Selected.OptionId != acp.PermissionOptionId("allow") {
				t.Fatalf("permission outcome = %#v, want allow once", resp.Outcome)
			}
			created := approval.createdInput()
			if created.ToolName != "permission" || !created.ForceReview {
				t.Fatalf("created approval = %+v, want generic permission with ForceReview", created)
			}
		})
	}
}

func TestRequestPermissionCarriesAgentOptionsVerbatim(t *testing.T) {
	t.Parallel()

	approval := &fakeACPToolApproval{decision: toolapproval.Request{
		Status: toolapproval.StatusApproved, DecidedByUser: true,
		SelectedOptionID: "allow-session",
	}}
	callbacks := &clientCallbacks{
		approval:    approval,
		toolGateway: testACPToolGateway("native_tool"),
		baseSession: ToolSessionContext{
			BotID: "bot-1", SessionID: "session-1", RunID: "run-1",
		},
		events: &toolEventEmitter{},
	}
	callbacks.setPromptState(newEventCollector(), nil, callbacks.baseSession)
	response, err := callbacks.RequestPermission(context.Background(), acp.RequestPermissionRequest{
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: "elicitation-consent-1", Title: acp.Ptr("Approve MCP tool call"), Kind: acp.Ptr(acp.ToolKindOther),
			RawInput: map[string]any{
				"serverName": "Memoh Tools",
				"url":        "https://example.test/authorize",
			},
		},
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKindAllowOnce, Name: "This turn", OptionId: "allow-once"},
			{Kind: acp.PermissionOptionKindAllowAlways, Name: "This session", OptionId: "allow-session"},
			{Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: "reject-once"},
		},
	})
	if err != nil {
		t.Fatalf("RequestPermission() error = %v", err)
	}
	if response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != "allow-session" {
		t.Fatalf("outcome = %#v, want selected agent option", response.Outcome)
	}
	created := approval.createdInput()
	if created.ToolName != "permission" || len(created.Options) != 3 || created.Options[1].ID != "allow-session" || !created.ForceReview {
		t.Fatalf("created approval = %#v", created)
	}
}

// TestRequestPermissionDriftedConsentShapeStillForcesReview pins the fail-closed
// contract for consent detection: even when the adapter's "elicitation-" id
// prefix drifts and extra raw-input keys appear, a URL-bearing ask for a Memoh
// server name must land on the forced-review lane, never on the MCP-preflight
// auto-allow — a consent is never a policy shortcut.
func TestRequestPermissionDriftedConsentShapeStillForcesReview(t *testing.T) {
	t.Parallel()

	approval := &fakeACPToolApproval{decision: toolapproval.Request{
		Status: toolapproval.StatusApproved, DecidedByUser: true,
		SelectedOptionID: "allow-once",
	}}
	callbacks := &clientCallbacks{
		approval:    approval,
		toolGateway: testACPToolGateway("native_tool"),
		baseSession: ToolSessionContext{
			BotID: "bot-1", SessionID: "session-1", RunID: "run-1",
		},
		events: &toolEventEmitter{},
	}
	callbacks.setPromptState(newEventCollector(), nil, callbacks.baseSession)
	response, err := callbacks.RequestPermission(context.Background(), acp.RequestPermissionRequest{
		ToolCall: acp.ToolCallUpdate{
			// Drifted upstream shape: renamed id prefix and a third raw-input key.
			ToolCallId: "consent-2", Title: acp.Ptr("Approve MCP tool call"), Kind: acp.Ptr(acp.ToolKindOther),
			RawInput: map[string]any{
				"serverName": "Memoh Tools",
				"url":        "https://example.test/authorize",
				"detail":     "extra field a future adapter might add",
			},
		},
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: "allow-once"},
			{Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: "reject-once"},
		},
	})
	if err != nil {
		t.Fatalf("RequestPermission() error = %v", err)
	}
	if response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != "allow-once" {
		t.Fatalf("outcome = %#v, want the user's selected option", response.Outcome)
	}
	created := approval.createdInput()
	if created.ToolName != "permission" || !created.ForceReview {
		t.Fatalf("created approval = %+v, want generic permission with ForceReview (not auto-allowed)", created)
	}
}

// TestRequestPermissionUnsupportedOptionKindCancels pins the open-enum
// contract: a vendor or future option kind is not a protocol violation, so the
// request resolves as cancelled instead of a JSON-RPC InvalidParams error that
// would abort the agent's whole turn.
func TestRequestPermissionUnsupportedOptionKindCancels(t *testing.T) {
	t.Parallel()

	approval := &fakeACPToolApproval{}
	callbacks := &clientCallbacks{
		root:     "/data",
		cwd:      "/data",
		approval: approval,
		baseSession: ToolSessionContext{
			BotID:     "bot-1",
			SessionID: "session-1",
			RunID:     "run-1",
		},
		events: &toolEventEmitter{},
	}
	callbacks.setPromptState(newEventCollector(), nil, callbacks.baseSession)

	resp, err := callbacks.RequestPermission(context.Background(), acp.RequestPermissionRequest{
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId("exec-1"),
			Title:      acp.Ptr("Shell"),
			Kind:       acp.Ptr(acp.ToolKindExecute),
			RawInput:   map[string]any{"command": "ls"},
		},
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKind("confirm"), Name: "Confirm", OptionId: acp.PermissionOptionId("confirm")},
		},
	})
	if err != nil {
		t.Fatalf("RequestPermission error = %v", err)
	}
	if resp.Outcome.Cancelled == nil {
		t.Fatalf("permission outcome = %#v, want cancelled for unmappable options", resp.Outcome)
	}
	if got := approval.createdCount(); got != 0 {
		t.Fatalf("pending approvals created = %d, want 0 for unmappable options", got)
	}
}

// TestRequestPermissionNormalizesOptionKindCase pins that non-canonical option
// kind casing is tolerated end to end: storage and classification both see the
// normalized kind, so the approval is answered instead of hard-failing.
func TestRequestPermissionNormalizesOptionKindCase(t *testing.T) {
	t.Parallel()

	approval := &fakeACPToolApproval{
		decision: toolapproval.Request{
			ID:            "approval-3",
			Status:        toolapproval.StatusApproved,
			DecidedByUser: true,
		},
	}
	callbacks := &clientCallbacks{
		root:     "/data",
		cwd:      "/data",
		approval: approval,
		baseSession: ToolSessionContext{
			BotID:     "bot-1",
			SessionID: "session-1",
			RunID:     "run-1",
		},
		events: &toolEventEmitter{},
	}
	callbacks.setPromptState(newEventCollector(), nil, callbacks.baseSession)

	resp, err := callbacks.RequestPermission(context.Background(), acp.RequestPermissionRequest{
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId("exec-2"),
			Title:      acp.Ptr("Shell"),
			Kind:       acp.Ptr(acp.ToolKindExecute),
			RawInput:   map[string]any{"command": "ls"},
		},
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKind("Allow_Once"), Name: "Allow", OptionId: acp.PermissionOptionId("allow")},
			{Kind: acp.PermissionOptionKind("REJECT_ONCE"), Name: "Reject", OptionId: acp.PermissionOptionId("reject")},
		},
	})
	if err != nil {
		t.Fatalf("RequestPermission error = %v", err)
	}
	if resp.Outcome.Selected == nil || resp.Outcome.Selected.OptionId != acp.PermissionOptionId("allow") {
		t.Fatalf("permission outcome = %#v, want allow once", resp.Outcome)
	}
	if len(approval.created.Options) != 2 || approval.created.Options[0].Kind != toolapproval.OptionKindAllowOnce {
		t.Fatalf("stored options = %#v, want normalized kinds", approval.created.Options)
	}
}

// TestRequestPermissionNonInteractiveCancels pins the system-outcome contract:
// with nobody attached who could answer (no RunID), the auto-reject must reach
// the agent as a cancelled outcome, not as the reject_once option — a per-tool
// refusal would invite an unattended agent to retry-loop against rejections no
// user made.
func TestRequestPermissionNonInteractiveCancels(t *testing.T) {
	t.Parallel()

	approval := &fakeACPToolApproval{}
	callbacks := &clientCallbacks{
		root:     "/data",
		cwd:      "/data",
		approval: approval,
		baseSession: ToolSessionContext{
			BotID:             "bot-1",
			SessionID:         "session-1",
			ChannelIdentityID: "channel-1",
			// No RunID: nobody can see or answer the approval.
		},
		events: &toolEventEmitter{},
	}
	callbacks.setPromptState(newEventCollector(), nil, callbacks.baseSession)

	resp, err := callbacks.RequestPermission(context.Background(), acp.RequestPermissionRequest{
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId("exec-bg"),
			Title:      acp.Ptr("rm -rf /data/tmp"),
			Kind:       acp.Ptr(acp.ToolKindExecute),
			RawInput:   map[string]any{"command": "rm -rf /data/tmp"},
		},
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: acp.PermissionOptionId("allow")},
			{Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: acp.PermissionOptionId("reject")},
		},
	})
	if err != nil {
		t.Fatalf("RequestPermission error = %v", err)
	}
	if resp.Outcome.Cancelled == nil {
		t.Fatalf("permission outcome = %#v, want cancelled for system rejection", resp.Outcome)
	}
}

func TestRequestPermissionRejectedByMemohToolApprovalSelectsRejectOption(t *testing.T) {
	t.Parallel()

	approval := &fakeACPToolApproval{
		decision: toolapproval.Request{
			ID:            "approval-2",
			ShortID:       10,
			Status:        toolapproval.StatusRejected,
			DecidedByUser: true,
		},
	}
	callbacks := &clientCallbacks{
		root:     "/data",
		cwd:      "/data",
		approval: approval,
		baseSession: ToolSessionContext{
			BotID:     "bot-1",
			SessionID: "session-1",
			RunID:     "run-1",
		},
		events: &toolEventEmitter{},
	}
	collector := newEventCollector()
	callbacks.setPromptState(collector, nil, callbacks.baseSession)

	resp, err := callbacks.RequestPermission(context.Background(), acp.RequestPermissionRequest{
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId("exec-1"),
			Title:      acp.Ptr("Shell"),
			Kind:       acp.Ptr(acp.ToolKindExecute),
			RawInput:   map[string]any{"command": "rm -rf *"},
		},
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: acp.PermissionOptionId("allow")},
			{Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: acp.PermissionOptionId("reject")},
		},
	})
	if err != nil {
		t.Fatalf("RequestPermission error = %v", err)
	}
	if resp.Outcome.Selected == nil || resp.Outcome.Selected.OptionId != acp.PermissionOptionId("reject") {
		t.Fatalf("permission outcome = %#v, want reject once", resp.Outcome)
	}
	if approval.created.ToolName != "exec" {
		t.Fatalf("approval input = %#v", approval.created)
	}
	var sawRejected bool
	for _, ev := range collector.result().Events {
		if ev.Type == event.ToolApprovalRequest &&
			ev.ToolCallID == "exec-1" &&
			ev.ApprovalID == "approval-2" &&
			ev.Status == toolapproval.StatusRejected {
			sawRejected = true
		}
	}
	if !sawRejected {
		t.Fatalf("events = %#v, want rejected approval update", collector.result().Events)
	}
}

func TestCreateTerminalUsesMemohToolApproval(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	client := newTestBridgeClient(t, root)
	approval := &fakeACPToolApproval{
		decision: toolapproval.Request{
			ID:      "approval-terminal",
			ShortID: 11,
			Status:  toolapproval.StatusApproved,
		},
	}
	callbacks := newClientCallbacks(
		context.Background(),
		client,
		"/data",
		"/data",
		time.Second,
		nil,
		nil,
		nil,
		approval,
		nil,
		ToolSessionContext{
			BotID:             "bot-1",
			SessionID:         "session-1",
			RunID:             "run-1",
			ChannelIdentityID: "channel-1",
		},
		acpprofile.DefaultToolQuirks(),
	)
	collector := newEventCollector()
	callbacks.setPromptState(collector, nil, callbacks.baseSession)

	term, err := callbacks.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
		Command: "printf",
		Args:    []string{"terminal-ok"},
	})
	if err != nil {
		t.Fatalf("CreateTerminal error = %v", err)
	}
	if approval.created.ToolCallID != "terminal-term-1" || approval.created.ToolName != "exec" {
		t.Fatalf("approval input = %#v", approval.created)
	}
	input, ok := approval.created.ToolInput.(map[string]any)
	if !ok || input["command"] != "printf terminal-ok" {
		t.Fatalf("approval command input = %#v", approval.created.ToolInput)
	}

	if _, err := callbacks.WaitForTerminalExit(context.Background(), acp.WaitForTerminalExitRequest{TerminalId: term.TerminalId}); err != nil {
		t.Fatalf("WaitForTerminalExit error = %v", err)
	}
	events := collector.result().Events
	var sawStart, sawApproval, sawEnd bool
	for _, ev := range events {
		if ev.ToolCallID != "terminal-term-1" {
			continue
		}
		switch ev.Type {
		case event.ToolCallStart:
			sawStart = ev.ToolName == "exec"
		case event.ToolApprovalRequest:
			sawApproval = ev.ApprovalID == "approval-terminal"
		case event.ToolCallEnd:
			sawEnd = ev.ToolName == "exec"
		}
	}
	if !sawStart || !sawApproval || !sawEnd {
		t.Fatalf("terminal events start=%v approval=%v end=%v events=%#v", sawStart, sawApproval, sawEnd, events)
	}
}

func TestCreateTerminalRejectedByMemohToolApprovalDoesNotStartTerminal(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	client := newTestBridgeClient(t, root)
	approval := &fakeACPToolApproval{
		decision: toolapproval.Request{
			ID:            "approval-terminal-reject",
			ShortID:       12,
			Status:        toolapproval.StatusRejected,
			DecidedByUser: true,
		},
	}
	callbacks := newClientCallbacks(
		context.Background(),
		client,
		"/data",
		"/data",
		time.Second,
		nil,
		nil,
		nil,
		approval,
		nil,
		ToolSessionContext{
			BotID:     "bot-1",
			SessionID: "session-1",
			RunID:     "run-1",
		},
		acpprofile.DefaultToolQuirks(),
	)
	collector := newEventCollector()
	callbacks.setPromptState(collector, nil, callbacks.baseSession)

	_, err := callbacks.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
		Command: "pwd",
	})
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("CreateTerminal error = %v, want rejected", err)
	}
	result := collector.result()
	events := result.Events
	startIdx, endIdx := -1, -1
	for idx, ev := range events {
		if ev.ToolCallID != "terminal-term-1" {
			continue
		}
		switch ev.Type {
		case event.ToolCallStart:
			if startIdx < 0 {
				startIdx = idx
			}
		case event.ToolCallEnd:
			if strings.Contains(ev.Error, "rejected") {
				endIdx = idx
			}
		}
	}
	if startIdx < 0 || endIdx < 0 || startIdx > endIdx {
		t.Fatalf("events = %#v, want terminal tool_call_start before rejected tool_call_end", events)
	}
	if len(result.Output) != 2 {
		t.Fatalf("transcript output = %#v, want assistant tool call and tool result", result.Output)
	}
	if result.Output[0].Role != sdk.MessageRoleAssistant {
		t.Fatalf("first transcript message = %#v, want assistant tool call", result.Output[0])
	}
	if _, ok := result.Output[0].Content[0].(sdk.ToolCallPart); !ok {
		t.Fatalf("first transcript part = %#v, want tool call", result.Output[0].Content[0])
	}
	if _, ok := result.Output[1].Content[0].(sdk.ToolResultPart); !ok {
		t.Fatalf("second transcript part = %#v, want tool result", result.Output[1].Content[0])
	}
}

func TestWriteTextFileUsesMemohToolApproval(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	client := newTestBridgeClient(t, root)
	approval := &fakeACPToolApproval{
		decision: toolapproval.Request{
			ID:      "approval-write",
			ShortID: 13,
			Status:  toolapproval.StatusApproved,
		},
	}
	callbacks := newClientCallbacks(
		context.Background(),
		client,
		"/data",
		"/data",
		time.Second,
		nil,
		nil,
		nil,
		approval,
		nil,
		ToolSessionContext{
			BotID:             "bot-1",
			SessionID:         "session-1",
			RunID:             "run-1",
			ChannelIdentityID: "channel-1",
		},
		acpprofile.DefaultToolQuirks(),
	)
	collector := newEventCollector()
	callbacks.setPromptState(collector, nil, callbacks.baseSession)

	if _, err := callbacks.WriteTextFile(context.Background(), acp.WriteTextFileRequest{
		Path:    "/data/review.txt",
		Content: "review me\n",
	}); err != nil {
		t.Fatalf("WriteTextFile error = %v", err)
	}
	if approval.created.ToolName != "write" {
		t.Fatalf("approval input = %#v", approval.created)
	}
	input, ok := approval.created.ToolInput.(map[string]any)
	if !ok || input["path"] != "/data/review.txt" || input["content"] != "review me\n" {
		t.Fatalf("approval tool input = %#v", approval.created.ToolInput)
	}
	written, err := os.ReadFile(filepath.Join(root, "review.txt")) //nolint:gosec // reads from t.TempDir
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(written) != "review me\n" {
		t.Fatalf("written content = %q", written)
	}
	assertSingleApprovalWithStartEnd(t, collector.result().Events, approval.created.ToolCallID, "write", "approval-write")
}

func TestACPFileCallbacksRecheckRuntimeGuardAfterApproval(t *testing.T) {
	guardErr := errors.New("runtime ownership lost")
	tests := []struct {
		name string
		run  func(context.Context, *clientCallbacks) error
	}{
		{
			name: "read",
			run: func(ctx context.Context, callbacks *clientCallbacks) error {
				_, err := callbacks.ReadTextFile(ctx, acp.ReadTextFileRequest{Path: "/data/input.txt"})
				return err
			},
		},
		{
			name: "write",
			run: func(ctx context.Context, callbacks *clientCallbacks) error {
				_, err := callbacks.WriteTextFile(ctx, acp.WriteTextFileRequest{Path: "/data/output.txt", Content: "stale\n"})
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, server := newRecordingBridgeClient(t)
			approval := &fakeACPToolApproval{decision: toolapproval.Request{
				ID: "approval-" + tt.name, Status: toolapproval.StatusApproved,
			}}
			guardCalls := 0
			callbacks := newClientCallbacks(
				context.Background(), client, "/data", "/data", time.Second,
				nil, nil, nil, approval, nil,
				ToolSessionContext{
					BotID: "bot-1", SessionID: "session-1", RunID: "run-1",
					RuntimeGuard: func(context.Context) error {
						guardCalls++
						return guardErr
					},
				},
				acpprofile.DefaultToolQuirks(),
			)
			callbacks.setPromptState(newEventCollector(), nil, callbacks.baseSession)

			if err := tt.run(context.Background(), callbacks); !errors.Is(err, guardErr) {
				t.Fatalf("%s error = %v, want runtime guard error", tt.name, err)
			}
			if approval.createdCount() != 1 || guardCalls != 1 {
				t.Fatalf("%s approval/guard calls = %d/%d, want 1/1", tt.name, approval.createdCount(), guardCalls)
			}
			if reads, writes := server.readPaths(), server.writes(); len(reads) != 0 || len(writes) != 0 {
				t.Fatalf("%s bridge effects after guard rejection = reads:%#v writes:%#v", tt.name, reads, writes)
			}
		})
	}
}

func TestACPCreateTerminalRechecksRuntimeGuardAfterApproval(t *testing.T) {
	guardErr := errors.New("runtime ownership lost")
	client, server := newRecordingBridgeClient(t)
	approval := &fakeACPToolApproval{decision: toolapproval.Request{
		ID: "approval-terminal-guard", Status: toolapproval.StatusApproved,
	}}
	callbacks := newClientCallbacks(
		context.Background(), client, "/workspace", "/workspace", time.Second,
		nil, nil, nil, approval, nil,
		ToolSessionContext{
			BotID: "bot-1", SessionID: "session-1", RunID: "run-1",
			RuntimeGuard: func(context.Context) error { return guardErr },
		},
		acpprofile.DefaultToolQuirks(),
	)
	callbacks.setPromptState(newEventCollector(), nil, callbacks.baseSession)

	if _, err := callbacks.CreateTerminal(context.Background(), acp.CreateTerminalRequest{Command: "echo stale"}); !errors.Is(err, guardErr) {
		t.Fatalf("CreateTerminal() error = %v, want runtime guard error", err)
	}
	if approval.createdCount() != 1 {
		t.Fatalf("terminal approval calls = %d, want 1", approval.createdCount())
	}
	if records := server.records(); len(records) != 0 {
		t.Fatalf("terminal effects after guard rejection = %#v", records)
	}
}

func TestACPWorkspaceEffectsRejectStaleRedisOwner(t *testing.T) {
	redisURL := strings.TrimSpace(os.Getenv("MEMOH_TEST_REDIS_URL"))
	if redisURL == "" {
		redisURL = strings.TrimSpace(os.Getenv("MEMOH_TEST_VALKEY_URL"))
	}
	if redisURL == "" {
		if os.Getenv("MEMOH_TEST_DISTRIBUTED_REQUIRED") == "1" {
			t.Fatal("distributed ACP workspace guard test is required, but Redis or Valkey is not configured")
		}
		t.Skip("set MEMOH_TEST_REDIS_URL or MEMOH_TEST_VALKEY_URL to run distributed ACP workspace guard test")
	}

	ctx := context.Background()
	prefix := fmt.Sprintf("memoh:test:acp-workspace-guard:%d:", time.Now().UnixNano())
	rawOwnerBackend, err := sessionruntime.NewRedisBackend(ctx, sessionruntime.RedisOptions{URL: redisURL, KeyPrefix: prefix, StateTTL: time.Minute})
	if err != nil {
		t.Fatalf("create stale owner backend: %v", err)
	}
	t.Cleanup(func() { _ = rawOwnerBackend.Close() })
	owner := sessionruntime.NewManager(nonClosingDistributedBackend{DistributedBackend: rawOwnerBackend}, sessionruntime.Options{
		OwnerID: "acp-workspace-owner-a", StateTTL: time.Minute, OwnerLeaseTTL: 100 * time.Millisecond,
	})
	if err := owner.Start(ctx); err != nil {
		t.Fatalf("start stale owner manager: %v", err)
	}
	backendB, err := sessionruntime.NewRedisBackend(ctx, sessionruntime.RedisOptions{URL: redisURL, KeyPrefix: prefix, StateTTL: time.Minute})
	if err != nil {
		t.Fatalf("create takeover backend: %v", err)
	}
	takeover := sessionruntime.NewManager(backendB, sessionruntime.Options{
		OwnerID: "acp-workspace-owner-b", StateTTL: time.Minute, OwnerLeaseTTL: 100 * time.Millisecond,
	})
	if err := takeover.Start(ctx); err != nil {
		t.Fatalf("start takeover manager: %v", err)
	}
	t.Cleanup(func() { _ = takeover.Close() })

	const (
		botID     = "bot-acp-workspace-guard"
		sessionID = "session-acp-workspace-guard"
		streamA   = "stream-acp-workspace-owner-a"
		streamB   = "stream-acp-workspace-owner-b"
	)
	ownerHandle, err := owner.StartRunHandle(ctx, botID, sessionID, streamA, make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1))
	if err != nil {
		t.Fatalf("start stale owner run: %v", err)
	}
	ownerSnapshot, ok, err := rawOwnerBackend.Load(ctx, sessionruntime.Key{BotID: botID, SessionID: sessionID})
	if err != nil || !ok || ownerSnapshot.CurrentRunView == nil || ownerSnapshot.CurrentRunView.OwnerLeaseExpiresAt == nil {
		t.Fatalf("load stale owner lease = snapshot:%#v ok:%v err:%v", ownerSnapshot, ok, err)
	}
	ownerDeadline := *ownerSnapshot.CurrentRunView.OwnerLeaseExpiresAt
	if err := owner.Close(); err != nil {
		t.Fatalf("stop stale owner lease renewal: %v", err)
	}
	for {
		now, err := backendB.Now(ctx)
		if err != nil {
			t.Fatalf("load Redis time: %v", err)
		}
		if !now.Before(ownerDeadline.Add(10 * time.Millisecond)) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := takeover.Snapshot(ctx, botID, sessionID); err != nil {
		t.Fatalf("reconcile expired owner: %v", err)
	}
	if err := takeover.StartRun(ctx, botID, sessionID, streamB, make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start takeover run: %v", err)
	}

	bridgeClient, bridgeServer := newRecordingBridgeClient(t)
	approval := &fakeACPToolApproval{decision: toolapproval.Request{ID: "approval-stale-owner", Status: toolapproval.StatusApproved}}
	callbacks := newClientCallbacks(
		ctx, bridgeClient, "/data", "/data", time.Second, nil, nil, nil, approval, nil,
		ToolSessionContext{
			BotID: botID, SessionID: sessionID, RunID: streamA,
			RuntimeGuard: func(guardCtx context.Context) error {
				return owner.ValidateRunOwnership(guardCtx, ownerHandle)
			},
		},
		acpprofile.DefaultToolQuirks(),
	)
	callbacks.setPromptState(newEventCollector(), nil, callbacks.baseSession)
	for name, call := range map[string]func() error{
		"read": func() error {
			_, err := callbacks.ReadTextFile(ctx, acp.ReadTextFileRequest{Path: "/data/input.txt"})
			return err
		},
		"write": func() error {
			_, err := callbacks.WriteTextFile(ctx, acp.WriteTextFileRequest{Path: "/data/output.txt", Content: "stale\n"})
			return err
		},
		"terminal": func() error {
			_, err := callbacks.CreateTerminal(ctx, acp.CreateTerminalRequest{Command: "echo stale"})
			return err
		},
	} {
		if err := call(); !errors.Is(err, sessionruntime.ErrRunOwnershipLost) {
			t.Fatalf("stale %s error = %v, want ErrRunOwnershipLost", name, err)
		}
	}
	if reads, writes, execs := bridgeServer.readPaths(), bridgeServer.writes(), bridgeServer.records(); len(reads) != 0 || len(writes) != 0 || len(execs) != 0 {
		t.Fatalf("stale owner bridge effects = reads:%#v writes:%#v execs:%#v", reads, writes, execs)
	}
}

type nonClosingDistributedBackend struct {
	sessionruntime.DistributedBackend
}

func (nonClosingDistributedBackend) Close() error { return nil }

func TestWriteTextFileWithoutToolSessionIsRejectedWhenApprovalEnabled(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	client := newTestBridgeClient(t, root)
	callbacks := newClientCallbacks(
		context.Background(),
		client,
		"/data",
		"/data",
		time.Second,
		nil,
		nil,
		nil,
		&fakeACPToolApproval{decision: toolapproval.Request{Status: toolapproval.StatusApproved}},
		nil,
		ToolSessionContext{BotID: "bot-1"},
		acpprofile.DefaultToolQuirks(),
	)
	collector := newEventCollector()
	callbacks.setPromptState(collector, nil, callbacks.baseSession)

	_, err := callbacks.WriteTextFile(context.Background(), acp.WriteTextFileRequest{
		Path:    "/data/review.txt",
		Content: "review me\n",
	})
	// No session identity means nobody could be asked: a system outcome, so
	// the message must say "not approved", never "rejected by user".
	if err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Fatalf("WriteTextFile error = %v, want not approved", err)
	}
	if _, err := os.Stat(filepath.Join(root, "review.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("written file stat error = %v, want not exist", err)
	}
	events := collector.result().Events
	var sawRejectedEnd bool
	for _, ev := range events {
		if ev.Type == event.ToolCallEnd && ev.ToolName == "write" && strings.Contains(ev.Error, "not approved") {
			sawRejectedEnd = true
		}
	}
	if !sawRejectedEnd {
		t.Fatalf("events = %#v, want not-approved tool_call_end", events)
	}
}

// TestRequestPermissionScopeRejectsOutOfRootPaths guards the pre-ask scope
// gate: every key the native extraction reads a path from (file_path et al.)
// and diff-carried paths must be confined to the workspace root. A thick
// agent executes the approved action itself, so this gate is the only chance
// to stop an out-of-root target before a user is asked to approve it.
func TestRequestPermissionScopeRejectsOutOfRootPaths(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		toolCall acp.ToolCallUpdate
	}{
		{
			name: "unmapped path outside root",
			toolCall: acp.ToolCallUpdate{
				ToolCallId: acp.ToolCallId("unknown-out-of-scope"),
				Title:      acp.Ptr("Custom action"),
				RawInput:   map[string]any{"path": "/etc/passwd"},
			},
		},
		{
			name: "file_path key outside root",
			toolCall: acp.ToolCallUpdate{
				ToolCallId: acp.ToolCallId("write-escape"),
				Title:      acp.Ptr("Write file"),
				Kind:       acp.Ptr(acp.ToolKindEdit),
				RawInput:   map[string]any{"file_path": "/etc/passwd", "content": "x"},
			},
		},
		{
			name: "filePath key outside root",
			toolCall: acp.ToolCallUpdate{
				ToolCallId: acp.ToolCallId("write-escape-camel"),
				Title:      acp.Ptr("Write file"),
				Kind:       acp.Ptr(acp.ToolKindEdit),
				RawInput:   map[string]any{"filePath": "/etc/passwd", "content": "x"},
			},
		},
		{
			name: "diff path outside root",
			toolCall: acp.ToolCallUpdate{
				ToolCallId: acp.ToolCallId("edit-escape-diff"),
				Title:      acp.Ptr("Edit file"),
				Kind:       acp.Ptr(acp.ToolKindEdit),
				Content: []acp.ToolCallContent{{
					Diff: &acp.ToolCallContentDiff{Path: "/etc/cron.d/evil", NewText: "boom"},
				}},
			},
		},
		{
			// cwd/work_dir are the exec-escape keys: a command gated for the
			// workspace must not run with a working directory outside the root.
			name: "cwd key outside root",
			toolCall: acp.ToolCallUpdate{
				ToolCallId: acp.ToolCallId("exec-escape-cwd"),
				Title:      acp.Ptr("Run command"),
				Kind:       acp.Ptr(acp.ToolKindExecute),
				RawInput:   map[string]any{"command": "ls", "cwd": "/etc"},
			},
		},
		{
			name: "work_dir key outside root",
			toolCall: acp.ToolCallUpdate{
				ToolCallId: acp.ToolCallId("exec-escape-workdir"),
				Title:      acp.Ptr("Run command"),
				Kind:       acp.Ptr(acp.ToolKindExecute),
				RawInput:   map[string]any{"command": "ls", "work_dir": "/etc"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			approval := &fakeACPToolApproval{decision: toolapproval.Request{Status: toolapproval.StatusApproved}}
			callbacks := &clientCallbacks{
				root:     "/data",
				cwd:      "/data",
				approval: approval,
				baseSession: ToolSessionContext{
					BotID:             "bot-1",
					SessionID:         "session-1",
					RunID:             "run-1",
					ChannelIdentityID: "channel-1",
				},
				events: &toolEventEmitter{},
			}
			callbacks.setPromptState(newEventCollector(), nil, callbacks.baseSession)

			resp, err := callbacks.RequestPermission(context.Background(), acp.RequestPermissionRequest{
				ToolCall: tc.toolCall,
				Options: []acp.PermissionOption{
					{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: acp.PermissionOptionId("allow")},
				},
			})
			if err != nil {
				t.Fatalf("RequestPermission error = %v", err)
			}
			if resp.Outcome.Cancelled == nil {
				t.Fatalf("permission outcome = %#v, want cancelled for out-of-root path", resp.Outcome)
			}
			if got := approval.createdCount(); got != 0 {
				t.Fatalf("pending approvals created = %d, want 0 - the user must never be asked to approve an out-of-scope action", got)
			}
		})
	}

	t.Run("path from correlated tool state outside root", func(t *testing.T) {
		approval := &fakeACPToolApproval{decision: toolapproval.Request{Status: toolapproval.StatusApproved}}
		callbacks := &clientCallbacks{
			root:     "/data",
			cwd:      "/data",
			approval: approval,
			baseSession: ToolSessionContext{
				BotID:             "bot-1",
				SessionID:         "session-1",
				RunID:             "run-1",
				ChannelIdentityID: "channel-1",
			},
			events:     &toolEventEmitter{},
			toolMapper: newACPToolEventMapper(acpprofile.DefaultToolQuirks()),
		}
		callbacks.setPromptState(newEventCollector(), nil, callbacks.baseSession)
		sessionID := acp.SessionId("acp-session-scope")
		if err := callbacks.SessionUpdate(context.Background(), acp.SessionNotification{
			SessionId: sessionID,
			Update: acp.StartToolCall(
				acp.ToolCallId("write-cached-escape"),
				"Write file",
				acp.WithStartKind(acp.ToolKindEdit),
				acp.WithStartStatus(acp.ToolCallStatusInProgress),
				acp.WithStartRawInput(map[string]any{"file_path": "/etc/passwd", "content": "x"}),
			),
		}); err != nil {
			t.Fatalf("SessionUpdate error = %v", err)
		}

		resp, err := callbacks.RequestPermission(context.Background(), acp.RequestPermissionRequest{
			SessionId: sessionID,
			ToolCall: acp.ToolCallUpdate{
				ToolCallId: acp.ToolCallId("write-cached-escape"),
				Kind:       acp.Ptr(acp.ToolKindEdit),
				Status:     acp.Ptr(acp.ToolCallStatusPending),
			},
			Options: []acp.PermissionOption{
				{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: acp.PermissionOptionId("allow")},
			},
		})
		if err != nil {
			t.Fatalf("RequestPermission error = %v", err)
		}
		if resp.Outcome.Cancelled == nil {
			t.Fatalf("permission outcome = %#v, want cancelled for cached out-of-root path", resp.Outcome)
		}
		if got := approval.createdCount(); got != 0 {
			t.Fatalf("pending approvals created = %d, want 0", got)
		}
	})
}

func TestRequestPermissionGrantDedupesWriteTextFileApproval(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	client := newTestBridgeClient(t, root)
	approval := &fakeACPToolApproval{
		decision: toolapproval.Request{
			ID:      "approval-write",
			ShortID: 14,
			Status:  toolapproval.StatusApproved,
		},
	}
	callbacks := newClientCallbacks(
		context.Background(),
		client,
		"/data",
		"/data",
		time.Second,
		nil,
		nil,
		nil,
		approval,
		nil,
		ToolSessionContext{
			BotID:             "bot-1",
			SessionID:         "session-1",
			RunID:             "run-1",
			ChannelIdentityID: "channel-1",
		},
		acpprofile.DefaultToolQuirks(),
	)
	collector := newEventCollector()
	callbacks.setPromptState(collector, nil, callbacks.baseSession)

	permission, err := callbacks.RequestPermission(context.Background(), acp.RequestPermissionRequest{
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId("write-1"),
			Title:      acp.Ptr("Write /data/review.txt"),
			Kind:       acp.Ptr(acp.ToolKindEdit),
			RawInput: map[string]any{
				"path":    "/data/review.txt",
				"content": "review me\n",
			},
		},
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: acp.PermissionOptionId("allow")},
			{Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: acp.PermissionOptionId("reject")},
		},
	})
	if err != nil {
		t.Fatalf("RequestPermission error = %v", err)
	}
	if permission.Outcome.Selected == nil {
		t.Fatalf("permission outcome = %#v, want selected", permission.Outcome)
	}
	if _, err := callbacks.WriteTextFile(context.Background(), acp.WriteTextFileRequest{
		Path:    "/data/review.txt",
		Content: "review me\n",
	}); err != nil {
		t.Fatalf("WriteTextFile error = %v", err)
	}
	if got := approval.createdCount(); got != 1 {
		t.Fatalf("approval create count = %d, want 1", got)
	}
	assertSingleApprovalWithStartEnd(t, collector.result().Events, "write-1", "write", "approval-write")
}

func TestRequestPermissionGrantDedupesCreateTerminalApproval(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	client := newTestBridgeClient(t, root)
	approval := &fakeACPToolApproval{
		decision: toolapproval.Request{
			ID:      "approval-exec",
			ShortID: 15,
			Status:  toolapproval.StatusApproved,
		},
	}
	callbacks := newClientCallbacks(
		context.Background(),
		client,
		"/data",
		"/data",
		time.Second,
		nil,
		nil,
		nil,
		approval,
		nil,
		ToolSessionContext{
			BotID:             "bot-1",
			SessionID:         "session-1",
			RunID:             "run-1",
			ChannelIdentityID: "channel-1",
		},
		acpprofile.DefaultToolQuirks(),
	)
	collector := newEventCollector()
	callbacks.setPromptState(collector, nil, callbacks.baseSession)

	if _, err := callbacks.RequestPermission(context.Background(), acp.RequestPermissionRequest{
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId("exec-1"),
			Title:      acp.Ptr("Shell"),
			Kind:       acp.Ptr(acp.ToolKindExecute),
			RawInput:   map[string]any{"command": "pwd"},
		},
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: acp.PermissionOptionId("allow")},
			{Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: acp.PermissionOptionId("reject")},
		},
	}); err != nil {
		t.Fatalf("RequestPermission error = %v", err)
	}
	term, err := callbacks.CreateTerminal(context.Background(), acp.CreateTerminalRequest{Command: "pwd"})
	if err != nil {
		t.Fatalf("CreateTerminal error = %v", err)
	}
	if _, err := callbacks.WaitForTerminalExit(context.Background(), acp.WaitForTerminalExitRequest{TerminalId: term.TerminalId}); err != nil {
		t.Fatalf("WaitForTerminalExit error = %v", err)
	}
	if got := approval.createdCount(); got != 1 {
		t.Fatalf("approval create count = %d, want 1", got)
	}
	assertSingleApprovalWithStartEnd(t, collector.result().Events, "exec-1", "exec", "approval-exec")
}

func TestRequestPermissionGrantDedupesTerminalWithCwdAndArgs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	client := newTestBridgeClient(t, root)
	approval := &fakeACPToolApproval{
		decision: toolapproval.Request{
			ID:      "approval-exec-cwd",
			ShortID: 16,
			Status:  toolapproval.StatusApproved,
		},
	}
	callbacks := newClientCallbacks(
		context.Background(),
		client,
		"/data",
		"/data",
		time.Second,
		nil,
		nil,
		nil,
		approval,
		nil,
		ToolSessionContext{
			BotID:             "bot-1",
			SessionID:         "session-1",
			RunID:             "run-1",
			ChannelIdentityID: "channel-1",
		},
		acpprofile.DefaultToolQuirks(),
	)
	collector := newEventCollector()
	callbacks.setPromptState(collector, nil, callbacks.baseSession)

	// The permission request carries the raw command with loose spacing and no
	// cwd; the terminal create rebuilds it from Command+Args and adds a cwd.
	// The one-shot grant must still match.
	if _, err := callbacks.RequestPermission(context.Background(), acp.RequestPermissionRequest{
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId("exec-cwd-1"),
			Title:      acp.Ptr("Shell"),
			Kind:       acp.Ptr(acp.ToolKindExecute),
			RawInput:   map[string]any{"command": "printf  grant-ok"},
		},
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: acp.PermissionOptionId("allow")},
			{Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: acp.PermissionOptionId("reject")},
		},
	}); err != nil {
		t.Fatalf("RequestPermission error = %v", err)
	}
	term, err := callbacks.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
		Command: "printf",
		Args:    []string{"grant-ok"},
		Cwd:     acp.Ptr("/data"),
	})
	if err != nil {
		t.Fatalf("CreateTerminal error = %v", err)
	}
	if _, err := callbacks.WaitForTerminalExit(context.Background(), acp.WaitForTerminalExitRequest{TerminalId: term.TerminalId}); err != nil {
		t.Fatalf("WaitForTerminalExit error = %v", err)
	}
	if got := approval.createdCount(); got != 1 {
		t.Fatalf("approval create count = %d, want 1 (grant should dedupe despite cwd/spacing)", got)
	}
	assertSingleApprovalWithStartEnd(t, collector.result().Events, "exec-cwd-1", "exec", "approval-exec-cwd")
}

func TestPermissionNativeToolMapsClaudeCodeShapes(t *testing.T) {
	t.Parallel()

	// Shapes mirror what @agentclientprotocol/claude-agent-acp sends via
	// toolInfoFromToolUse: Bash -> execute + {command}, Write/Edit -> edit +
	// {file_path, ...}.
	cases := []struct {
		name     string
		request  acp.RequestPermissionRequest
		wantTool string
		wantKey  string
		wantVal  string
	}{
		{
			name: "bash",
			request: acp.RequestPermissionRequest{
				ToolCall: acp.ToolCallUpdate{
					ToolCallId: acp.ToolCallId("toolu_bash"),
					Title:      acp.Ptr("npm test"),
					Kind:       acp.Ptr(acp.ToolKindExecute),
					RawInput:   map[string]any{"command": "npm test", "description": "Run tests"},
				},
			},
			wantTool: "exec",
			wantKey:  "command",
			wantVal:  "npm test",
		},
		{
			name: "write",
			request: acp.RequestPermissionRequest{
				ToolCall: acp.ToolCallUpdate{
					ToolCallId: acp.ToolCallId("toolu_write"),
					Title:      acp.Ptr("Write foo.txt"),
					Kind:       acp.Ptr(acp.ToolKindEdit),
					RawInput:   map[string]any{"file_path": "/data/foo.txt", "content": "hello"},
					Locations:  []acp.ToolCallLocation{{Path: "/data/foo.txt"}},
				},
			},
			wantTool: "write",
			wantKey:  "path",
			wantVal:  "/data/foo.txt",
		},
		{
			name: "edit",
			request: acp.RequestPermissionRequest{
				ToolCall: acp.ToolCallUpdate{
					ToolCallId: acp.ToolCallId("toolu_edit"),
					Title:      acp.Ptr("Edit foo.txt"),
					Kind:       acp.Ptr(acp.ToolKindEdit),
					RawInput: map[string]any{
						"file_path":  "/data/foo.txt",
						"old_string": "hello",
						"new_string": "world",
					},
					Locations: []acp.ToolCallLocation{{Path: "/data/foo.txt"}},
				},
			},
			wantTool: "edit",
			wantKey:  "path",
			wantVal:  "/data/foo.txt",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state := &acpToolState{id: strings.TrimSpace(string(tc.request.ToolCall.ToolCallId))}
			mergePermissionToolUpdate(state, tc.request.ToolCall)
			toolCallID, toolName, input, ok := permissionNativeToolState(state, acpprofile.DefaultToolQuirks())
			if !ok {
				t.Fatalf("permissionNativeToolState() failed to map %s request", tc.name)
			}
			if toolCallID != strings.TrimSpace(string(tc.request.ToolCall.ToolCallId)) {
				t.Fatalf("toolCallID = %q, want %q", toolCallID, tc.request.ToolCall.ToolCallId)
			}
			if toolName != tc.wantTool {
				t.Fatalf("toolName = %q, want %q", toolName, tc.wantTool)
			}
			if got := stringFromAny(input[tc.wantKey]); got != tc.wantVal {
				t.Fatalf("input[%s] = %q, want %q (input=%#v)", tc.wantKey, got, tc.wantVal, input)
			}
		})
	}
}

func TestPinSessionModeSkipsWhenNotNeeded(t *testing.T) {
	t.Parallel()

	modes := &acp.SessionModeState{
		CurrentModeId: acp.SessionModeId("default"),
		AvailableModes: []acp.SessionMode{
			{Id: acp.SessionModeId("default"), Name: "Always Ask"},
			{Id: acp.SessionModeId("acceptEdits"), Name: "Accept Edits"},
		},
	}
	// nil conn proves these paths never issue a set_mode call.
	if err := pinSessionMode(context.Background(), nil, acp.SessionId("s1"), modes, "", nil, "claude-code"); err != nil {
		t.Fatalf("empty desired mode: %v", err)
	}
	if err := pinSessionMode(context.Background(), nil, acp.SessionId("s1"), modes, "default", nil, "claude-code"); err != nil {
		t.Fatalf("already in desired mode: %v", err)
	}
}

func TestPinSessionModeFailsWhenRequiredModeCannotBeVerified(t *testing.T) {
	t.Parallel()

	if err := pinSessionMode(context.Background(), nil, acp.SessionId("s1"), nil, "default", nil, "claude-code"); err == nil {
		t.Fatal("nil modes returned nil error")
	}
	modes := &acp.SessionModeState{
		CurrentModeId: acp.SessionModeId("acceptEdits"),
		AvailableModes: []acp.SessionMode{
			{Id: acp.SessionModeId("acceptEdits"), Name: "Accept Edits"},
		},
	}
	if err := pinSessionMode(context.Background(), nil, acp.SessionId("s1"), modes, "default", nil, "claude-code"); err == nil {
		t.Fatal("unavailable pinned mode returned nil error")
	}
}

func TestApprovalGrantsAreClearedBetweenPrompts(t *testing.T) {
	t.Parallel()

	callbacks := &clientCallbacks{}
	input := writeToolInput("/data/review.txt", "review me\n")
	callbacks.rememberApprovalGrant("write-1", "write", input)
	if got, ok := callbacks.consumeApprovalGrant("write", input); !ok || got != "write-1" {
		t.Fatalf("consume grant = %q, %v; want write-1, true", got, ok)
	}

	callbacks.rememberApprovalGrant("write-1", "write", input)
	callbacks.setPromptState(nil, nil, ToolSessionContext{})
	if got, ok := callbacks.consumeApprovalGrant("write", input); ok || got != "" {
		t.Fatalf("stale grant survived prompt reset: %q, %v", got, ok)
	}
}

func TestWriteApprovalGrantKeyIncludesFullContentHashWhenPreviewTruncated(t *testing.T) {
	t.Parallel()

	prefix := strings.Repeat("a", maxWriteToolContentPreview)
	first := writeToolInput("/data/large.txt", prefix+"b")
	second := writeToolInput("/data/large.txt", prefix+"c")
	if first["content"] != second["content"] || first["content_bytes"] != second["content_bytes"] {
		t.Fatalf("test setup expected same preview and byte count: %#v %#v", first, second)
	}
	if first["content_sha256"] == "" || first["content_sha256"] == second["content_sha256"] {
		t.Fatalf("content hashes should distinguish truncated writes: %#v %#v", first["content_sha256"], second["content_sha256"])
	}
	if approvalGrantKey("write", first) == approvalGrantKey("write", second) {
		t.Fatal("grant keys for distinct truncated writes should differ")
	}
}

func assertSingleApprovalWithStartEnd(t *testing.T, events []event.StreamEvent, toolCallID, toolName, approvalID string) {
	t.Helper()
	var pendingApprovals, approvedApprovals, starts, ends int
	for _, ev := range events {
		if ev.ToolCallID != toolCallID {
			continue
		}
		switch ev.Type {
		case event.ToolApprovalRequest:
			if ev.ApprovalID != approvalID {
				t.Fatalf("approval event = %#v, want approval id %q", ev, approvalID)
			}
			switch ev.Status {
			case toolapproval.StatusPending:
				pendingApprovals++
			case toolapproval.StatusApproved:
				approvedApprovals++
			default:
				t.Fatalf("approval event = %#v, want pending or approved", ev)
			}
		case event.ToolCallStart:
			starts++
			if ev.ToolName != toolName {
				t.Fatalf("start event = %#v, want tool %q", ev, toolName)
			}
		case event.ToolCallEnd:
			ends++
			if ev.ToolName != toolName || ev.Error != "" {
				t.Fatalf("end event = %#v, want successful %q", ev, toolName)
			}
		}
	}
	if pendingApprovals != 1 || approvedApprovals != 1 || starts != 1 || ends != 1 {
		t.Fatalf("events for %s pending/approved/start/end = %d/%d/%d/%d, events=%#v", toolCallID, pendingApprovals, approvedApprovals, starts, ends, events)
	}
}

func newTestBridgeClient(t *testing.T, root string) *bridge.Client {
	t.Helper()
	listener := bufconn.Listen(16 * 1024 * 1024)
	server := grpc.NewServer(
		grpc.MaxRecvMsgSize(16*1024*1024),
		grpc.MaxSendMsgSize(16*1024*1024),
	)
	pb.RegisterContainerServiceServer(server, bridgesvc.New(bridgesvc.Options{
		DefaultWorkDir:    root,
		WorkspaceRoot:     root,
		DataMount:         config.DefaultDataMount,
		AllowHostAbsolute: true,
	}))
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	conn, err := grpc.NewClient("passthrough:///acpclient-test",
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

type startupCancelBridgeServer struct {
	*bridgesvc.Server

	mu               sync.Mutex
	execs            int
	processStarted   chan struct{}
	processCancelled chan struct{}
}

func (s *startupCancelBridgeServer) Exec(stream grpc.BidiStreamingServer[pb.ExecInput, pb.ExecOutput]) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	s.mu.Lock()
	s.execs++
	execNumber := s.execs
	s.mu.Unlock()
	// RuntimeLease hardens its UUID directory before the existing command
	// availability probe, so both setup commands complete before the long-lived
	// ACP process starts.
	if execNumber <= 2 {
		return stream.Send(&pb.ExecOutput{Stream: pb.ExecOutput_EXIT, ExitCode: 0})
	}
	close(s.processStarted)
	<-stream.Context().Done()
	close(s.processCancelled)
	return stream.Context().Err()
}

func newStartupCancelBridgeClient(t *testing.T, testServer *startupCancelBridgeServer) *bridge.Client {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	pb.RegisterContainerServiceServer(server, testServer)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	conn, err := grpc.NewClient("passthrough:///acpclient-startup-cancel-test",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return bridge.NewClientFromConn(conn)
}

func writeFakeAgentScript(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-acp-agent.sh")
	script := fmt.Sprintf("#!/bin/sh\nMEMOH_ACP_FAKE_AGENT=1 exec %s -test.run '^TestFakeACPAgentHelper$' --\n", escapeShellArg(os.Args[0]))
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil { //nolint:gosec // test helper must be executable.
		t.Fatal(err)
	}
	return path
}

func TestFakeACPAgentHelper(_ *testing.T) {
	if os.Getenv("MEMOH_ACP_FAKE_AGENT") != "1" {
		return
	}
	if err := captureFakeAgentEnv(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	agent := &fakeACPAgent{}
	conn := acp.NewAgentSideConnection(agent, os.Stdout, os.Stdin)
	agent.conn = conn
	<-conn.Done()
	os.Exit(0)
}

func captureFakeAgentEnv() error {
	path := os.Getenv("MEMOH_ACP_FAKE_AGENT_CAPTURE_ENV_FILE")
	if path == "" {
		return nil
	}
	captured := map[string]string{}
	for _, key := range []string{
		"GOOGLE_API_KEY",
		"GEMINI_API_KEY",
		"OPENROUTER_API_KEY",
		"OPENAI_API_KEY",
	} {
		if value, ok := os.LookupEnv(key); ok {
			captured[key] = value
		}
	}
	raw, err := json.Marshal(captured)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600) //nolint:gosec // test helper writes to env-provided temp path.
}

type fakeACPAgent struct {
	conn                   *acp.AgentSideConnection
	cwd                    string
	modelID                string
	reasoningEffort        string
	reasoningModelSpecific bool
}

func (*fakeACPAgent) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

func (*fakeACPAgent) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, nil
}

func (*fakeACPAgent) Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error) {
	capabilities := acp.AgentCapabilities{LoadSession: false}
	if os.Getenv("MEMOH_ACP_FAKE_AGENT_RESUME") == "1" {
		capabilities.SessionCapabilities.Resume = &acp.SessionResumeCapabilities{}
	}
	if os.Getenv("MEMOH_ACP_FAKE_AGENT_LOAD") == "1" {
		capabilities.LoadSession = true
	}
	if os.Getenv("MEMOH_ACP_FAKE_AGENT_MCP_HTTP") == "1" {
		capabilities.McpCapabilities.Http = true
	}
	if os.Getenv("MEMOH_ACP_FAKE_AGENT_MCP_ACP") == "1" {
		capabilities.McpCapabilities.Acp = true
	}
	if os.Getenv("MEMOH_ACP_FAKE_AGENT_CLOSE") == "1" {
		capabilities.SessionCapabilities.Close = &acp.SessionCloseCapabilities{}
	}
	protocolVersion := acp.ProtocolVersion(acp.ProtocolVersionNumber)
	if os.Getenv("MEMOH_ACP_FAKE_AGENT_PROTOCOL_VERSION") == "2" {
		protocolVersion = acp.ProtocolVersion(2)
	}
	return acp.InitializeResponse{
		ProtocolVersion:   protocolVersion,
		AgentCapabilities: capabilities,
	}, nil
}

func (*fakeACPAgent) Cancel(context.Context, acp.CancelNotification) error {
	if path := os.Getenv("MEMOH_ACP_PROMPT_CANCELLED_FILE"); path != "" {
		_ = os.WriteFile(path, []byte("cancelled"), 0o600) //nolint:gosec // test helper writes to env-provided temp path.
	}
	return nil
}

func (*fakeACPAgent) CloseSession(_ context.Context, req acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	if path := os.Getenv("MEMOH_ACP_FAKE_AGENT_CAPTURE_CLOSE_FILE"); path != "" {
		if err := os.WriteFile(path, []byte(req.SessionId), 0o600); err != nil { //nolint:gosec // test helper writes to env-provided temp path.
			return acp.CloseSessionResponse{}, err
		}
	}
	return acp.CloseSessionResponse{}, nil
}

func (*fakeACPAgent) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, nil
}

func (a *fakeACPAgent) NewSession(_ context.Context, p acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	a.prepareFakeSession(p.Cwd)
	if err := captureFakeSessionLifecycle("new", "fake-session", p.Meta); err != nil {
		return acp.NewSessionResponse{}, err
	}
	if capturePath := os.Getenv("MEMOH_ACP_FAKE_AGENT_CAPTURE_MCP_FILE"); capturePath != "" {
		raw, err := json.Marshal(p.McpServers)
		if err != nil {
			return acp.NewSessionResponse{}, err
		}
		if err := os.WriteFile(capturePath, raw, 0o600); err != nil { //nolint:gosec // test helper writes to env-provided temp path.
			return acp.NewSessionResponse{}, err
		}
	}
	sessionID := acp.SessionId("fake-session")
	if os.Getenv("MEMOH_ACP_FAKE_AGENT_EMPTY_SESSION_ID") == "1" {
		sessionID = ""
	}
	resp := acp.NewSessionResponse{SessionId: sessionID}
	resp.ConfigOptions = a.configOptions()
	return resp, nil
}

func (a *fakeACPAgent) Prompt(ctx context.Context, p acp.PromptRequest) (acp.PromptResponse, error) {
	if auth := os.Getenv("MEMOH_ACP_FAKE_AGENT_REFRESH_AUTH"); auth != "" {
		home := strings.TrimSpace(os.Getenv("CODEX_HOME"))
		if home == "" {
			return acp.PromptResponse{}, errors.New("fake Codex agent missing CODEX_HOME")
		}
		if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(auth), 0o600); err != nil { //nolint:gosec // test helper writes only to the lease-owned home.
			return acp.PromptResponse{}, err
		}
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	}
	if os.Getenv("MEMOH_ACP_FAKE_AGENT_HANG_PROMPT") == "1" {
		if path := os.Getenv("MEMOH_ACP_PROMPT_STARTED_FILE"); path != "" {
			_ = os.WriteFile(path, []byte("started"), 0o600) //nolint:gosec // test helper writes to env-provided temp path.
		}
		<-ctx.Done()
		if path := os.Getenv("MEMOH_ACP_PROMPT_CANCELLED_FILE"); path != "" {
			_ = os.WriteFile(path, []byte("cancelled"), 0o600) //nolint:gosec // test helper writes to env-provided temp path.
		}
		return acp.PromptResponse{}, ctx.Err()
	}
	if os.Getenv("MEMOH_ACP_FAKE_AGENT_RELEASE_TERMINAL_WITHOUT_WAIT") == "1" {
		return a.promptReleaseTerminalAfterOutput(ctx, p)
	}

	outputPath := filepath.Join(a.cwd, "output.txt")
	permission, err := a.conn.RequestPermission(ctx, acp.RequestPermissionRequest{
		SessionId: p.SessionId,
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId("write-output"),
			Title:      acp.Ptr("Write output file"),
			Kind:       acp.Ptr(acp.ToolKindEdit),
			Status:     acp.Ptr(acp.ToolCallStatusPending),
			Locations:  []acp.ToolCallLocation{{Path: outputPath}},
			RawInput:   map[string]any{"path": outputPath, "cwd": a.cwd},
		},
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: acp.PermissionOptionId("allow")},
			{Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: acp.PermissionOptionId("reject")},
		},
	})
	if err != nil {
		return acp.PromptResponse{}, err
	}
	if permission.Outcome.Selected == nil || permission.Outcome.Selected.OptionId != acp.PermissionOptionId("allow") {
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	}

	read, err := a.conn.ReadTextFile(ctx, acp.ReadTextFileRequest{
		SessionId: p.SessionId,
		Path:      filepath.Join(a.cwd, "input.txt"),
	})
	if err != nil {
		return acp.PromptResponse{}, err
	}
	if _, err := a.conn.WriteTextFile(ctx, acp.WriteTextFileRequest{
		SessionId: p.SessionId,
		Path:      outputPath,
		Content:   "written by fake agent\n",
	}); err != nil {
		return acp.PromptResponse{}, err
	}

	term, err := a.conn.CreateTerminal(ctx, acp.CreateTerminalRequest{
		SessionId: p.SessionId,
		Command:   "printf",
		Args:      []string{"terminal-ok"},
		Cwd:       &a.cwd,
	})
	if err != nil {
		return acp.PromptResponse{}, err
	}
	if _, err := a.conn.WaitForTerminalExit(ctx, acp.WaitForTerminalExitRequest{SessionId: p.SessionId, TerminalId: term.TerminalId}); err != nil {
		return acp.PromptResponse{}, err
	}
	termOut, err := a.conn.TerminalOutput(ctx, acp.TerminalOutputRequest{SessionId: p.SessionId, TerminalId: term.TerminalId})
	if err != nil {
		return acp.PromptResponse{}, err
	}
	_, _ = a.conn.ReleaseTerminal(ctx, acp.ReleaseTerminalRequest{SessionId: p.SessionId, TerminalId: term.TerminalId})
	_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{
		SessionId: p.SessionId,
		Update: acp.UpdateAgentMessageText(
			"read: " + strings.TrimSpace(read.Content) + " term: " + strings.TrimSpace(termOut.Output),
		),
	})
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func (a *fakeACPAgent) promptReleaseTerminalAfterOutput(ctx context.Context, p acp.PromptRequest) (acp.PromptResponse, error) {
	term, err := a.conn.CreateTerminal(ctx, acp.CreateTerminalRequest{
		SessionId: p.SessionId,
		Command:   "printf",
		Args:      []string{"terminal-ok"},
		Cwd:       &a.cwd,
	})
	if err != nil {
		return acp.PromptResponse{}, err
	}

	var termOut acp.TerminalOutputResponse
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		termOut, err = a.conn.TerminalOutput(ctx, acp.TerminalOutputRequest{SessionId: p.SessionId, TerminalId: term.TerminalId})
		if err != nil {
			return acp.PromptResponse{}, err
		}
		if strings.Contains(termOut.Output, "terminal-ok") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(termOut.Output, "terminal-ok") {
		return acp.PromptResponse{}, fmt.Errorf("terminal output = %q, want terminal-ok", termOut.Output)
	}
	if _, err := a.conn.ReleaseTerminal(ctx, acp.ReleaseTerminalRequest{SessionId: p.SessionId, TerminalId: term.TerminalId}); err != nil {
		return acp.PromptResponse{}, err
	}
	_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{
		SessionId: p.SessionId,
		Update:    acp.UpdateAgentMessageText("term: " + strings.TrimSpace(termOut.Output)),
	})
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("file %s was not created within %s", path, timeout)
}

func (a *fakeACPAgent) ResumeSession(_ context.Context, p acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	a.prepareFakeSession(p.Cwd)
	if err := captureFakeSessionLifecycle("resume", string(p.SessionId), p.Meta); err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	return acp.ResumeSessionResponse{ConfigOptions: a.configOptions()}, nil
}

func (a *fakeACPAgent) LoadSession(ctx context.Context, p acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	a.prepareFakeSession(p.Cwd)
	if err := captureFakeSessionLifecycle("load", string(p.SessionId), p.Meta); err != nil {
		return acp.LoadSessionResponse{}, err
	}
	if os.Getenv("MEMOH_ACP_FAKE_AGENT_REPLAY_HISTORY") == "1" {
		if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{
			SessionId: p.SessionId,
			Update:    acp.UpdateAgentMessageText("historical replay"),
		}); err != nil {
			return acp.LoadSessionResponse{}, err
		}
	}
	return acp.LoadSessionResponse{ConfigOptions: a.configOptions()}, nil
}

func (a *fakeACPAgent) prepareFakeSession(cwd string) {
	a.cwd = cwd
	if os.Getenv("MEMOH_ACP_FAKE_AGENT_MODELS") == "1" {
		a.modelID = "gpt-5.1-codex"
	}
	if os.Getenv("MEMOH_ACP_FAKE_AGENT_REASONING") != "" {
		a.reasoningEffort = "medium"
	}
}

func captureFakeSessionLifecycle(method, sessionID string, meta map[string]any) error {
	path := os.Getenv("MEMOH_ACP_FAKE_AGENT_CAPTURE_SESSION_LIFECYCLE_FILE")
	if path == "" {
		return nil
	}
	raw, err := json.Marshal(map[string]any{"method": method, "session_id": sessionID, "meta": meta})
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600) //nolint:gosec // test helper writes to an env-provided temporary capture.
}

func (a *fakeACPAgent) SetSessionConfigOption(ctx context.Context, p acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	if p.ValueId == nil || p.ValueId.SessionId != acp.SessionId("fake-session") {
		return acp.SetSessionConfigOptionResponse{}, errors.New("unexpected config request")
	}
	if os.Getenv("MEMOH_ACP_FAKE_AGENT_CONFIG_OMIT_OPTIONS") == "1" {
		return acp.SetSessionConfigOptionResponse{}, nil
	}
	if os.Getenv("MEMOH_ACP_FAKE_AGENT_CONFIG_NOOP") == "1" {
		return acp.SetSessionConfigOptionResponse{ConfigOptions: a.configOptions()}, nil
	}
	value := string(p.ValueId.Value)
	switch string(p.ValueId.ConfigId) {
	case "model":
		if value != "gpt-5.1-codex" && value != "gpt-5.1-codex-high" {
			return acp.SetSessionConfigOptionResponse{}, errors.New("unsupported model")
		}
		a.modelID = value
		if os.Getenv("MEMOH_ACP_FAKE_AGENT_REASONING_MODEL_NOTIFY") == "1" {
			a.reasoningEffort = "max"
			a.reasoningModelSpecific = true
		}
	case "thinking":
		available := map[string]bool{"low": true, "medium": true, "high": true}
		if a.reasoningModelSpecific {
			available = map[string]bool{"balanced": true, "max": true}
		}
		if !available[value] {
			return acp.SetSessionConfigOptionResponse{}, errors.New("unsupported reasoning effort")
		}
		a.reasoningEffort = value
	default:
		return acp.SetSessionConfigOptionResponse{}, errors.New("unexpected config id")
	}
	if os.Getenv("MEMOH_ACP_FAKE_AGENT_CONFIG_ERROR_AFTER_APPLY") == "1" {
		return acp.SetSessionConfigOptionResponse{}, errors.New("forced config failure after apply")
	}
	options := a.configOptions()
	if os.Getenv("MEMOH_ACP_FAKE_AGENT_REASONING_NOTIFY") == "1" {
		_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{
			SessionId: p.ValueId.SessionId,
			Update: acp.SessionUpdate{ConfigOptionUpdate: &acp.SessionConfigOptionUpdate{
				SessionUpdate: "config_option_update",
				ConfigOptions: options,
			}},
		})
	}
	return acp.SetSessionConfigOptionResponse{ConfigOptions: options}, nil
}

func (a *fakeACPAgent) reasoningConfigOptions() []acp.SessionConfigOption {
	category := acp.SessionConfigOptionCategoryThoughtLevel
	if a.reasoningModelSpecific {
		return []acp.SessionConfigOption{
			testSelectOption("thinking", &category, a.reasoningEffort, "balanced", "max"),
		}
	}
	return []acp.SessionConfigOption{
		testSelectOption("thinking", &category, a.reasoningEffort, "low", "medium", "high"),
	}
}

func (a *fakeACPAgent) configOptions() []acp.SessionConfigOption {
	var options []acp.SessionConfigOption
	if os.Getenv("MEMOH_ACP_FAKE_AGENT_MODELS") == "1" {
		category := acp.SessionConfigOptionCategoryModel
		description := "Highest reasoning"
		values := acp.SessionConfigSelectOptionsUngrouped{
			{Value: acp.SessionConfigValueId("gpt-5.1-codex"), Name: "GPT-5.1 Codex"},
			{Value: acp.SessionConfigValueId("gpt-5.1-codex-high"), Name: "GPT-5.1 Codex High", Description: &description},
		}
		options = append(options, acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
			Id:           acp.SessionConfigId("model"),
			Name:         "Model",
			Type:         "select",
			Category:     &category,
			CurrentValue: acp.SessionConfigValueId(a.modelID),
			Options:      acp.SessionConfigSelectOptions{Ungrouped: &values},
		}})
	}
	if os.Getenv("MEMOH_ACP_FAKE_AGENT_REASONING") != "" {
		options = append(options, a.reasoningConfigOptions()...)
	}
	return options
}

func (*fakeACPAgent) SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}

type fakeACPToolApproval struct {
	mu            sync.Mutex
	created       toolapproval.CreatePendingInput
	createCount   int
	decision      toolapproval.Request
	evaluation    toolapproval.Evaluation
	waiters       int
	evaluateFence runtimefence.Fence
}

func (f *fakeACPToolApproval) EvaluatePolicy(ctx context.Context, _ toolapproval.CreatePendingInput) (toolapproval.Evaluation, error) {
	f.mu.Lock()
	f.evaluateFence, _ = runtimefence.FromContext(ctx)
	evaluation := f.evaluation
	f.mu.Unlock()
	if strings.TrimSpace(evaluation.Decision) != "" {
		return evaluation, nil
	}
	return toolapproval.Evaluation{Decision: toolapproval.DecisionNeedsApproval}, nil
}

func (f *fakeACPToolApproval) CreatePending(_ context.Context, input toolapproval.CreatePendingInput) (toolapproval.Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = input
	f.createCount++
	req := toolapproval.Request{
		ID:                "approval-1",
		BotID:             input.BotID,
		SessionID:         input.SessionID,
		ChannelIdentityID: input.ChannelIdentityID,
		ToolCallID:        input.ToolCallID,
		ToolName:          input.ToolName,
		ToolInput:         copyInputMap(input.ToolInput),
		ShortID:           1,
		Status:            toolapproval.StatusPending,
		SourcePlatform:    input.SourcePlatform,
		ConversationType:  input.ConversationType,
	}
	if strings.TrimSpace(f.decision.ID) != "" {
		req.ID = f.decision.ID
	}
	if f.decision.ShortID != 0 {
		req.ShortID = f.decision.ShortID
	}
	return req, nil
}

func (*fakeACPToolApproval) Reject(context.Context, string, string, string) (toolapproval.Request, error) {
	return toolapproval.Request{Status: toolapproval.StatusRejected}, nil
}

func (f *fakeACPToolApproval) Get(_ context.Context, approvalID string) (toolapproval.Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	decision := f.decision
	if strings.TrimSpace(decision.ID) == "" {
		decision.ID = approvalID
	}
	return decision, nil
}

func (f *fakeACPToolApproval) WaitForDecision(_ context.Context, approvalID string) (toolapproval.Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	decision := f.decision
	if strings.TrimSpace(decision.ID) == "" {
		decision.ID = approvalID
	}
	if strings.TrimSpace(decision.Status) == "" {
		decision.Status = toolapproval.StatusApproved
	}
	return decision, nil
}

func (f *fakeACPToolApproval) RegisterWaiter(string) func() {
	f.mu.Lock()
	f.waiters++
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		f.waiters--
		f.mu.Unlock()
	}
}

func (f *fakeACPToolApproval) createdInput() toolapproval.CreatePendingInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.created
}

func (f *fakeACPToolApproval) createdCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createCount
}

func copyInputMap(input any) map[string]any {
	out := map[string]any{}
	if m, ok := input.(map[string]any); ok {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}
