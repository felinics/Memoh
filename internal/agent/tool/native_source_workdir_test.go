package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/agent/background"
	"github.com/felinics/memoh/internal/chat/thread"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/workdir"
	"github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspace/bridgesvc"
)

func TestPostgresNativeMCPExecUsesPersistedParentAndChildWorkdir(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	ctx := t.Context()
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(ctx)) })
	userID, botID, agentID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := tx.Exec(ctx, `WITH created AS (INSERT INTO users (id,username,is_active) VALUES ($1,$2,true) RETURNING id) INSERT INTO team_members (user_id,role) SELECT id,'admin' FROM created`, userID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO bots (id,owner_user_id,name) VALUES ($1,$2,$3)`, botID, userID, botID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO bot_agents (id,bot_id,name,runtime,metadata) VALUES ($1,$2,'MCP workdir','codex','{"auth":"api_key"}')`, agentID, botID); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	queries := sqlc.New(tx)
	store := postgresstore.NewWithQueries(queries)
	saved, err := store.CreateWorkdir(ctx, dbstore.CreateBotWorkdirInput{BotID: botID, Name: "Project", TargetKind: workspace.WorkspaceTargetNative, Path: project, CreatedByUserID: userID})
	if err != nil {
		t.Fatal(err)
	}
	sessions := thread.NewService(nil, postgresstore.NewQueries(queries), nil)
	parent, err := sessions.Create(ctx, thread.CreateInput{BotID: botID, RuntimeType: thread.RuntimeCodex, BotAgentID: agentID, WorkdirID: saved.ID, CreatedByUserID: userID})
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := sessions.CreateSubagent(ctx, thread.CreateSubagentInput{Thread: thread.CreateInput{BotID: botID, RuntimeType: thread.RuntimeCodex, BotAgentID: agentID, ParentThreadID: parent.ID, CreatedByUserID: userID, RuntimeMetadata: map[string]any{"runtime_owner_account_id": userID}}})
	if err != nil {
		t.Fatal(err)
	}
	gateway := nativeWorkdirGateway(t, root, sessions, workdir.NewService(store, nil))
	for _, session := range []thread.Thread{parent, child} {
		t.Run(session.Type, func(t *testing.T) {
			identity := mcp.ToolSessionContext{BotID: botID, SessionID: session.ID, SessionType: session.SessionMode, IsSubagent: session.Type == thread.TypeSubagent}
			descriptors, err := gateway.ListTools(ctx, identity)
			if err != nil || len(descriptors) != 1 || !strings.Contains(descriptors[0].Description, "Runs in "+project+" by default") {
				t.Fatalf("MCP listing lost the stored workdir: descriptors=%#v err=%v", descriptors, err)
			}
			result, err := gateway.CallTool(ctx, identity, mcp.ToolCallPayload{Name: "exec", Arguments: map[string]any{"command": "pwd"}})
			if err != nil || nativeWorkdirStdout(t, result) != project+"\n" {
				t.Fatalf("MCP exec did not use stored workdir: result=%#v err=%v want=%q", result, err, project)
			}
			result, err = gateway.CallTool(ctx, identity, mcp.ToolCallPayload{Name: "exec", Arguments: map[string]any{"command": "pwd", "work_dir": root}})
			if err != nil || nativeWorkdirStdout(t, result) != root+"\n" {
				t.Fatalf("MCP exec ignored explicit work_dir: result=%#v err=%v", result, err)
			}
			result, err = gateway.CallTool(ctx, identity, mcp.ToolCallPayload{Name: "exec", Arguments: map[string]any{"command": "pwd", "target_id": "other-computer"}})
			if err == nil && result["isError"] != true {
				t.Fatalf("MCP exec escaped the stored target: %#v", result)
			}
		})
	}
	result, err := gateway.CallTool(ctx, mcp.ToolSessionContext{BotID: uuid.NewString(), SessionID: parent.ID}, mcp.ToolCallPayload{Name: "exec", Arguments: map[string]any{"command": "pwd"}})
	if err == nil && result["isError"] != true {
		t.Fatalf("foreign bot reused a private workdir: %#v", result)
	}
}

type failingMCPWorkdirSessions struct{ reads int }

func (s *failingMCPWorkdirSessions) Get(context.Context, string) (thread.Thread, error) {
	s.reads++
	return thread.Thread{}, errors.New("PRIVATE_DATABASE_DIAGNOSTIC")
}

type boundMCPWorkdirSessions struct{}

func (boundMCPWorkdirSessions) Get(context.Context, string) (thread.Thread, error) {
	return thread.Thread{BotID: "bot", WorkdirID: "stored-workdir"}, nil
}

type failingMCPWorkdirResolver struct{}

func (failingMCPWorkdirResolver) ResolveForSession(context.Context, string, string) (workdir.Resolved, error) {
	return workdir.Resolved{}, errors.New("PRIVATE_WORKDIR_DIAGNOSTIC")
}

func TestNativeMCPWorkdirLookupFailureDoesNotExecuteOrLeak(t *testing.T) {
	root := t.TempDir()
	gateway := nativeWorkdirGateway(t, root, boundMCPWorkdirSessions{}, failingMCPWorkdirResolver{})
	result, err := gateway.CallTool(t.Context(), mcp.ToolSessionContext{BotID: "bot", SessionID: "trusted-session"}, mcp.ToolCallPayload{Name: "exec", Arguments: map[string]any{"command": "touch reached"}})
	if err == nil && result["isError"] != true {
		t.Fatalf("failed workdir lookup executed against the default: %#v", result)
	}
	if _, statErr := os.Stat(filepath.Join(root, "reached")); !os.IsNotExist(statErr) {
		t.Fatalf("failed workdir lookup produced a side effect: %v", statErr)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "PRIVATE_WORKDIR_DIAGNOSTIC") {
		t.Fatalf("private diagnostic leaked: %s", encoded)
	}
}

func TestNativeMCPPublicHeadersCannotBindPrivateWorkdir(t *testing.T) {
	root := t.TempDir()
	sessions := &failingMCPWorkdirSessions{}
	gateway := nativeWorkdirGateway(t, root, sessions, nil)
	result, err := gateway.CallTool(t.Context(), mcp.ToolSessionContext{BotID: "bot", SessionID: "private-session-header", PublicRequest: true}, mcp.ToolCallPayload{Name: "exec", Arguments: map[string]any{"command": "pwd"}})
	if err != nil || nativeWorkdirStdout(t, result) != root+"\n" || sessions.reads != 0 {
		t.Fatalf("public header looked up private workdir: reads=%d result=%#v err=%v", sessions.reads, result, err)
	}
}

func TestNativeMCPSessionLookupFailureDoesNotExecuteOrLeak(t *testing.T) {
	root := t.TempDir()
	sessions := &failingMCPWorkdirSessions{}
	gateway := nativeWorkdirGateway(t, root, sessions, nil)
	result, err := gateway.CallTool(t.Context(), mcp.ToolSessionContext{BotID: "bot", SessionID: "trusted-session"}, mcp.ToolCallPayload{Name: "exec", Arguments: map[string]any{"command": "touch reached"}})
	if err == nil && result["isError"] != true {
		t.Fatalf("failed lookup executed against default workdir: %#v", result)
	}
	if _, statErr := os.Stat(filepath.Join(root, "reached")); !os.IsNotExist(statErr) {
		t.Fatalf("failed lookup produced a side effect: %v", statErr)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "PRIVATE_DATABASE_DIAGNOSTIC") {
		t.Fatalf("private diagnostic leaked: %s", encoded)
	}
}

func nativeWorkdirGateway(t *testing.T, root string, sessions NativeToolSessionReader, workdirs NativeToolWorkdirResolver, bg ...*background.Manager) *mcp.ToolGatewayService {
	t.Helper()
	gateway, _ := nativeWorkdirTargetGateway(t, root, sessions, workdirs, bg...)
	return gateway
}

func nativeWorkdirTargetGateway(t *testing.T, root string, sessions NativeToolSessionReader, workdirs NativeToolWorkdirResolver, bg ...*background.Manager) (*mcp.ToolGatewayService, *containerTestTargetProvider) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pb.RegisterContainerServiceServer(server, bridgesvc.New(bridgesvc.Options{DefaultWorkDir: root, DataMount: root, WorkspaceRoot: root, AllowHostAbsolute: true}))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	connection, err := grpc.NewClient("passthrough://native-workdir", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	client := bridge.NewClientFromConn(connection)
	info := bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: root}
	provider := &containerTestTargetProvider{containerTestBridgeProvider: containerTestBridgeProvider{client: client, info: info}, resolved: workspace.ResolvedWorkspaceTarget{TargetID: workspace.WorkspaceTargetNative, Kind: workspace.WorkspaceTargetNative, Client: client, Info: info}}
	var manager *background.Manager
	if len(bg) > 0 {
		manager = bg[0]
	}
	source := NewNativeToolSource(nil, []ToolProvider{NewContainerProvider(nil, provider, manager, root)}, NativeToolSourceOptions{AllowTools: map[string]bool{"exec": true}, Sessions: sessions, Workdirs: workdirs})
	return mcp.NewToolGatewayService(nil, []mcp.ToolSource{source}), provider
}

func nativeWorkdirStdout(t *testing.T, result map[string]any) string {
	t.Helper()
	content, ok := result["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("no structured tool result: %#v", result)
	}
	stdout, _ := content["stdout"].(string)
	return stdout
}

type subagentMarkerSessions struct{}

func (subagentMarkerSessions) Get(_ context.Context, id string) (thread.Thread, error) {
	return thread.Thread{ID: id, BotID: "bot"}, nil
}

func TestNativeMCPExecMarksSubagentProcesses(t *testing.T) {
	childID := uuid.NewString()
	for _, tc := range []struct {
		name       string
		bg         *background.Manager
		background bool
	}{{"plain", nil, false}, {"foreground", background.New(nil), false}, {"background", background.New(nil), true}} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			gateway := nativeWorkdirGateway(t, root, subagentMarkerSessions{}, nil, tc.bg)
			for _, identity := range []mcp.ToolSessionContext{
				{BotID: "bot", SessionID: childID, SessionType: "subagent", IsSubagent: true},
				{BotID: "bot", SessionID: uuid.NewString(), SessionType: "chat"},
			} {
				out := filepath.Join(root, identity.SessionID)
				result, err := gateway.CallTool(t.Context(), identity, mcp.ToolCallPayload{Name: "exec", Arguments: map[string]any{"command": `printf %s "$MEMOH_SUBAGENT_THREAD" > ` + out + `.tmp && mv ` + out + `.tmp ` + out, "run_in_background": tc.background}})
				if err != nil || result["isError"] == true {
					t.Fatalf("exec failed: %#v %v", result, err)
				}
				want := ""
				if identity.IsSubagent {
					want = childID
				}
				deadline := time.Now().Add(5 * time.Second)
				for {
					data, err := os.ReadFile(out) //nolint:gosec // test output path.
					if err == nil {
						if string(data) != want {
							t.Fatalf("session %s process marker=%q want %q", identity.SessionType, data, want)
						}
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("command never ran: %v", err)
					}
					time.Sleep(20 * time.Millisecond)
				}
			}
		})
	}
}

type unboundSubagentSessions struct{ project string }

func (s unboundSubagentSessions) Get(_ context.Context, id string) (thread.Thread, error) {
	return thread.Thread{ID: id, BotID: "bot", Type: thread.TypeSubagent, SessionMode: thread.TypeSubagent, RuntimeType: thread.RuntimeCodex, RuntimeMetadata: map[string]any{"project_path": s.project}}, nil
}

func TestNativeMCPKeepsExternalSubagentToolsOnTheServerWorkspace(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	gateway, target := nativeWorkdirTargetGateway(t, root, unboundSubagentSessions{project: project}, nil)
	identity := mcp.ToolSessionContext{BotID: "bot", SessionID: uuid.NewString(), SessionType: thread.TypeSubagent, IsSubagent: true}
	result, err := gateway.CallTool(t.Context(), identity, mcp.ToolCallPayload{Name: "exec", Arguments: map[string]any{"command": "touch reached", "target_id": uuid.NewString()}})
	if err == nil && result["isError"] != true {
		t.Fatalf("external subagent moved a command off the Server Workspace: %#v", result)
	}
	if _, statErr := os.Stat(filepath.Join(project, "reached")); !os.IsNotExist(statErr) {
		t.Fatalf("rejected target still ran the command: %v", statErr)
	}
	result, err = gateway.CallTool(t.Context(), identity, mcp.ToolCallPayload{Name: "exec", Arguments: map[string]any{"command": "pwd"}})
	if err != nil || nativeWorkdirStdout(t, result) != project+"\n" || target.resolvedCtx != workspace.WorkspaceTargetNative {
		t.Fatalf("external subagent default left the Server Workspace or its project: target=%q result=%#v err=%v", target.resolvedCtx, result, err)
	}
}
