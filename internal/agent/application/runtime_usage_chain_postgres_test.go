package application

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	acpagent "github.com/felinics/memoh/internal/agent/runtime/acp"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/acp/client"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/command"
	"github.com/felinics/memoh/internal/config"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/runtimefence"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspace/bridgesvc"
)

// usageChainTurn is what the scripted ACP agent sends for one prompt.
type usageChainTurn struct {
	Updates []acp.SessionUsageUpdate `json:"updates"`
	Usage   acp.Usage                `json:"usage"`
}

// TestUsageChainACPAgentHelper is the scripted ACP agent process; the test
// binary re-executes itself into it.
func TestUsageChainACPAgentHelper(_ *testing.T) {
	if os.Getenv("MEMOH_USAGE_CHAIN_ACP_AGENT") != "1" {
		return
	}
	var script []usageChainTurn
	if err := json.Unmarshal([]byte(os.Getenv("MEMOH_USAGE_CHAIN_SCRIPT")), &script); err != nil {
		os.Exit(2)
	}
	agent := &usageChainAgent{script: script}
	agent.conn = acp.NewAgentSideConnection(agent, os.Stdout, os.Stdin)
	<-agent.conn.Done()
	os.Exit(0)
}

type usageChainAgent struct {
	conn    *acp.AgentSideConnection
	script  []usageChainTurn
	prompts int
}

func (*usageChainAgent) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

func (*usageChainAgent) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, nil
}

func (*usageChainAgent) Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error) {
	return acp.InitializeResponse{ProtocolVersion: acp.ProtocolVersion(acp.ProtocolVersionNumber)}, nil
}

func (*usageChainAgent) Cancel(context.Context, acp.CancelNotification) error { return nil }

func (*usageChainAgent) CloseSession(context.Context, acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, nil
}

func (*usageChainAgent) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, nil
}

func (*usageChainAgent) NewSession(context.Context, acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	return acp.NewSessionResponse{SessionId: "chain-session"}, nil
}

func (*usageChainAgent) ResumeSession(context.Context, acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, nil
}

func (*usageChainAgent) LoadSession(context.Context, acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	return acp.LoadSessionResponse{}, nil
}

func (*usageChainAgent) SetSessionConfigOption(context.Context, acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, nil
}

func (*usageChainAgent) SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}

func (a *usageChainAgent) Prompt(ctx context.Context, p acp.PromptRequest) (acp.PromptResponse, error) {
	turn := a.script[a.prompts]
	a.prompts++
	if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: p.SessionId, Update: acp.UpdateAgentMessageText(fmt.Sprintf("reply %d", a.prompts))}); err != nil {
		return acp.PromptResponse{}, err
	}
	for i := range turn.Updates {
		if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: p.SessionId, Update: acp.SessionUpdate{UsageUpdate: &turn.Updates[i]}}); err != nil {
			return acp.PromptResponse{}, err
		}
	}
	usage := turn.Usage
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn, Usage: &usage}, nil
}

type usageChainWorkspace struct {
	client *bridge.Client
	root   string
}

func (w usageChainWorkspace) MCPClient(context.Context, string) (*bridge.Client, error) {
	return w.client, nil
}

func (w usageChainWorkspace) WorkspaceInfo(context.Context, string) (bridge.WorkspaceInfo, error) {
	return bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: w.root}, nil
}

func newUsageChainBridge(t *testing.T, root string) *bridge.Client {
	t.Helper()
	listener := bufconn.Listen(16 * 1024 * 1024)
	server := grpc.NewServer()
	pb.RegisterContainerServiceServer(server, bridgesvc.New(bridgesvc.Options{
		DefaultWorkDir: root, WorkspaceRoot: root, DataMount: config.DefaultDataMount, AllowHostAbsolute: true,
	}))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	conn, err := grpc.NewClient("passthrough:///usage-chain",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return bridge.NewClientFromConn(conn)
}

// usageChainPrompter stands where the session pool stands: it hands each
// prompt to one live ACP client session.
type usageChainPrompter struct {
	session *acpclient.Session
}

func (p usageChainPrompter) Prompt(ctx context.Context, input acpagent.PromptInput) (acpclient.PromptResult, error) {
	return p.session.Prompt(ctx, input.Prompt)
}

type ownerRole struct{}

func (ownerRole) GetMemberRole(context.Context, string, string) (string, error) { return "owner", nil }

// One deterministic chain from the wire to what users read: a scripted ACP
// agent process speaks JSON-RPC over stdio to the production client; the
// production ACP driver and External Agent turn path persist each round to
// PostgreSQL; /context and /status read it back. The first turn replays the
// claude-agent-acp 0.44.0 recording of two API requests.
func TestPostgresProviderUsageACPWireToStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newExternalUsageHarness(t, ctx, session.RuntimeACPAgent)

	script := []usageChainTurn{
		{
			Updates: []acp.SessionUsageUpdate{{Used: 1511, Size: 200000}, {Used: 1530, Size: 200000}, {Used: 1571, Size: 200000}, {Used: 1585, Size: 200000}, {Used: 1585, Size: 200000}},
			Usage:   acp.Usage{InputTokens: 40, OutputTokens: 35, TotalTokens: 3115, CachedReadTokens: acp.Ptr(2500), CachedWriteTokens: acp.Ptr(540)},
		},
		{Usage: acp.Usage{InputTokens: 12, OutputTokens: 3, TotalTokens: 15}},
		{
			Updates: []acp.SessionUsageUpdate{{Used: 4200, Size: 0}},
			Usage:   acp.Usage{InputTokens: 4000, OutputTokens: 200, TotalTokens: 4200},
		},
	}
	rawScript, err := json.Marshal(script)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "project"), 0o750); err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(root, "usage-chain-agent.sh")
	launcher := fmt.Sprintf("#!/bin/sh\nMEMOH_USAGE_CHAIN_ACP_AGENT=1 MEMOH_USAGE_CHAIN_SCRIPT='%s' exec '%s' -test.run '^TestUsageChainACPAgentHelper$' --\n", rawScript, os.Args[0])
	if err := os.WriteFile(agentPath, []byte(launcher), 0o700); err != nil { //nolint:gosec // test helper must be executable.
		t.Fatal(err)
	}
	runner := acpclient.NewRunner(nil, usageChainWorkspace{client: newUsageChainBridge(t, root), root: root})
	acpSession, err := runner.StartSession(ctx, acpclient.StartRequest{
		AgentID: acpprofile.AgentACPID, BotID: h.botID, ProjectPath: "/data/project", Command: agentPath, Timeout: 10 * time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer func() { _ = acpSession.Close() }()

	service := newACPLifecycleService(t, usageChainPrompter{session: acpSession}, h.messages, &recordingContextLifecycleStore{})
	service.botPermissions = allowWorkspaceExecForBot(h.botID, "user-1")
	service.sessionService = &fakeBackgroundSessionService{getFn: func(_ context.Context, sessionID string) (session.Thread, error) {
		return session.Thread{
			ID: sessionID, BotID: h.botID, Type: session.TypeACPAgent, RuntimeType: session.RuntimeACPAgent,
			Metadata: map[string]any{"acp_agent_id": "chain-agent", "project_path": "/data/project", "runtime_owner_account_id": "user-1"},
		}, nil
	}}
	driver := acpagent.NewDriver(usageChainPrompter{session: acpSession})
	commands := command.NewHandler(slog.New(slog.DiscardHandler), ownerRole{}, nil, nil, nil, nil, nil, nil, postgresstore.NewQueriesWithPool(h.pool, h.queries), nil, nil, nil)

	read := func(text string) string {
		t.Helper()
		out, err := commands.ExecuteWithInput(ctx, command.ExecuteInput{BotID: h.botID, ChannelIdentityID: "user-1", Text: text, SessionID: h.sessionID})
		if err != nil {
			t.Fatalf("%s: %v", text, err)
		}
		return out
	}
	for i, want := range []struct {
		context, status string
		latest          string
	}{
		{context: "1% · 1.6K / 200.0K used", status: "Context: 1.6K / 200.0K"},
		{context: "Not available", status: "Context: Not available"},
		{context: "4.2K tokens used", status: "Context: 4.2K"},
	} {
		h.streamACPTurn(t, ctx, service, driver, fmt.Sprintf("turn %d", i+1))
		if out := read("/context"); !strings.Contains(out, want.context) {
			t.Fatalf("turn %d /context = %q, want %q", i+1, out, want.context)
		}
		if out := read("/status"); !strings.Contains(out, want.status) || strings.Contains(out, "3.1K") {
			t.Fatalf("turn %d /status = %q, want %q", i+1, out, want.status)
		}
	}
	assertExternalUsageAccounting(t, ctx, h, []usageTokens{
		{input: 3080, output: 35, total: 3115, noCache: 40, cacheRead: 2500, cacheWrite: 540},
		{input: 12, output: 3, total: 15, noCache: 12},
		{input: 4000, output: 200, total: 4200, noCache: 4000},
	})
}

// streamACPTurn admits one run and drives it through the External Agent
// turn path the WebSocket handler uses.
func (h externalUsageHarness) streamACPTurn(t *testing.T, ctx context.Context, service *Service, driver *acpagent.Driver, query string) {
	t.Helper()
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	admission, err := h.manager.Admit(runCtx, sessionruntime.AdmitInput{
		BotID: h.botID, SessionID: h.sessionID, InvocationID: uuid.NewString(),
		Payload: []byte(`{"kind":"message","text":"` + query + `"}`),
		Execution: sessionruntime.Execution{
			Admission: func(context.Context, sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
				return sessionruntime.RunAdmissionView{}, nil
			},
			AbortCh: make(chan struct{}, 1), Cancel: func() { cancel(context.Canceled) }, OwnershipCancel: cancel,
		},
	})
	if err != nil || !admission.Started {
		t.Fatalf("Admit() = %+v, %v", admission, err)
	}
	handle := admission.Handle
	fenced := runtimefence.WithContext(runCtx, runtimefence.Fence{BotID: handle.BotID, SessionID: handle.SessionID, Token: handle.FencingToken})
	position := admission.TurnPosition
	req := ChatRequest{
		BotID: h.botID, ChatID: h.botID, ThreadID: h.sessionID, RunID: admission.RunID, TurnID: admission.TurnID, TurnPosition: &position,
		Query: query, RawQuery: query, UserVisibleText: query, RunHandle: handle, InjectCh: make(chan turn.InjectMessage),
		SourceChannelIdentityID: "user-1",
	}
	events := make(chan WSStreamEvent, 64)
	if _, err := service.streamRuntimeWS(fenced, driver, req, events, make(chan struct{}), true); err != nil {
		t.Fatalf("streamRuntimeWS() error = %v", err)
	}
	if _, err := h.manager.FinishRun(context.WithoutCancel(ctx), handle, sessionruntime.RunStatusCompleted); err != nil {
		t.Fatalf("FinishRun() error = %v", err)
	}
}
