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
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/event"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/external/acp/profile"
	"github.com/felinics/memoh/internal/config"
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
			Backend:        bridge.WorkspaceBackendContainer,
			DefaultWorkDir: root,
			ToolsHTTPURL:   "http://memoh.test/bots/bot-1/tools",
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
	if servers[0]["type"] != "http" || !strings.HasPrefix(rawURL, "http://memoh.test/bots/bot-1/tools/") || servers[0]["name"] != memohToolsMCPServerSlug {
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
		if err != nil && !errors.Is(err, ErrSessionCloseUnconfirmed) {
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
	_, err := runner.StartSession(context.Background(), StartRequest{
		BotID:   "bot-1",
		AgentID: acpprofile.AgentACPID,
		Command: "memoh-definitely-missing-acp-command",
	}, nil)
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
	delivered := &eventLog{}
	callbacks.setPromptState(newEventCollector(), delivered, callbacks.baseSession)

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
	events := delivered.all()
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

func TestRuntimeOwnedPermissionReachesTheUserPastAPermissivePolicy(t *testing.T) {
	t.Parallel()

	approval := &fakeACPToolApproval{
		evaluation: toolapproval.Evaluation{Decision: toolapproval.DecisionBypass},
		decision:   toolapproval.Request{ID: "approval-1", Status: toolapproval.StatusApproved},
	}
	callbacks := &clientCallbacks{
		root:                "/data",
		approval:            approval,
		runtimeOwnsApproval: true,
		baseSession:         ToolSessionContext{BotID: "bot-1", SessionID: "session-1", RunID: "run-1"},
		events:              &toolEventEmitter{},
	}
	callbacks.setPromptState(newEventCollector(), nil, callbacks.baseSession)

	_, err := callbacks.RequestPermission(context.Background(), acp.RequestPermissionRequest{
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId("exec-1"),
			Kind:       acp.Ptr(acp.ToolKindExecute),
			RawInput:   map[string]any{"command": "uname -m"},
		},
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: acp.PermissionOptionId("allow")},
		},
	})
	if err != nil {
		t.Fatalf("RequestPermission error = %v", err)
	}
	if approval.createCount != 1 {
		t.Fatalf("approvals created = %d, want the user asked once", approval.createCount)
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
		approval: approval,
		baseSession: ToolSessionContext{
			BotID:     "bot-1",
			SessionID: "session-1",
			RunID:     "run-1",
		},
		events: &toolEventEmitter{},
	}
	delivered := &eventLog{}
	callbacks.setPromptState(newEventCollector(), delivered, callbacks.baseSession)

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
	for _, ev := range delivered.all() {
		if ev.Type == event.ToolApprovalRequest &&
			ev.ToolCallID == "exec-1" &&
			ev.ApprovalID == "approval-2" &&
			ev.Status == toolapproval.StatusRejected {
			sawRejected = true
		}
	}
	if !sawRejected {
		t.Fatalf("events = %#v, want rejected approval update", delivered.all())
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
	)
	delivered := &eventLog{}
	callbacks.setPromptState(newEventCollector(), delivered, callbacks.baseSession)

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
	events := delivered.all()
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
			approval: approval,
			baseSession: ToolSessionContext{
				BotID:             "bot-1",
				SessionID:         "session-1",
				RunID:             "run-1",
				ChannelIdentityID: "channel-1",
			},
			events:     &toolEventEmitter{},
			toolMapper: newACPToolEventMapper(),
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
	agent := &fakeACPAgent{}
	conn := acp.NewAgentSideConnection(agent, os.Stdout, os.Stdin)
	agent.conn = conn
	<-conn.Done()
	os.Exit(0)
}

type fakeACPAgent struct {
	conn            *acp.AgentSideConnection
	cwd             string
	modelID         string
	reasoningEffort string
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
	resp := acp.NewSessionResponse{SessionId: acp.SessionId("fake-session")}
	resp.ConfigOptions = a.configOptions()
	return resp, nil
}

func (a *fakeACPAgent) Prompt(ctx context.Context, p acp.PromptRequest) (acp.PromptResponse, error) {
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
	if raw := os.Getenv("MEMOH_ACP_FAKE_AGENT_BURST_CHUNKS"); raw != "" {
		return a.promptBurst(p, raw)
	}
	if raw := os.Getenv("MEMOH_ACP_FAKE_AGENT_USAGE"); raw != "" {
		var usage acp.Usage
		if err := json.Unmarshal([]byte(raw), &usage); err != nil {
			return acp.PromptResponse{}, err
		}
		_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: p.SessionId, Update: acp.UpdateAgentMessageText("usage reported")})
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn, Usage: &usage}, nil
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
	if err := fakeSessionRestoreError("MEMOH_ACP_FAKE_AGENT_RESUME_ERROR"); err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	return acp.ResumeSessionResponse{ConfigOptions: a.configOptions()}, nil
}

func (a *fakeACPAgent) LoadSession(ctx context.Context, p acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	a.prepareFakeSession(p.Cwd)
	if err := captureFakeSessionLifecycle("load", string(p.SessionId), p.Meta); err != nil {
		return acp.LoadSessionResponse{}, err
	}
	if err := fakeSessionRestoreError("MEMOH_ACP_FAKE_AGENT_LOAD_ERROR"); err != nil {
		return acp.LoadSessionResponse{}, err
	}
	if os.Getenv("MEMOH_ACP_FAKE_AGENT_REPLAY_HISTORY") == "1" {
		for _, update := range []acp.SessionUpdate{
			acp.UpdateUserMessageText("historical question"),
			acp.UpdateAgentMessageText("historical replay"),
			acp.StartToolCall("historical-tool", "historical tool"),
		} {
			if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: p.SessionId, Update: update}); err != nil {
				return acp.LoadSessionResponse{}, err
			}
		}
	}
	if raw := os.Getenv("MEMOH_ACP_FAKE_AGENT_REPLAY_BURST"); raw != "" {
		if err := replayBurst(p.SessionId, raw); err != nil {
			return acp.LoadSessionResponse{}, err
		}
	}
	return acp.LoadSessionResponse{ConfigOptions: a.configOptions()}, nil
}

// fakeSessionRestoreError answers a restore the way the env names: with a
// JSON-RPC error, or by exiting so the connection breaks.
func fakeSessionRestoreError(env string) error {
	switch os.Getenv(env) {
	case "":
		return nil
	case "exit":
		os.Exit(1)
	}
	code := map[string]int{"unsupported": -32601, "missing": -32002, "invalid": -32602, "internal": -32603, "auth": -32000}[os.Getenv(env)]
	return &acp.RequestError{Code: code, Message: "injected restore failure"}
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

func (a *fakeACPAgent) SetSessionConfigOption(_ context.Context, p acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	if p.ValueId == nil || p.ValueId.SessionId != acp.SessionId("fake-session") {
		return acp.SetSessionConfigOptionResponse{}, errors.New("unexpected config request")
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
	case "thinking":
		available := map[string]bool{"low": true, "medium": true, "high": true}
		if !available[value] {
			return acp.SetSessionConfigOptionResponse{}, errors.New("unsupported reasoning effort")
		}
		a.reasoningEffort = value
	default:
		return acp.SetSessionConfigOptionResponse{}, errors.New("unexpected config id")
	}
	options := a.configOptions()
	return acp.SetSessionConfigOptionResponse{ConfigOptions: options}, nil
}

func (a *fakeACPAgent) reasoningConfigOptions() []acp.SessionConfigOption {
	category := acp.SessionConfigOptionCategoryThoughtLevel
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

func (*fakeACPToolApproval) Cancel(context.Context, string, string) (toolapproval.Request, error) {
	return toolapproval.Request{Status: toolapproval.StatusCancelled}, nil
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
	// Without session/close the agent may still hold the session until its
	// process ends, and the caller must know that.
	if err := sess.Close(); !errors.Is(err, ErrSessionCloseUnconfirmed) {
		t.Fatalf("Close() error = %v, want ErrSessionCloseUnconfirmed", err)
	}
	if err := sess.ForceClose(); !errors.Is(err, ErrSessionCloseUnconfirmed) {
		t.Fatalf("repeated close error = %v, want ErrSessionCloseUnconfirmed", err)
	}
	if _, err := os.Stat(capturePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session/close was called without the capability: %v", err)
	}
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
