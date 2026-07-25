package chat

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	acpprofile "github.com/memohai/memoh/domains/agent/acp/profile"
	session "github.com/memohai/memoh/domains/agent/chat/thread"
	"github.com/memohai/memoh/domains/api/bot"
	"github.com/memohai/memoh/domains/api/http/httpfixture"
)

type sessionCreateQueries struct {
	stubThreadStore
	bot          bot.Record
	permissions  []byte
	createCalled bool
	createParams session.CreateRecord
}

func (q *sessionCreateQueries) InTransaction(_ context.Context, fn func(session.Store) error) error {
	return fn(q)
}

func (q *sessionCreateQueries) GetBotMetadata(_ context.Context, _ string) (map[string]any, error) {
	return botMetadataMap(q.bot), nil
}

func (q *sessionCreateQueries) CreateThread(_ context.Context, record session.CreateRecord) (session.Thread, error) {
	q.createCalled = true
	q.createParams = record
	return session.Thread{
		ID:              "22222222-2222-2222-2222-222222222222",
		BotID:           record.BotID,
		ChannelType:     record.ChannelType,
		Type:            record.Type,
		SessionMode:     record.SessionMode,
		RuntimeType:     record.RuntimeType,
		RuntimeMetadata: record.RuntimeMetadata,
		Title:           record.Title,
		Metadata:        record.Metadata,
		CreatedByUserID: record.CreatedByUserID,
	}, nil
}

func TestCreateSessionRejectsUnknownTypeAsBadRequest(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := &sessionCreateQueries{
		bot: httpfixture.BotRow(botID, map[string]any{}),
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
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
		bot:         httpfixture.BotRow(botID, map[string]any{}),
		permissions: []byte(`["chat"]`),
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("user"),
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
		bot: httpfixture.BotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{"enabled": true, "setup_mode": "self"},
				},
			},
		}),
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
	)

	body := `{"type":"acp_agent","title":"Codex","metadata":{"acp_agent_id":"codex","project_path":"/data/app","runtime_owner_account_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}}`
	if err := callCreateSession(handler, botID, body); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if !queries.createCalled {
		t.Fatalf("CreateSession did not insert ACP session")
	}
	if queries.createParams.Type != session.TypeACPAgent {
		t.Fatalf("CreateSession type = %q, want acp_agent", queries.createParams.Type)
	}
	if queries.createParams.Metadata["acp_agent_id"] != "codex" || queries.createParams.Metadata["project_path"] != "/data/app" {
		t.Fatalf("CreateSession metadata = %#v", queries.createParams.Metadata)
	}
	if queries.createParams.Metadata["runtime_owner_account_id"] != "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb" {
		t.Fatalf("runtime owner = %#v, want authenticated user", queries.createParams.Metadata["runtime_owner_account_id"])
	}
	if queries.createParams.RuntimeMetadata["runtime_owner_account_id"] != "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb" {
		t.Fatalf("runtime metadata owner = %#v, want authenticated user", queries.createParams.RuntimeMetadata["runtime_owner_account_id"])
	}
}

func TestCreateSessionRejectsSystemACPRuntime(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := &sessionCreateQueries{
		bot: httpfixture.BotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{"enabled": true, "setup_mode": "self"},
				},
			},
		}),
		permissions: []byte(`["workspace_exec"]`),
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("user"),
	)

	body := `{"type":"schedule","runtime_type":"acp_agent","metadata":{"acp_agent_id":"codex"}}`
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
		bot: httpfixture.BotRow(botID, map[string]any{}),
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("user"),
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
		bot: httpfixture.BotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{"enabled": true, "setup_mode": "self"},
				},
			},
		}),
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
	)

	body := `{"type":"acp_agent","title":"Codex","metadata":{"acp_agent_id":"codex"}}`
	if err := callCreateSession(handler, botID, body); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if queries.createParams.Metadata["project_path"] != session.DefaultACPProjectPath || queries.createParams.Metadata["acp_project_mode"] != session.DefaultACPProjectMode {
		t.Fatalf("CreateSession metadata = %#v, want default ACP project", queries.createParams.Metadata)
	}
}

type recordingRuntimeBinder struct {
	bindArgs []string
	bindErr  error
}

func (*recordingRuntimeBinder) CloseSession(string) error { return nil }

func (b *recordingRuntimeBinder) BindRuntime(botID, runtimeID, sessionID, agentID, projectPath, runtimeOwnerAccountID string) error {
	b.bindArgs = []string{botID, runtimeID, sessionID, agentID, projectPath, runtimeOwnerAccountID}
	return b.bindErr
}

func TestCreateSessionBindsWarmACPRuntime(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := &sessionCreateQueries{
		bot: httpfixture.BotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{"enabled": true, "setup_mode": "self"},
				},
			},
		}),
	}
	binder := &recordingRuntimeBinder{}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		binder,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
	)

	body := `{"type":"acp_agent","title":"Codex","metadata":{"acp_agent_id":"codex"},"acp_runtime_id":"rt_warm"}`
	if err := callCreateSession(handler, botID, body); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	want := []string{botID, "rt_warm", "22222222-2222-2222-2222-222222222222", "codex", session.DefaultACPProjectPath, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"}
	if len(binder.bindArgs) != len(want) {
		t.Fatalf("bind args = %#v, want %#v", binder.bindArgs, want)
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
		bot: httpfixture.BotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{"enabled": true, "setup_mode": "self"},
				},
			},
		}),
	}
	binder := &recordingRuntimeBinder{bindErr: errors.New("runtime gone")}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		binder,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
	)

	// A failed bind must not fail session creation: the first prompt cold
	// starts instead.
	body := `{"type":"acp_agent","title":"Codex","metadata":{"acp_agent_id":"codex"},"acp_runtime_id":"rt_gone"}`
	if err := callCreateSession(handler, botID, body); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if !queries.createCalled {
		t.Fatalf("session was not created")
	}
}

func callCreateSession(handler *SessionHandler, botID string, body string) error {
	e := echo.New()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/bots/"+botID+"/sessions", bytes.NewBufferString(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(e, req, rec, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	ctx.SetPath("/bots/:bot_id/sessions")
	ctx.SetParamNames("bot_id")
	ctx.SetParamValues(botID)
	return handler.CreateSession(ctx)
}
