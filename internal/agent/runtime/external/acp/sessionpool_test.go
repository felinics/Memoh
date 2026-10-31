package acp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/agentstate"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/acp/client"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/external/acp/profile"
	"github.com/felinics/memoh/internal/agent/runtime/external/agentproc"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/runtimefence"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspace/bridgesvc"
)

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
	workspace := sessionPoolWorkspace{
		client: newSessionPoolBridgeClient(t, root),
		info: bridge.WorkspaceInfo{
			Backend:        bridge.WorkspaceBackendContainer,
			DefaultWorkDir: root,
		},
	}
	pool := newSessionPool(nil, workspace, fakeBotGetter{bot: bot}, newTestSupervisor(t), Deps{})
	return pool, root
}

func TestDriverRestoresNativeSession(t *testing.T) {
	stored := func(id, threadID string) map[string]any {
		return map[string]any{NativeSessionKey: map[string]any{"id": id, "thread_id": threadID}}
	}
	for _, tt := range []struct {
		name string
		// resume is the fake agent's answer to session/resume: "1" restores,
		// a number refuses with that JSON-RPC code.
		resume      string
		metadata    map[string]any
		fresh       bool
		ensureFirst bool
		// image is a prompt the agent cannot take, so it is never sent.
		image   bool
		calls   string
		wantErr bool
		notices int
		// persisted is the session ID written back, empty for none.
		persisted string
	}{
		{name: "restores the stored session", resume: "1", metadata: stored("saved", "thread-1"), calls: "resume:saved"},
		{name: "agent lost the session", resume: "-32002", metadata: stored("saved", "thread-1"), calls: "resume:saved new", notices: 1, persisted: "session-pool-fake-session"},
		{name: "a runtime control started the replacement", resume: "-32002", metadata: stored("saved", "thread-1"), ensureFirst: true, calls: "resume:saved new", notices: 1, persisted: "session-pool-fake-session"},
		{name: "copied from another thread", resume: "1", metadata: stored("saved", "thread-0"), calls: "new", notices: 1, persisted: "session-pool-fake-session"},
		{name: "thread without a stored session", resume: "1", metadata: map[string]any{}, calls: "new", persisted: "session-pool-fake-session"},
		{name: "a prompt the agent never got records no session", resume: "1", metadata: map[string]any{}, image: true, calls: "new", wantErr: true},
		{name: "discuss starts fresh", resume: "1", metadata: stored("saved", "thread-1"), fresh: true, calls: "new"},
		{name: "auth failure keeps the stored session", resume: "-32000", metadata: stored("saved", "thread-1"), calls: "resume:saved", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_RESUME", tt.resume)
			sessionLog := filepath.Join(t.TempDir(), "sessions")
			t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_SESSION_LOG", sessionLog)
			pool := newFakeScriptPool(t)
			pool.store = fakeSessionGetter{session: SessionDescriptor{BotID: "bot-1", IsACP: true, AgentID: acpprofile.AgentACPID, ProjectPath: "/data/project", RuntimeMetadata: tt.metadata}}
			if tt.ensureFirst {
				if _, err := pool.Ensure(context.Background(), PromptInput{BotID: "bot-1", SessionID: "thread-1", RuntimeOwnerAccountID: "user-1"}); err != nil {
					t.Fatal(err)
				}
			}
			var images []external.Image
			if tt.image {
				images = []external.Image{{MimeType: "image/png", Data: []byte("image")}}
			}
			notices := 0
			result, err := NewDriver(pool).Prompt(context.Background(), external.PromptInput{
				BotID: "bot-1", ThreadID: "thread-1", RunID: "run-1", Prompt: "continue", Images: images,
				RuntimeOwnerAccountID: "user-1", RuntimeMetadata: tt.metadata, ForceFreshRuntime: tt.fresh,
				Sink: external.EventSinkFunc(func(ev event.StreamEvent) {
					if ev.NoticeKind == event.NoticeNativeHistoryLost {
						notices++
					}
				}),
			})
			if (err != nil) != tt.wantErr {
				t.Fatalf("Prompt() error = %v, want error %v", err, tt.wantErr)
			}
			raw, readErr := os.ReadFile(sessionLog) //nolint:gosec // test-owned temporary file.
			if readErr != nil {
				t.Fatal(readErr)
			}
			if calls := strings.Join(strings.Fields(string(raw)), " "); calls != tt.calls {
				t.Fatalf("session calls = %q, want %q", calls, tt.calls)
			}
			if notices != tt.notices {
				t.Fatalf("history-lost notices = %d, want %d", notices, tt.notices)
			}
			persisted, _ := storedNativeSession(result.RuntimeMetadata, "thread-1")
			if persisted != tt.persisted || (tt.persisted == "" && result.RuntimeMetadata != nil) {
				t.Fatalf("persisted runtime metadata = %#v, want session %q", result.RuntimeMetadata, tt.persisted)
			}
		})
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
	// sessions counts session/new, so each new session has an ID of its own.
	sessions int
}

func (*sessionPoolFakeAgent) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

func (*sessionPoolFakeAgent) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, nil
}

func (*sessionPoolFakeAgent) Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error) {
	if path := os.Getenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_STARTED"); path != "" {
		if err := os.WriteFile(path, []byte("started"), 0o600); err != nil { //nolint:gosec // the test names a path under t.TempDir.
			return acp.InitializeResponse{}, err
		}
	}
	capabilities := acp.AgentCapabilities{LoadSession: false}
	if os.Getenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_RESUME") != "" {
		capabilities.SessionCapabilities.Resume = &acp.SessionResumeCapabilities{}
	}
	if mode := os.Getenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_CLOSE"); mode == "1" || mode == "slow" || mode == "never" {
		capabilities.SessionCapabilities.Close = &acp.SessionCloseCapabilities{}
	}
	return acp.InitializeResponse{
		ProtocolVersion:   acp.ProtocolVersionNumber,
		AgentCapabilities: capabilities,
	}, nil
}

func (*sessionPoolFakeAgent) Cancel(context.Context, acp.CancelNotification) error { return nil }

func (*sessionPoolFakeAgent) CloseSession(_ context.Context, p acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	if os.Getenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_CLOSE") == "never" {
		select {}
	}
	if os.Getenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_CLOSE") == "slow" {
		_ = logPoolSessionCall("close-start:" + string(p.SessionId))
		time.Sleep(300 * time.Millisecond)
		return acp.CloseSessionResponse{}, logPoolSessionCall("close-end:" + string(p.SessionId))
	}
	return acp.CloseSessionResponse{}, logPoolSessionCall("close:" + string(p.SessionId))
}

func (*sessionPoolFakeAgent) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, nil
}

func (a *sessionPoolFakeAgent) NewSession(context.Context, acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	if err := logPoolSessionCall("new"); err != nil {
		return acp.NewSessionResponse{}, err
	}
	a.sessions++
	id := "session-pool-fake-session"
	if a.sessions > 1 {
		id += "-" + strconv.Itoa(a.sessions)
	}
	resp := acp.NewSessionResponse{SessionId: acp.SessionId(id)}
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
	a.appendConfigLog(fmt.Sprintf("prompt:model=%s,reasoning=%s", a.modelID, a.reasoningEffort))
	_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{
		SessionId: p.SessionId,
		Update:    acp.UpdateAgentMessageText("session-pool-ok"),
	})
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

// ResumeSession answers with MEMOH_ACP_SESSION_POOL_FAKE_AGENT_RESUME: "1"
// restores the session, a number is the JSON-RPC error code to refuse with.
func (*sessionPoolFakeAgent) ResumeSession(_ context.Context, p acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	if err := logPoolSessionCall("resume:" + string(p.SessionId)); err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	if code, err := strconv.Atoi(os.Getenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_RESUME")); err == nil && code != 1 {
		return acp.ResumeSessionResponse{}, &acp.RequestError{Code: code, Message: "injected resume failure"}
	}
	return acp.ResumeSessionResponse{}, nil
}

func logPoolSessionCall(call string) error {
	path := os.Getenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_SESSION_LOG")
	if path == "" {
		return nil
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // test-owned temporary file.
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	_, err = file.WriteString(call + "\n")
	return err
}

func (a *sessionPoolFakeAgent) SetSessionConfigOption(_ context.Context, p acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	if p.ValueId == nil || !strings.HasPrefix(string(p.ValueId.SessionId), "session-pool-fake-session") {
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
	if os.Getenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_CONFIG_ANSWER_LATE") == "1" {
		// Applied, but answered after the caller gave up.
		time.Sleep(time.Second)
	}
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

func newTestSupervisor(t *testing.T) *agentproc.Supervisor {
	t.Helper()
	procs := agentproc.New(agentproc.Options{})
	t.Cleanup(func() { procs.Close(context.Background()) })
	return procs
}

// liveProcess returns the Agent's shared process, if it has one.
func liveProcess(p *SessionPool, botID, botAgentID string) *Process {
	r, _ := p.procs.Peek(agentproc.Key{Runtime: RuntimeType, BotID: botID, BotAgentID: botAgentID}).(*Process)
	return r
}

// liveSession returns the session a conversation runs on, if it has one.
func liveSession(p *SessionPool, conversation string) *Binding {
	return p.holder(conversation)
}

func sessionInput(sessionID, prompt string) PromptInput {
	return PromptInput{
		BotID:                 "bot-1",
		SessionID:             sessionID,
		AgentID:               acpprofile.AgentACPID,
		ProjectPath:           "/data/project",
		Prompt:                prompt,
		RuntimeOwnerAccountID: "user-1",
	}
}

// ACP lets one connection host several concurrent sessions: an Agent's
// conversations share its process, each on a session of its own.
func TestSessionPoolSharesTheAgentProcessBetweenConversations(t *testing.T) {
	pool := newFakeScriptPool(t)
	input := sessionInput("session-1", "first prompt")
	result, err := pool.Prompt(context.Background(), input)
	if err != nil || !strings.Contains(result.Text, "session-pool-ok") {
		t.Fatalf("Prompt(first) = %q, %v", result.Text, err)
	}
	proc := liveProcess(pool, "bot-1", "")
	first := liveSession(pool, "session-1")
	if proc == nil || first == nil || first.rt != proc || !strings.HasPrefix(first.id, runtimeIDPrefix) || first.active != nil || proc.Busy() {
		t.Fatalf("process = %#v, session = %#v", proc, first)
	}
	input.Prompt = "second prompt"
	if _, err := pool.Prompt(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if liveSession(pool, "session-1") != first {
		t.Fatal("the same conversation opened a second session")
	}
	if _, err := pool.Prompt(context.Background(), sessionInput("session-2", "third prompt")); err != nil {
		t.Fatal(err)
	}
	other := liveSession(pool, "session-2")
	if other == nil || other == first || other.rt != proc || other.sess.ID() == first.sess.ID() {
		t.Fatalf("another conversation's session = %#v", other)
	}
	if liveProcess(pool, "bot-1", "") != proc {
		t.Fatal("another conversation started a second process")
	}
	status := pool.RuntimeStatus(PromptInput{BotID: "bot-1", SessionID: "session-1"})
	if status.State != stateIdle || status.ACPSession != first.sess.ID() || status.ProjectPath != "/data/project" || status.RuntimeID != first.id {
		t.Fatalf("RuntimeStatus() = %#v", status)
	}
}

func TestSessionPoolFreshTurnRunsOnAThrowawaySession(t *testing.T) {
	pool := newFakeScriptPool(t)
	input := sessionInput("session-1", "first prompt")
	if _, err := pool.Prompt(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	first := liveSession(pool, "session-1")
	input.ForceFreshRuntime = true
	if _, err := pool.Prompt(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if liveSession(pool, "session-1") != first || first.isClosed() {
		t.Fatal("the fresh turn replaced the conversation's session")
	}
	proc := first.rt
	proc.mu.Lock()
	left := len(proc.transient)
	proc.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d throwaway sessions stayed open", left)
	}
}

// A dropped session is gone before the conversation restores it: a
// session/close still on its way would end the session just restored.
func TestADroppedSessionClosesBeforeItIsRestored(t *testing.T) {
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_MODELS", "1")
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_RESUME", "1")
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_CLOSE", "slow")
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_CONFIG_ANSWER_LATE", "1")
	sessionLog := filepath.Join(t.TempDir(), "sessions")
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_SESSION_LOG", sessionLog)
	pool := newFakeScriptPool(t)
	store := &memorySessionStore{sessions: map[string]SessionDescriptor{
		"session-1": {BotID: "bot-1", IsACP: true, AgentID: acpprofile.AgentACPID, ProjectPath: "/data/project", RuntimeOwnerAccountID: "user-1"},
	}}
	pool.store = store
	if _, err := pool.Prompt(context.Background(), sessionInput("session-1", "hello")); err != nil {
		t.Fatal(err)
	}
	store.record("session-1", liveSession(pool, "session-1").sess.ID())
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := pool.SetModel(ctx, sessionInput("session-1", ""), "gpt-5.1-codex-high"); err == nil {
		t.Fatal("an unanswered model change succeeded")
	}
	if _, err := pool.Ensure(context.Background(), sessionInput("session-1", "")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(sessionLog) //nolint:gosec // test-owned temporary file.
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Fields(string(raw))
	closed, resumed := slices.Index(calls, "close-end:session-pool-fake-session"), slices.Index(calls, "resume:session-pool-fake-session")
	if closed < 0 || resumed < 0 || closed > resumed {
		t.Fatalf("session calls = %v, want the close finished before the resume", calls)
	}
}

// A session whose close the agent never confirmed is gone only once its
// process has exited: its native session is restored on another process,
// after the old one ended.
func TestAnUnconfirmedCloseRestoresOnlyAfterTheProcessEnded(t *testing.T) {
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_MODELS", "1")
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_RESUME", "1")
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_CLOSE", "never")
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_CONFIG_ANSWER_LATE", "1")
	pool := newFakeScriptPool(t)
	store := &memorySessionStore{sessions: map[string]SessionDescriptor{
		"session-1": {BotID: "bot-1", IsACP: true, AgentID: acpprofile.AgentACPID, ProjectPath: "/data/project", RuntimeOwnerAccountID: "user-1"},
	}}
	pool.store = store
	if _, err := pool.Prompt(context.Background(), sessionInput("session-1", "hello")); err != nil {
		t.Fatal(err)
	}
	old := liveSession(pool, "session-1")
	store.record("session-1", old.sess.ID())
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := pool.SetModel(ctx, sessionInput("session-1", ""), "gpt-5.1-codex-high"); err == nil {
		t.Fatal("an unanswered model change succeeded")
	}
	if _, err := pool.Ensure(context.Background(), sessionInput("session-1", "")); err != nil {
		t.Fatal(err)
	}
	restored := liveSession(pool, "session-1")
	select {
	case <-old.rt.proc.Exited():
	default:
		t.Fatal("the session was restored while the process holding it still ran")
	}
	if restored.rt == old.rt || restored.sess.ID() != old.sess.ID() {
		t.Fatalf("restored %s on the old process = %v", restored.sess.ID(), restored.rt == old.rt)
	}
}

// The process is out of service the moment its agent process is, before any
// teardown here runs.
func TestAKilledAgentProcessIsOutOfServiceAtOnce(t *testing.T) {
	pool := newFakeScriptPool(t)
	if _, err := pool.Ensure(context.Background(), sessionInput("session-1", "")); err != nil {
		t.Fatal(err)
	}
	proc := liveProcess(pool, "bot-1", "")
	proc.proc.Kill()
	select {
	case <-proc.Done():
	default:
		t.Fatal("a killed agent process is still in service")
	}
}

// A history reset clears the conversation's stored session: the next turn
// opens a new session on the same process, and other conversations keep
// theirs.
func TestSessionPoolOpensANewSessionAfterAReset(t *testing.T) {
	pool := newFakeScriptPool(t)
	store := &memorySessionStore{sessions: map[string]SessionDescriptor{}}
	pool.store = store
	for _, conversation := range []string{"session-1", "session-2"} {
		store.sessions[conversation] = SessionDescriptor{BotID: "bot-1", IsACP: true, AgentID: acpprofile.AgentACPID, ProjectPath: "/data/project", RuntimeOwnerAccountID: "user-1"}
		if _, err := pool.Prompt(context.Background(), sessionInput(conversation, "hello")); err != nil {
			t.Fatal(err)
		}
		store.record(conversation, liveSession(pool, conversation).sess.ID())
	}
	first, other := liveSession(pool, "session-1"), liveSession(pool, "session-2")
	if _, err := pool.Prompt(context.Background(), sessionInput("session-1", "again")); err != nil {
		t.Fatal(err)
	}
	if liveSession(pool, "session-1") != first {
		t.Fatal("a recorded session was not reused")
	}
	store.clear("session-1")
	if _, err := pool.Prompt(context.Background(), sessionInput("session-1", "after reset")); err != nil {
		t.Fatal(err)
	}
	next := liveSession(pool, "session-1")
	if next == nil || next == first || !first.isClosed() || next.rt != first.rt || next.sess.ID() == first.sess.ID() {
		t.Fatalf("after the reset: session = %#v, previous closed = %v", next, first.isClosed())
	}
	if liveSession(pool, "session-2") != other || other.isClosed() {
		t.Fatal("the reset touched another conversation's session")
	}
}

// An agent offers commands and settings by its working directory, so a
// draft's options come from a session in the project it will run in.
func TestAgentOptionsComeFromTheDraftsProject(t *testing.T) {
	pool := newFakeScriptPool(t)
	ctx := context.Background()
	for _, project := range []string{"/data/project", "/data", "/data/project"} {
		out, err := pool.AgentOptions(ctx, PromptInput{BotID: "bot-1", AgentID: acpprofile.AgentACPID, ProjectPath: project})
		if err != nil {
			t.Fatal(err)
		}
		if out.ProjectPath != project {
			t.Fatalf("options for %s came from %q", project, out.ProjectPath)
		}
	}
}

func TestSessionPoolNeverReplacesASessionAtWork(t *testing.T) {
	pool := newFakeScriptPool(t)
	store := &memorySessionStore{sessions: map[string]SessionDescriptor{
		"session-1": {BotID: "bot-1", IsACP: true, AgentID: acpprofile.AgentACPID, ProjectPath: "/data/project", RuntimeOwnerAccountID: "user-1"},
	}}
	pool.store = store
	if _, err := pool.Prompt(context.Background(), sessionInput("session-1", "hello")); err != nil {
		t.Fatal(err)
	}
	store.record("session-1", liveSession(pool, "session-1").sess.ID())
	working := liveSession(pool, "session-1")
	release := working.Hold()
	defer release()
	store.clear("session-1")
	_, err := pool.Ensure(context.Background(), sessionInput("session-1", ""))
	var failure *external.Failure
	if !errors.As(err, &failure) || failure.Kind != external.FailureRuntimeBusy {
		t.Fatalf("replacing a session at work: %v, want runtime busy", err)
	}
	if liveSession(pool, "session-1") != working || working.isClosed() {
		t.Fatal("the session at work was replaced")
	}
}

// A replaced process hands each conversation over: the session closes on the
// old process before the new one restores it, so it is never continued on
// two processes at once.
func TestSessionPoolHandsAConversationOverToANewProcess(t *testing.T) {
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_RESUME", "1")
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_CLOSE", "1")
	sessionLog := filepath.Join(t.TempDir(), "sessions")
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_SESSION_LOG", sessionLog)
	pool := newFakeScriptPool(t)
	store := &memorySessionStore{sessions: map[string]SessionDescriptor{
		"session-1": {BotID: "bot-1", IsACP: true, AgentID: acpprofile.AgentACPID, ProjectPath: "/data/project", RuntimeOwnerAccountID: "user-1"},
	}}
	pool.store = store
	if _, err := pool.Prompt(context.Background(), sessionInput("session-1", "hello")); err != nil {
		t.Fatal(err)
	}
	first := liveSession(pool, "session-1")
	store.record("session-1", first.sess.ID())
	old := first.rt
	// Another conversation's turn still holds the old process when it is
	// replaced, so it stays alive while this conversation moves on.
	l, err := pool.Launch(context.Background(), sessionInput("session-1", ""))
	if err != nil {
		t.Fatal(err)
	}
	held, releaseOld, err := pool.leaseProcess(context.Background(), l)
	if err != nil || held != old {
		t.Fatalf("lease old process = %v, %v", held, err)
	}
	defer releaseOld()
	pool.procs.MarkBroken(old.key, old)
	if _, err := pool.Prompt(context.Background(), sessionInput("session-1", "after replacement")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-old.Done():
		t.Fatal("the old process closed while a lease held it")
	default:
	}
	next := liveSession(pool, "session-1")
	if next == nil || next.rt == old || !first.isClosed() || next.sess.ID() != first.sess.ID() {
		t.Fatalf("after the replacement: session = %#v, previous closed = %v", next, first.isClosed())
	}
	raw, err := os.ReadFile(sessionLog) //nolint:gosec // test-owned temporary file.
	if err != nil {
		t.Fatal(err)
	}
	want := "new close:" + first.sess.ID() + " resume:" + first.sess.ID()
	if calls := strings.Join(strings.Fields(string(raw)), " "); !strings.HasPrefix(calls, want) {
		t.Fatalf("session calls = %q, want them to start %q", calls, want)
	}
}

// memorySessionStore records native sessions the way the application does
// after a turn.
type memorySessionStore struct {
	mu       sync.Mutex
	sessions map[string]SessionDescriptor
}

func (s *memorySessionStore) Get(_ context.Context, id string) (SessionDescriptor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id], nil
}

func (s *memorySessionStore) record(conversation, nativeID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	desc := s.sessions[conversation]
	desc.RuntimeMetadata = nativeSessionMetadata(nativeID, conversation)
	s.sessions[conversation] = desc
}

func (s *memorySessionStore) clear(conversation string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	desc := s.sessions[conversation]
	desc.RuntimeMetadata = map[string]any{}
	s.sessions[conversation] = desc
}

func TestResolveRuntimeToolContextFailsClosed(t *testing.T) {
	pool := newSessionPool(nil, nil, nil, nil, Deps{})
	r := &Process{host: pool.Host, key: agentproc.Key{Runtime: RuntimeType, BotID: "bot-1"}}
	b := &Binding{rt: r, conversation: "session-1", id: "rt_1", toolToken: "token"}
	pool.register(b)
	b.active = &client.ToolSessionContext{RunID: "run-1", SessionToken: "session-token", ContextBudgetMaxTokens: 1000}
	got, ok := pool.ResolveRuntimeToolContext("bot-1", "rt_1", "token")
	if !ok || !got.RuntimeActive || got.RunID != "run-1" || got.SessionToken != "session-token" || got.SessionID != "session-1" || got.ContextBudgetMaxTokens != 1000 {
		t.Fatalf("tool context = %#v, %v", got, ok)
	}
	for _, probe := range [][3]string{{"bot-2", "rt_1", "token"}, {"bot-1", "rt_1", "wrong"}, {"bot-1", "rt_2", "token"}} {
		if _, ok := pool.ResolveRuntimeToolContext(probe[0], probe[1], probe[2]); ok {
			t.Fatalf("resolved %v", probe)
		}
	}
	pool.unregister(b)
	if _, ok := pool.ResolveRuntimeToolContext("bot-1", "rt_1", "token"); ok {
		t.Fatal("a closed session still resolves")
	}
}

type fixedEpochStore struct{ epoch int64 }

func (s *fixedEpochStore) RuntimeConfigEpoch(context.Context, string, string) (agentstate.RuntimeConfigEpoch, error) {
	return agentstate.RuntimeConfigEpoch{Bot: s.epoch}, nil
}

// A start that waited out a reset resolved its command on the old
// configuration: it must not run the agent at all.
func TestAStartQueuedBehindAResetNeverRunsTheOldConfiguration(t *testing.T) {
	started := filepath.Join(t.TempDir(), "started")
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_STARTED", started)
	pool := newFakeScriptPool(t)
	epochs := &fixedEpochStore{epoch: 1}
	pool.stateStore = epochs
	input := PromptInput{BotID: "bot-1", AgentID: acpprofile.AgentACPID}
	launch, err := pool.Launch(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	unblock, err := pool.procs.Drain(t.Context(), func(k agentproc.Key) bool { return k.BotID == input.BotID }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unblock()
	finished := make(chan error, 1)
	go func() {
		_, release, err := pool.leaseProcess(t.Context(), launch)
		if release != nil {
			release()
		}
		finished <- err
	}()
	select {
	case err := <-finished:
		t.Fatalf("lease passed the reset gate: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	epochs.epoch = 2
	unblock()
	if err := <-finished; !errors.Is(err, agentstate.ErrRuntimeConfigStale) {
		t.Fatalf("lease error = %v, want stale configuration", err)
	}
	if _, err := os.Stat(started); err == nil {
		t.Fatal("the agent started on a configuration the reset replaced")
	}
}

// Custom adapters rarely advertise mcpCapabilities.http; they still get
// Memoh's tools over HTTP MCP.
func TestACustomAgentTakesMemohToolsWithoutAdvertisingHTTPMCP(t *testing.T) {
	pool := newFakeScriptPool(t)
	input := sessionInput("s", "hi")
	if _, err := pool.Ensure(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	b := liveSession(pool, "s")
	if b.rt.conn.SupportsHTTPMCP() {
		t.Fatal("the fake agent advertises HTTP MCP; the test needs one that does not")
	}
	if !b.rt.takesHTTPMCP() {
		t.Fatal("a custom agent that does not advertise HTTP MCP is offered no Memoh tools")
	}
}

type fenceRecordingApprovals struct {
	client.ToolApprovalService
	fence runtimefence.Fence
}

func (f *fenceRecordingApprovals) CancelPendingForSession(ctx context.Context, _, _, _ string) ([]toolapproval.Request, error) {
	f.fence, _ = runtimefence.FromContext(ctx)
	return nil, nil
}

type fenceRecordingInputs struct {
	client.UserInputService
	fence runtimefence.Fence
}

func (f *fenceRecordingInputs) CancelPendingForSession(ctx context.Context, _, _, _ string) ([]userinput.Request, error) {
	f.fence, _ = runtimefence.FromContext(ctx)
	return nil, nil
}

// A closing session cancels what its own prompt waits on under that prompt's
// fence, so the stores refuse it once a newer run owns the conversation.
func TestAClosingSessionCancelsDecisionsUnderItsPromptsFence(t *testing.T) {
	approvals, inputs := &fenceRecordingApprovals{}, &fenceRecordingInputs{}
	host := &Host{approval: approvals, userInput: inputs, logger: slog.New(slog.DiscardHandler)}
	fence := runtimefence.Fence{BotID: "bot-1", SessionID: "s", Token: 7}
	b := &Binding{
		rt:           &Process{host: host, key: agentproc.Key{BotID: "bot-1"}},
		conversation: "s",
		active:       &client.ToolSessionContext{RunContext: t.Context(), RuntimeFence: fence},
	}
	b.cancelActiveDecisions()
	if approvals.fence != fence || inputs.fence != fence {
		t.Fatalf("cleanup fences = %+v, %+v; want the prompt's %+v", approvals.fence, inputs.fence, fence)
	}
}
