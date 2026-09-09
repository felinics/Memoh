package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/bots"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type sessionCreateQueries struct {
	dbstore.Queries
	bot          sqlc.GetBotByIDRow
	permissions  []byte
	botAgent     sqlc.BotAgent
	createCalled bool
	createParams sqlc.CreateSessionParams
}

func (q *sessionCreateQueries) GetBotAgentByID(_ context.Context, _ sqlc.GetBotAgentByIDParams) (sqlc.BotAgent, error) {
	return q.botAgent, nil
}

func (q *sessionCreateQueries) GetBotByID(_ context.Context, _ pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return q.bot, nil
}

func (*sessionCreateQueries) ListSessionsByBot(_ context.Context, _ pgtype.UUID) ([]sqlc.ListSessionsByBotRow, error) {
	return nil, nil
}

func (q *sessionCreateQueries) ListBotUserGrantsForUser(_ context.Context, _ sqlc.ListBotUserGrantsForUserParams) ([]sqlc.ListBotUserGrantsForUserRow, error) {
	permissions := q.permissions
	if permissions == nil {
		permissions = []byte(`["chat"]`)
	}
	return []sqlc.ListBotUserGrantsForUserRow{{Permissions: permissions}}, nil
}

func (q *sessionCreateQueries) CreateSession(_ context.Context, arg sqlc.CreateSessionParams) (sqlc.BotSession, error) {
	q.createCalled = true
	q.createParams = arg
	return sqlc.BotSession{
		ID:              testUUID("22222222-2222-2222-2222-222222222222"),
		BotID:           arg.BotID,
		BotAgentID:      arg.BotAgentID,
		ChannelType:     arg.ChannelType,
		Type:            arg.Type,
		SessionMode:     arg.SessionMode,
		RuntimeType:     arg.RuntimeType,
		RuntimeMetadata: arg.RuntimeMetadata,
		Title:           arg.Title,
		Metadata:        arg.Metadata,
		CreatedAt:       pgtype.Timestamptz{Valid: true},
		UpdatedAt:       pgtype.Timestamptz{Valid: true},
	}, nil
}

func TestCreateSessionRejectsUnknownTypeAsBadRequest(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := &sessionCreateQueries{
		bot: testBotRow(botID, map[string]any{}),
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	err := callCreateSession(handler, botID, `{"type":"conversation","title":"bad"}`)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusBadRequest {
		t.Fatalf("CreateSession() error = %v, want HTTP 400", err)
	}
	if queries.createCalled {
		t.Fatalf("CreateSession should reject unknown type before DB insert")
	}
}

func TestCreateSessionAuthorizesFinalDescriptor(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := &sessionCreateQueries{
		bot:         testBotRow(botID, map[string]any{}),
		permissions: []byte(`["chat"]`),
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		bots.NewService(nil, queries),
		newTestAdminAccountService("user"),
	)

	err := callCreateSession(handler, botID, `{"type":"chat","session_mode":"discuss","title":"discuss"}`)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusForbidden {
		t.Fatalf("CreateSession() error = %v, want HTTP 403", err)
	}
	if queries.createCalled {
		t.Fatal("CreateSession should authorize the final session descriptor before insert")
	}
}

func TestCreateSessionAcceptsACPAgentType(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := &sessionCreateQueries{
		bot: testBotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentACPID: map[string]any{"enabled": true, "setup_mode": "api_key", "managed": map[string]any{"command": "my-agent-acp"}},
				},
			},
		}),
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	body := `{"type":"acp_agent","title":"Codex","metadata":{"acp_agent_id":"acp","project_path":"/data/app","runtime_owner_account_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}}`
	if err := callCreateSession(handler, botID, body); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if !queries.createCalled {
		t.Fatalf("CreateSession did not insert ACP session")
	}
	if queries.createParams.Type != session.TypeACPAgent {
		t.Fatalf("CreateSession type = %q, want acp_agent", queries.createParams.Type)
	}
	if got := string(queries.createParams.Metadata); !strings.Contains(got, `"acp_agent_id":"acp"`) || !strings.Contains(got, `"project_path":"/data/app"`) {
		t.Fatalf("CreateSession metadata = %s", got)
	}
	var metadata map[string]any
	if err := json.Unmarshal(queries.createParams.Metadata, &metadata); err != nil {
		t.Fatalf("metadata json = %v", err)
	}
	if metadata["runtime_owner_account_id"] != "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb" {
		t.Fatalf("runtime owner = %#v, want authenticated user", metadata["runtime_owner_account_id"])
	}
	var runtimeMetadata map[string]any
	if err := json.Unmarshal(queries.createParams.RuntimeMetadata, &runtimeMetadata); err != nil {
		t.Fatalf("runtime metadata json = %v", err)
	}
	if runtimeMetadata["runtime_owner_account_id"] != "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb" {
		t.Fatalf("runtime metadata owner = %#v, want authenticated user", runtimeMetadata["runtime_owner_account_id"])
	}
}

func TestCreateSessionResolvesPersistedBotAgentDescriptor(t *testing.T) {
	const (
		botID      = "11111111-1111-1111-1111-111111111111"
		botAgentID = "33333333-3333-3333-3333-333333333333"
	)
	queries := &sessionCreateQueries{
		bot: testBotRow(botID, nil),
		botAgent: sqlc.BotAgent{
			ID:                testUUID(botAgentID),
			BotID:             testUUID(botID),
			Name:              "Primary Codex",
			Runtime:           botagents.RuntimeCodex,
			Enabled:           true,
			Metadata:          []byte(`{"provider":"codex","auth":"api_key"}`),
			AgentCredentialID: testUUID("44444444-4444-4444-4444-444444444444"),
			CreatedAt:         pgtype.Timestamptz{Valid: true},
			UpdatedAt:         pgtype.Timestamptz{Valid: true},
		},
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)
	agents := botagents.NewService(slog.Default(), queries)
	agents.SetCredentialResolver(func(context.Context, string, string) string {
		return agentcredential.AuthKindOpenAIAPIKey
	})
	handler.SetBotAgents(agents)

	if err := callCreateSession(handler, botID, `{"type":"chat","bot_agent_id":"`+botAgentID+`","title":"Codex"}`); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if queries.createParams.BotAgentID.String() != botAgentID {
		t.Fatalf("bot_agent_id = %q, want %q", queries.createParams.BotAgentID.String(), botAgentID)
	}
	if queries.createParams.RuntimeType != session.RuntimeCodex {
		t.Fatalf("runtime_type = %q, want %q", queries.createParams.RuntimeType, session.RuntimeCodex)
	}
	var metadata map[string]any
	if err := json.Unmarshal(queries.createParams.Metadata, &metadata); err != nil {
		t.Fatalf("metadata json = %v", err)
	}
	if metadata["project_path"] != session.DefaultACPProjectPath {
		t.Fatalf("metadata = %#v, want external runtime defaults", metadata)
	}
	if metadata["acp_agent_id"] != nil {
		t.Fatalf("metadata = %#v, direct runtime sessions carry no acp_agent_id", metadata)
	}
}

func TestCreateSessionRejectsSystemACPRuntime(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := &sessionCreateQueries{
		bot: testBotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentACPID: map[string]any{"enabled": true, "setup_mode": "api_key", "managed": map[string]any{"command": "my-agent-acp"}},
				},
			},
		}),
		permissions: []byte(`["workspace_exec"]`),
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		bots.NewService(nil, queries),
		newTestAdminAccountService("user"),
	)

	body := `{"type":"schedule","runtime_type":"acp_agent","metadata":{"acp_agent_id":"acp"}}`
	err := callCreateSession(handler, botID, body)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusBadRequest {
		t.Fatalf("CreateSession() error = %v, want HTTP 400", err)
	}
	if queries.createCalled {
		t.Fatal("CreateSession should not insert system ACP sessions")
	}
}

func TestCreateSessionRejectsSubagentTypeForChatUser(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := &sessionCreateQueries{
		bot: testBotRow(botID, map[string]any{}),
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		bots.NewService(nil, queries),
		newTestAdminAccountService("user"),
	)

	err := callCreateSession(handler, botID, `{"type":"subagent","title":"direct child"}`)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusForbidden {
		t.Fatalf("CreateSession() error = %v, want HTTP 403", err)
	}
	if queries.createCalled {
		t.Fatal("chat user should not be able to create subagent sessions directly")
	}
}

func TestCreateSessionDefaultsACPProjectPath(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := &sessionCreateQueries{
		bot: testBotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentACPID: map[string]any{"enabled": true, "setup_mode": "api_key", "managed": map[string]any{"command": "my-agent-acp"}},
				},
			},
		}),
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	body := `{"type":"acp_agent","title":"Codex","metadata":{"acp_agent_id":"acp"}}`
	if err := callCreateSession(handler, botID, body); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(queries.createParams.Metadata, &metadata); err != nil {
		t.Fatalf("metadata json = %v", err)
	}
	if metadata["project_path"] != session.DefaultACPProjectPath || metadata["acp_project_mode"] != session.DefaultACPProjectMode {
		t.Fatalf("CreateSession metadata = %#v, want default ACP project", metadata)
	}
}

type recordingRuntimeBinder struct {
	bindCtx  context.Context
	bindArgs []string
	bindErr  error
}

func (*recordingRuntimeBinder) CloseSession(string) error { return nil }

func (*recordingRuntimeBinder) BeginSessionHistoryReset(ctx context.Context, _, _ string) (context.Context, func(), error) {
	return ctx, func() {}, nil
}

func (b *recordingRuntimeBinder) BindRuntime(ctx context.Context, botID, runtimeID, sessionID, agentID, projectPath, runtimeOwnerAccountID string) error {
	b.bindCtx = ctx
	b.bindArgs = []string{botID, runtimeID, sessionID, agentID, projectPath, runtimeOwnerAccountID}
	return b.bindErr
}

func TestCreateSessionBindsWarmACPRuntime(t *testing.T) {
	type contextKey struct{}

	botID := "11111111-1111-1111-1111-111111111111"
	queries := &sessionCreateQueries{
		bot: testBotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentACPID: map[string]any{"enabled": true, "setup_mode": "api_key", "managed": map[string]any{"command": "my-agent-acp"}},
				},
			},
		}),
	}
	binder := &recordingRuntimeBinder{}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		binder,
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	body := `{"type":"acp_agent","title":"Codex","metadata":{"acp_agent_id":"acp"},"acp_runtime_id":"rt_warm"}`
	requestCtx := context.WithValue(context.Background(), contextKey{}, "create-session-scope")
	req := httptest.NewRequestWithContext(requestCtx, http.MethodPost, "/bots/"+botID+"/sessions", bytes.NewBufferString(body))
	if err := callCreateSessionRequest(handler, botID, req); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	want := []string{botID, "rt_warm", "22222222-2222-2222-2222-222222222222", "acp", session.DefaultACPProjectPath, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"}
	if len(binder.bindArgs) != len(want) {
		t.Fatalf("bind args = %#v, want %#v", binder.bindArgs, want)
	}
	if binder.bindCtx == nil {
		t.Fatal("BindRuntime context is nil")
	}
	if got := binder.bindCtx.Value(contextKey{}); got != "create-session-scope" {
		t.Fatalf("BindRuntime context value = %v, want create-session-scope", got)
	}
	for i := range want {
		if binder.bindArgs[i] != want[i] {
			t.Fatalf("bind args = %#v, want %#v", binder.bindArgs, want)
		}
	}
}

func TestCreateSessionToleratesFailedRuntimeBind(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := &sessionCreateQueries{
		bot: testBotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentACPID: map[string]any{"enabled": true, "setup_mode": "api_key", "managed": map[string]any{"command": "my-agent-acp"}},
				},
			},
		}),
	}
	binder := &recordingRuntimeBinder{bindErr: errors.New("runtime gone")}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		binder,
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	// A failed bind must not fail session creation: the first prompt cold
	// starts instead.
	body := `{"type":"acp_agent","title":"Codex","metadata":{"acp_agent_id":"acp"},"acp_runtime_id":"rt_gone"}`
	if err := callCreateSession(handler, botID, body); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if !queries.createCalled {
		t.Fatalf("session was not created")
	}
}

func callCreateSession(handler *SessionHandler, botID string, body string) error {
	req := httptest.NewRequest(http.MethodPost, "/bots/"+botID+"/sessions", bytes.NewBufferString(body))
	return callCreateSessionRequest(handler, botID, req)
}

func callCreateSessionRequest(handler *SessionHandler, botID string, req *http.Request) error {
	e := echo.New()
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	ctx.SetPath("/bots/:bot_id/sessions")
	ctx.SetParamNames("bot_id")
	ctx.SetParamValues(botID)
	return handler.CreateSession(ctx)
}
