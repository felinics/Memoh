package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	acpagent "github.com/felinics/memoh/internal/agent/runtime/acp"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/acp/client"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type acpRuntimeQueries struct {
	dbstore.Queries
	bot         sqlc.GetBotByIDRow
	session     sqlc.BotSession
	permissions []byte
}

type fakeACPRuntimePool struct {
	status             acpagent.RuntimeStatus
	statusErr          error
	ensureInput        acpagent.PromptInput
	setModelInput      acpagent.PromptInput
	setModelID         string
	setModelContextErr error
	setReasoningInput  acpagent.PromptInput
	setReasoningEffort string
	setReasoningCtxErr error
	createInput        acpagent.CreateRuntimeInput
	createErr          error
	statusBotID        string
	statusRuntimeID    string
	modelBotID         string
	modelRuntimeID     string
	modelID            string
	reasoningBotID     string
	reasoningRuntimeID string
	reasoningEffort    string
	modeBotID          string
	modeRuntimeID      string
	modeID             string
	modeContextErr     error
	closedBotID        string
	closedRuntimeID    string
	closeErr           error
}

func (*fakeACPRuntimePool) RuntimeStatus(sessionID, agentID, projectPath string) acpagent.RuntimeStatus {
	return acpagent.RuntimeStatus{
		SessionID:   sessionID,
		AgentID:     agentID,
		ProjectPath: projectPath,
		State:       "idle",
	}
}

func (p *fakeACPRuntimePool) Ensure(_ context.Context, input acpagent.PromptInput) (acpagent.RuntimeStatus, error) {
	p.ensureInput = input
	return p.status, nil
}

func (p *fakeACPRuntimePool) SetModel(ctx context.Context, input acpagent.PromptInput, modelID string) (acpagent.RuntimeStatus, error) {
	p.setModelInput = input
	p.setModelID = modelID
	p.setModelContextErr = ctx.Err()
	return p.status, nil
}

func (p *fakeACPRuntimePool) SetReasoning(ctx context.Context, input acpagent.PromptInput, effort string) (acpagent.RuntimeStatus, error) {
	p.setReasoningInput = input
	p.setReasoningEffort = effort
	p.setReasoningCtxErr = ctx.Err()
	return p.status, nil
}

func (p *fakeACPRuntimePool) SetMode(_ context.Context, _ acpagent.PromptInput, _ string) (acpagent.RuntimeStatus, error) {
	return p.status, p.statusErr
}

func (p *fakeACPRuntimePool) CreateRuntime(_ context.Context, input acpagent.CreateRuntimeInput) (acpagent.RuntimeStatus, error) {
	p.createInput = input
	return p.status, p.createErr
}

func (p *fakeACPRuntimePool) RuntimeStatusByID(botID, runtimeID string) (acpagent.RuntimeStatus, error) {
	p.statusBotID = botID
	p.statusRuntimeID = runtimeID
	return p.status, p.statusErr
}

func (p *fakeACPRuntimePool) SetRuntimeModel(_ context.Context, botID, runtimeID, modelID string) (acpagent.RuntimeStatus, error) {
	p.modelBotID = botID
	p.modelRuntimeID = runtimeID
	p.modelID = modelID
	return p.status, p.statusErr
}

func (p *fakeACPRuntimePool) SetRuntimeReasoning(_ context.Context, botID, runtimeID, effort string) (acpagent.RuntimeStatus, error) {
	p.reasoningBotID = botID
	p.reasoningRuntimeID = runtimeID
	p.reasoningEffort = effort
	return p.status, p.statusErr
}

func (p *fakeACPRuntimePool) SetRuntimeMode(ctx context.Context, botID, runtimeID, modeID string) (acpagent.RuntimeStatus, error) {
	p.modeBotID = botID
	p.modeRuntimeID = runtimeID
	p.modeID = modeID
	p.modeContextErr = ctx.Err()
	return p.status, p.statusErr
}

func (p *fakeACPRuntimePool) CloseRuntime(botID, runtimeID string) error {
	p.closedBotID = botID
	p.closedRuntimeID = runtimeID
	return p.closeErr
}

func (q acpRuntimeQueries) GetBotByID(_ context.Context, _ pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return q.bot, nil
}

func (q acpRuntimeQueries) GetSessionByID(_ context.Context, _ pgtype.UUID) (sqlc.BotSession, error) {
	return q.session, nil
}

func (q acpRuntimeQueries) ListBotUserGrantsForUser(_ context.Context, _ sqlc.ListBotUserGrantsForUserParams) ([]sqlc.ListBotUserGrantsForUserRow, error) {
	permissions := q.permissions
	if permissions == nil {
		permissions = []byte(`["chat"]`)
	}
	return []sqlc.ListBotUserGrantsForUserRow{{Permissions: permissions}}, nil
}

func TestACPRuntimeHandlerReturnsIdleStatus(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "22222222-2222-2222-2222-222222222222"
	queries := acpRuntimeQueries{
		bot: testBotRow(botID, acpEnabledBotMetadata()),
		session: sqlc.BotSession{
			ID:    testUUID(sessionID),
			BotID: testUUID(botID),
			Type:  session.TypeACPAgent,
			Title: "Codex",
			RuntimeMetadata: testJSON(map[string]any{
				"acp_agent_id":             acpprofile.AgentACPID,
				"project_path":             "/data/app",
				"runtime_owner_account_id": "user-1",
			}),
		},
	}
	handler := NewACPRuntimeHandler(
		nil,
		session.NewService(nil, queries, nil),
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/bots/"+botID+"/sessions/"+sessionID+"/acp-runtime", nil)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/sessions/:session_id/acp-runtime")
	ctx.SetParamNames("bot_id", "session_id")
	ctx.SetParamValues(botID, sessionID)

	if err := handler.GetRuntime(ctx); err != nil {
		t.Fatalf("GetRuntime() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["state"] != "idle" {
		t.Fatalf("runtime status = %#v, want idle", got)
	}
	if _, ok := got["status"]; ok {
		t.Fatalf("status field should be dropped from response, got %#v", got)
	}
	if _, ok := got["turn_status"]; ok {
		t.Fatalf("turn_status field should be dropped from response, got %#v", got)
	}
	if got["agent_id"] != acpprofile.AgentACPID || got["project_path"] != "/data/app" {
		t.Fatalf("runtime metadata = %#v", got)
	}
}

func TestACPRuntimeHandlerEnsureStartsRuntimeAndReturnsModels(t *testing.T) {
	t.Setenv("MEMOH_ACP_MCP_HTTP_BASE_URL", "http://example.com")

	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "44444444-4444-4444-4444-444444444444"
	queries := acpRuntimeQueries{
		bot: testBotRow(botID, acpEnabledBotMetadata()),
		session: sqlc.BotSession{
			ID:    testUUID(sessionID),
			BotID: testUUID(botID),
			Type:  session.TypeACPAgent,
			Title: "Codex",
			RuntimeMetadata: testJSON(map[string]any{
				"acp_agent_id":             acpprofile.AgentACPID,
				"project_path":             "/data/app",
				"runtime_owner_account_id": "user-1",
			}),
		},
	}
	pool := &fakeACPRuntimePool{
		status: acpagent.RuntimeStatus{
			SessionID:   sessionID,
			AgentID:     acpprofile.AgentACPID,
			ProjectPath: "/data/app",
			State:       "idle",
			ACPSession:  "acp-session-1",
			Models: &acpclient.ModelState{
				Supported:      true,
				CurrentModelID: "gpt-5.1-codex",
				Available: []acpclient.ModelInfo{{
					ID:   "gpt-5.1-codex",
					Name: "GPT-5.1 Codex",
				}},
			},
		},
	}
	handler := newACPRuntimeHandler(
		pool,
		session.NewService(nil, queries, nil),
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/bots/"+botID+"/sessions/"+sessionID+"/acp-runtime", nil)
	req.Header.Set("Authorization", "Bearer token-1")
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/sessions/:session_id/acp-runtime")
	ctx.SetParamNames("bot_id", "session_id")
	ctx.SetParamValues(botID, sessionID)

	if err := handler.EnsureRuntime(ctx); err != nil {
		t.Fatalf("EnsureRuntime() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if pool.ensureInput.BotID != botID || pool.ensureInput.SessionID != sessionID || pool.ensureInput.AgentID != acpprofile.AgentACPID || pool.ensureInput.ProjectPath != "/data/app" {
		t.Fatalf("Ensure input = %#v", pool.ensureInput)
	}
	if pool.ensureInput.SessionToken != "" || pool.ensureInput.ToolHTTPURL != "http://example.com/bots/"+botID+"/tools" {
		t.Fatalf("Ensure tool context = %#v", pool.ensureInput)
	}
	var got acpagent.RuntimeStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ACPSession != "acp-session-1" || got.Models == nil || !got.Models.Supported || got.Models.CurrentModelID != "gpt-5.1-codex" {
		t.Fatalf("EnsureRuntime response = %#v", got)
	}
	if len(got.Models.Available) != 1 || got.Models.Available[0].ID != "gpt-5.1-codex" {
		t.Fatalf("EnsureRuntime models = %#v", got.Models)
	}
}

func TestACPRuntimeHandlerEnsureRejectsMissingRuntimeOwner(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "66666666-6666-6666-6666-666666666666"
	queries := acpRuntimeQueries{
		bot: testBotRow(botID, acpEnabledBotMetadata()),
		session: sqlc.BotSession{
			ID:    testUUID(sessionID),
			BotID: testUUID(botID),
			Type:  session.TypeACPAgent,
			Title: "Codex",
			Metadata: testJSON(map[string]any{
				"acp_agent_id": acpprofile.AgentACPID,
				"project_path": "/data/app",
			}),
		},
	}
	pool := &fakeACPRuntimePool{}
	handler := newACPRuntimeHandler(
		pool,
		session.NewService(nil, queries, nil),
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/bots/"+botID+"/sessions/"+sessionID+"/acp-runtime", nil)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/sessions/:session_id/acp-runtime")
	ctx.SetParamNames("bot_id", "session_id")
	ctx.SetParamValues(botID, sessionID)

	err := handler.EnsureRuntime(ctx)
	problem, ok := apperror.ProblemFrom(err, "")
	if !ok || problem.Status != http.StatusConflict || problem.Code != string(apperror.CodeACPRuntimeConflict) {
		t.Fatalf("EnsureRuntime() error = %v, want %d %s", err, http.StatusConflict, apperror.CodeACPRuntimeConflict)
	}
	if pool.ensureInput.BotID != "" {
		t.Fatalf("pool should not be called without runtime owner: %#v", pool.ensureInput)
	}
}

func TestACPRuntimeHandlerEnsureAllowsWorkspaceExecMember(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "77777777-7777-7777-7777-777777777777"
	actorUserID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	runtimeOwnerID := "cccccccc-cccc-cccc-cccc-cccccccccccc"
	queries := acpRuntimeQueries{
		bot: testBotRow(botID, acpEnabledBotMetadata()),
		session: sqlc.BotSession{
			ID:    testUUID(sessionID),
			BotID: testUUID(botID),
			Type:  session.TypeACPAgent,
			Title: "Codex",
			RuntimeMetadata: testJSON(map[string]any{
				"acp_agent_id":             acpprofile.AgentACPID,
				"project_path":             "/data/app",
				"runtime_owner_account_id": runtimeOwnerID,
			}),
		},
		permissions: []byte(`["workspace_exec"]`),
	}
	pool := &fakeACPRuntimePool{}
	handler := newACPRuntimeHandler(
		pool,
		session.NewService(nil, queries, nil),
		bots.NewService(nil, queries),
		newTestAdminAccountService("user"),
	)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/bots/"+botID+"/sessions/"+sessionID+"/acp-runtime", nil)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, actorUserID)
	ctx.SetPath("/bots/:bot_id/sessions/:session_id/acp-runtime")
	ctx.SetParamNames("bot_id", "session_id")
	ctx.SetParamValues(botID, sessionID)

	if err := handler.EnsureRuntime(ctx); err != nil {
		t.Fatalf("EnsureRuntime() error = %v", err)
	}
	if pool.ensureInput.BotID != botID {
		t.Fatalf("pool input = %#v", pool.ensureInput)
	}
}

func TestAuthorizeACPRuntimeSessionAccess(t *testing.T) {
	t.Run("owner with workspace exec", func(t *testing.T) {
		err := authorizeExternalAgentSessionAccess(
			"user-1",
			[]string{bots.PermissionWorkspaceExec},
			"user-1",
		)
		if err != nil {
			t.Fatalf("authorizeExternalAgentSessionAccess() error = %v", err)
		}
	})

	t.Run("manager may operate another owner's runtime", func(t *testing.T) {
		err := authorizeExternalAgentSessionAccess(
			"user-1",
			[]string{bots.PermissionManage},
			"user-2",
		)
		if err != nil {
			t.Fatalf("authorizeExternalAgentSessionAccess() error = %v", err)
		}
	})

	t.Run("runtime owner without workspace exec is forbidden", func(t *testing.T) {
		// The owner has no standing beyond their live grants: revoking
		// workspace_exec must lock the owner out at decision time.
		err := authorizeExternalAgentSessionAccess(
			"user-1",
			[]string{bots.PermissionChat},
			"user-1",
		)
		var httpErr *echo.HTTPError
		if !errors.As(err, &httpErr) || httpErr.Code != http.StatusForbidden {
			t.Fatalf("authorizeExternalAgentSessionAccess() error = %v, want HTTP 403", err)
		}
	})

	t.Run("workspace exec member may operate another owner's runtime", func(t *testing.T) {
		err := authorizeExternalAgentSessionAccess(
			"user-1",
			[]string{bots.PermissionWorkspaceExec},
			"user-2",
		)
		if err != nil {
			t.Fatalf("authorizeExternalAgentSessionAccess() error = %v", err)
		}
	})

	t.Run("member without workspace exec is forbidden", func(t *testing.T) {
		err := authorizeExternalAgentSessionAccess(
			"user-1",
			[]string{bots.PermissionChat},
			"user-2",
		)
		var httpErr *echo.HTTPError
		if !errors.As(err, &httpErr) || httpErr.Code != http.StatusForbidden {
			t.Fatalf("authorizeExternalAgentSessionAccess() error = %v, want HTTP 403", err)
		}
	})
}

func TestACPRuntimeHandlerSetModel(t *testing.T) {
	t.Setenv("MEMOH_ACP_MCP_HTTP_BASE_URL", "http://example.com")

	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "55555555-5555-5555-5555-555555555555"
	queries := acpRuntimeQueries{
		bot: testBotRow(botID, acpEnabledBotMetadata()),
		session: sqlc.BotSession{
			ID:    testUUID(sessionID),
			BotID: testUUID(botID),
			Type:  session.TypeACPAgent,
			Title: "Codex",
			RuntimeMetadata: testJSON(map[string]any{
				"acp_agent_id":             acpprofile.AgentACPID,
				"project_path":             "/data/app",
				"runtime_owner_account_id": "user-1",
			}),
		},
	}
	pool := &fakeACPRuntimePool{
		status: acpagent.RuntimeStatus{
			SessionID:   sessionID,
			AgentID:     acpprofile.AgentACPID,
			ProjectPath: "/data/app",
			State:       "idle",
			ACPSession:  "acp-session-1",
			Models: &acpclient.ModelState{
				Supported:      true,
				CurrentModelID: "gpt-5.1-codex-high",
				Available: []acpclient.ModelInfo{{
					ID:   "gpt-5.1-codex-high",
					Name: "GPT-5.1 Codex High",
				}},
			},
		},
	}
	handler := newACPRuntimeHandler(
		pool,
		session.NewService(nil, queries, nil),
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequest(
		http.MethodPatch,
		"/bots/"+botID+"/sessions/"+sessionID+"/acp-runtime/model",
		bytes.NewBufferString(`{"model_id":"gpt-5.1-codex-high"}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("Authorization", "Bearer token-2")
	requestCtx, cancelRequest := context.WithCancel(req.Context())
	cancelRequest()
	req = req.WithContext(requestCtx)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/sessions/:session_id/acp-runtime/model")
	ctx.SetParamNames("bot_id", "session_id")
	ctx.SetParamValues(botID, sessionID)

	if err := handler.SetModel(ctx); err != nil {
		t.Fatalf("SetModel() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if pool.setModelInput.BotID != botID || pool.setModelInput.SessionID != sessionID || pool.setModelInput.AgentID != acpprofile.AgentACPID || pool.setModelInput.ProjectPath != "/data/app" {
		t.Fatalf("SetModel input = %#v", pool.setModelInput)
	}
	if pool.setModelInput.SessionToken != "" || pool.setModelInput.ToolHTTPURL != "http://example.com/bots/"+botID+"/tools" {
		t.Fatalf("SetModel tool context = %#v", pool.setModelInput)
	}
	if pool.setModelID != "gpt-5.1-codex-high" {
		t.Fatalf("SetModel model id = %q", pool.setModelID)
	}
	if pool.setModelContextErr != nil {
		t.Fatalf("SetModel context error = %v, want request cancellation detached", pool.setModelContextErr)
	}
	var got acpagent.RuntimeStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Models == nil || got.Models.CurrentModelID != "gpt-5.1-codex-high" {
		t.Fatalf("SetModel response = %#v", got)
	}
}

func TestACPRuntimeHandlerSetReasoning(t *testing.T) {
	t.Setenv("MEMOH_ACP_MCP_HTTP_BASE_URL", "http://example.com")

	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "55555555-5555-5555-5555-555555555555"
	queries := acpRuntimeQueries{
		bot: testBotRow(botID, acpEnabledBotMetadata()),
		session: sqlc.BotSession{
			ID:    testUUID(sessionID),
			BotID: testUUID(botID),
			Type:  session.TypeACPAgent,
			RuntimeMetadata: testJSON(map[string]any{
				"acp_agent_id":             acpprofile.AgentACPID,
				"project_path":             "/data/app",
				"runtime_owner_account_id": "user-1",
			}),
		},
	}
	pool := &fakeACPRuntimePool{status: acpagent.RuntimeStatus{
		SessionID: sessionID,
		AgentID:   acpprofile.AgentACPID,
		State:     "idle",
		Reasoning: &acpclient.ReasoningState{
			Supported:     true,
			CurrentEffort: "low",
			Available:     []acpclient.ReasoningEffortInfo{{ID: "low", Name: "Low"}},
		},
	}}
	handler := newACPRuntimeHandler(
		pool,
		session.NewService(nil, queries, nil),
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequest(
		http.MethodPatch,
		"/bots/"+botID+"/sessions/"+sessionID+"/acp-runtime/reasoning",
		bytes.NewBufferString(`{"reasoning_effort":"low"}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	requestCtx, cancelRequest := context.WithCancel(req.Context())
	cancelRequest()
	req = req.WithContext(requestCtx)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/sessions/:session_id/acp-runtime/reasoning")
	ctx.SetParamNames("bot_id", "session_id")
	ctx.SetParamValues(botID, sessionID)

	if err := handler.SetReasoning(ctx); err != nil {
		t.Fatalf("SetReasoning() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if pool.setReasoningInput.BotID != botID || pool.setReasoningInput.SessionID != sessionID || pool.setReasoningEffort != "low" {
		t.Fatalf("SetReasoning call = %#v, %q", pool.setReasoningInput, pool.setReasoningEffort)
	}
	if pool.setReasoningInput.ToolHTTPURL != "http://example.com/bots/"+botID+"/tools" {
		t.Fatalf("SetReasoning tool context = %#v", pool.setReasoningInput)
	}
	if pool.setReasoningCtxErr != nil {
		t.Fatalf("SetReasoning context error = %v, want request cancellation detached", pool.setReasoningCtxErr)
	}
}

func acpEnabledBotMetadata() map[string]any {
	return map[string]any{
		acpprofile.MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				acpprofile.AgentACPID: map[string]any{"enabled": true, "setup_mode": "api_key", "managed": map[string]any{"command": "my-agent-acp"}},
			},
		},
	}
}

func TestACPRuntimeHandlerCreateRuntime(t *testing.T) {
	t.Setenv("MEMOH_ACP_MCP_HTTP_BASE_URL", "http://example.com")

	botID := "11111111-1111-1111-1111-111111111111"
	queries := acpRuntimeQueries{
		bot: testBotRow(botID, acpEnabledBotMetadata()),
	}
	pool := &fakeACPRuntimePool{
		status: acpagent.RuntimeStatus{
			RuntimeID:      "rt_warm",
			AgentID:        acpprofile.AgentACPID,
			ProjectPath:    "/data",
			State:          "idle",
			DefaultModelID: "gpt-5.1-codex",
			Models: &acpclient.ModelState{
				Supported:      true,
				CurrentModelID: "gpt-5.1-codex",
				Available: []acpclient.ModelInfo{{
					ID:   "gpt-5.1-codex",
					Name: "GPT-5.1 Codex",
				}},
			},
		},
	}
	handler := newACPRuntimeHandler(
		pool,
		session.NewService(nil, queries, nil),
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequest(
		http.MethodPost,
		"/bots/"+botID+"/acp-runtimes",
		bytes.NewBufferString(`{"acp_agent_id":"acp"}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("Authorization", "Bearer token-3")
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/acp-runtimes")
	ctx.SetParamNames("bot_id")
	ctx.SetParamValues(botID)

	if err := handler.CreateRuntime(ctx); err != nil {
		t.Fatalf("CreateRuntime() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if pool.createInput.BotID != botID || pool.createInput.AgentID != acpprofile.AgentACPID || pool.createInput.ProjectPath != "/data" {
		t.Fatalf("CreateRuntime input = %#v", pool.createInput)
	}
	if pool.createInput.RuntimeOwnerAccountID != "user-1" {
		t.Fatalf("CreateRuntime owner = %q, want authenticated user", pool.createInput.RuntimeOwnerAccountID)
	}
	if pool.createInput.ToolHTTPURL != "http://example.com/bots/"+botID+"/tools" {
		t.Fatalf("CreateRuntime tool context = %#v", pool.createInput)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["runtime_id"] != "rt_warm" || got["default_model_id"] != "gpt-5.1-codex" {
		t.Fatalf("CreateRuntime response = %#v", got)
	}
}

func TestACPRuntimeHandlerCreateRuntimeRejectsDisabledAgent(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := acpRuntimeQueries{
		bot: testBotRow(botID, map[string]any{}),
	}
	pool := &fakeACPRuntimePool{}
	handler := newACPRuntimeHandler(
		pool,
		session.NewService(nil, queries, nil),
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequest(
		http.MethodPost,
		"/bots/"+botID+"/acp-runtimes",
		bytes.NewBufferString(`{"acp_agent_id":"acp"}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/acp-runtimes")
	ctx.SetParamNames("bot_id")
	ctx.SetParamValues(botID)

	err := handler.CreateRuntime(ctx)
	problem, ok := apperror.ProblemFrom(err, "")
	if !ok || problem.Status != http.StatusForbidden || problem.Code != string(apperror.CodeACPAccessForbidden) {
		t.Fatalf("CreateRuntime() error = %v, want %d %s", err, http.StatusForbidden, apperror.CodeACPAccessForbidden)
	}
	if pool.createInput.BotID != "" {
		t.Fatalf("pool should not be called for a disabled agent: %#v", pool.createInput)
	}
}

func TestACPRuntimeHandlerCreateRuntimeRejectsUnconfiguredAgent(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := acpRuntimeQueries{
		bot: testBotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentACPID: map[string]any{"enabled": true, "setup_mode": "api_key"},
				},
			},
		}),
	}
	pool := &fakeACPRuntimePool{}
	handler := newACPRuntimeHandler(
		pool,
		session.NewService(nil, queries, nil),
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequest(
		http.MethodPost,
		"/bots/"+botID+"/acp-runtimes",
		bytes.NewBufferString(`{"acp_agent_id":"acp"}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/acp-runtimes")
	ctx.SetParamNames("bot_id")
	ctx.SetParamValues(botID)

	err := handler.CreateRuntime(ctx)
	problem, ok := apperror.ProblemFrom(err, "")
	if !ok || problem.Status != http.StatusBadRequest || problem.Code != string(apperror.CodeACPRequestInvalid) {
		t.Fatalf("CreateRuntime() error = %v, want %d %s", err, http.StatusBadRequest, apperror.CodeACPRequestInvalid)
	}
	if pool.createInput.BotID != "" {
		t.Fatalf("pool should not be called for an unconfigured agent: %#v", pool.createInput)
	}
}

func TestACPRuntimeHandlerCreateRuntimeMapsCapToTooManyRequests(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := acpRuntimeQueries{
		bot: testBotRow(botID, acpEnabledBotMetadata()),
	}
	pool := &fakeACPRuntimePool{createErr: acpagent.ErrTooManyRuntimes}
	handler := newACPRuntimeHandler(
		pool,
		session.NewService(nil, queries, nil),
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequest(
		http.MethodPost,
		"/bots/"+botID+"/acp-runtimes",
		bytes.NewBufferString(`{"acp_agent_id":"acp"}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/acp-runtimes")
	ctx.SetParamNames("bot_id")
	ctx.SetParamValues(botID)

	err := handler.CreateRuntime(ctx)
	problem, ok := apperror.ProblemFrom(err, "")
	if !ok || problem.Status != http.StatusTooManyRequests || problem.Code != string(apperror.CodeACPRuntimeLimitReached) {
		t.Fatalf("CreateRuntime() error = %v, want %d %s", err, http.StatusTooManyRequests, apperror.CodeACPRuntimeLimitReached)
	}
}

func TestACPRuntimeHandlerCreateRuntimeRedactsStartFailure(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := acpRuntimeQueries{
		bot: testBotRow(botID, acpEnabledBotMetadata()),
	}
	pool := &fakeACPRuntimePool{createErr: errors.New("start /Users/alice/.codex/auth.json failed with token sk-secret")}
	handler := newACPRuntimeHandler(
		pool,
		session.NewService(nil, queries, nil),
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequest(
		http.MethodPost,
		"/bots/"+botID+"/acp-runtimes",
		bytes.NewBufferString(`{"acp_agent_id":"acp"}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/acp-runtimes")
	ctx.SetParamNames("bot_id")
	ctx.SetParamValues(botID)

	err := handler.CreateRuntime(ctx)
	problem, ok := apperror.ProblemFrom(err, "")
	if !ok || problem.Status != http.StatusInternalServerError || problem.Code != string(apperror.CodeACPOperationFailed) {
		t.Fatalf("CreateRuntime() error = %v, want %d %s", err, http.StatusInternalServerError, apperror.CodeACPOperationFailed)
	}
	if strings.Contains(problem.Detail, "/Users/alice") || strings.Contains(problem.Detail, "sk-secret") {
		t.Fatalf("runtime start problem leaked raw error: %q", problem.Detail)
	}
	if cause := apperror.CauseOf(err); cause == nil || !strings.Contains(cause.Error(), "sk-secret") {
		t.Fatalf("runtime start cause = %v, want private diagnostic", cause)
	}
}

func TestACPRuntimeHandlerSetRuntimeModelAllowsReset(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := acpRuntimeQueries{
		bot: testBotRow(botID, acpEnabledBotMetadata()),
	}
	pool := &fakeACPRuntimePool{
		status: acpagent.RuntimeStatus{
			RuntimeID:             "rt_warm",
			AgentID:               acpprofile.AgentACPID,
			State:                 "idle",
			RuntimeOwnerAccountID: "user-1",
		},
	}
	handler := newACPRuntimeHandler(
		pool,
		session.NewService(nil, queries, nil),
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequest(
		http.MethodPatch,
		"/bots/"+botID+"/acp-runtimes/rt_warm/model",
		bytes.NewBufferString(`{"model_id":""}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/acp-runtimes/:runtime_id/model")
	ctx.SetParamNames("bot_id", "runtime_id")
	ctx.SetParamValues(botID, "rt_warm")

	if err := handler.SetRuntimeModel(ctx); err != nil {
		t.Fatalf("SetRuntimeModel() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if pool.modelBotID != botID || pool.modelRuntimeID != "rt_warm" || pool.modelID != "" {
		t.Fatalf("SetRuntimeModel call = %q %q %q, want reset request", pool.modelBotID, pool.modelRuntimeID, pool.modelID)
	}
}

func TestACPRuntimeHandlerSetRuntimeReasoning(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := acpRuntimeQueries{bot: testBotRow(botID, acpEnabledBotMetadata())}
	pool := &fakeACPRuntimePool{status: acpagent.RuntimeStatus{
		RuntimeID:             "rt_warm",
		AgentID:               acpprofile.AgentACPID,
		State:                 "idle",
		RuntimeOwnerAccountID: "user-1",
	}}
	handler := newACPRuntimeHandler(
		pool,
		session.NewService(nil, queries, nil),
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequest(
		http.MethodPatch,
		"/bots/"+botID+"/acp-runtimes/rt_warm/reasoning",
		bytes.NewBufferString(`{"reasoning_effort":"low"}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/acp-runtimes/:runtime_id/reasoning")
	ctx.SetParamNames("bot_id", "runtime_id")
	ctx.SetParamValues(botID, "rt_warm")

	if err := handler.SetRuntimeReasoning(ctx); err != nil {
		t.Fatalf("SetRuntimeReasoning() error = %v", err)
	}
	if pool.reasoningBotID != botID || pool.reasoningRuntimeID != "rt_warm" || pool.reasoningEffort != "low" {
		t.Fatalf("SetRuntimeReasoning call = %q %q %q", pool.reasoningBotID, pool.reasoningRuntimeID, pool.reasoningEffort)
	}
}

func TestACPRuntimeHandlerSetRuntimeMode(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := acpRuntimeQueries{bot: testBotRow(botID, acpEnabledBotMetadata())}
	pool := &fakeACPRuntimePool{status: acpagent.RuntimeStatus{
		RuntimeID:             "rt_warm",
		AgentID:               acpprofile.AgentACPID,
		State:                 "idle",
		RuntimeOwnerAccountID: "user-1",
	}}
	handler := newACPRuntimeHandler(
		pool,
		session.NewService(nil, queries, nil),
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequest(
		http.MethodPatch,
		"/bots/"+botID+"/acp-runtimes/rt_warm/mode",
		bytes.NewBufferString(`{"mode_id":"plan"}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	requestCtx, cancelRequest := context.WithCancel(req.Context())
	cancelRequest()
	req = req.WithContext(requestCtx)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/acp-runtimes/:runtime_id/mode")
	ctx.SetParamNames("bot_id", "runtime_id")
	ctx.SetParamValues(botID, "rt_warm")

	if err := handler.SetRuntimeMode(ctx); err != nil {
		t.Fatalf("SetRuntimeMode() error = %v", err)
	}
	if pool.modeBotID != botID || pool.modeRuntimeID != "rt_warm" || pool.modeID != "plan" {
		t.Fatalf("SetRuntimeMode call = %q %q %q", pool.modeBotID, pool.modeRuntimeID, pool.modeID)
	}
	if pool.modeContextErr != nil {
		t.Fatalf("SetRuntimeMode context error = %v, want request cancellation detached", pool.modeContextErr)
	}
}

func TestACPRuntimeHandlerRuntimeNotFoundMapsTo404(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := acpRuntimeQueries{
		bot: testBotRow(botID, acpEnabledBotMetadata()),
	}
	pool := &fakeACPRuntimePool{statusErr: acpagent.ErrRuntimeNotFound}
	handler := newACPRuntimeHandler(
		pool,
		session.NewService(nil, queries, nil),
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/bots/"+botID+"/acp-runtimes/rt_gone", nil)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/acp-runtimes/:runtime_id")
	ctx.SetParamNames("bot_id", "runtime_id")
	ctx.SetParamValues(botID, "rt_gone")

	err := handler.GetRuntimeByID(ctx)
	if got := apperror.CodeOf(err); got != apperror.CodeACPRuntimeNotFound {
		t.Fatalf("GetRuntimeByID() code = %q, want %q", got, apperror.CodeACPRuntimeNotFound)
	}
	if pool.statusBotID != botID || pool.statusRuntimeID != "rt_gone" {
		t.Fatalf("RuntimeStatusByID call = %q %q", pool.statusBotID, pool.statusRuntimeID)
	}
}

func TestRuntimePoolConfigFailureUsesApplicationError(t *testing.T) {
	cause := fmt.Errorf("%w: transport closed", acpagent.ErrRuntimeConfigUpdateFailed)
	err := runtimePoolError(cause)
	if got := apperror.CodeOf(err); got != apperror.CodeACPConfigUpdateFailed {
		t.Fatalf("runtimePoolError() code = %q, want %q", got, apperror.CodeACPConfigUpdateFailed)
	}
	if got := apperror.CauseOf(err); !errors.Is(got, cause) {
		t.Fatalf("runtimePoolError() cause = %v, want private cause", got)
	}
}

func TestRuntimePoolSelectionErrorsUseApplicationErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		code apperror.Code
	}{
		{
			name: "unsupported",
			err:  acpclient.ErrModelSelectionUnsupported,
			code: apperror.CodeACPModelSelectionUnsupported,
		},
		{
			name: "unavailable",
			err:  fmt.Errorf("%w: stale-model", acpclient.ErrModelUnavailable),
			code: apperror.CodeACPModelUnavailable,
		},
		{
			name: "missing",
			err:  acpclient.ErrModelIDRequired,
			code: apperror.CodeACPModelIDRequired,
		},
		{
			name: "reasoning unsupported",
			err:  acpclient.ErrReasoningSelectionUnsupported,
			code: apperror.CodeACPReasoningUnsupported,
		},
		{
			name: "reasoning unavailable",
			err:  fmt.Errorf("%w: stale-effort", acpclient.ErrReasoningEffortUnavailable),
			code: apperror.CodeACPReasoningUnavailable,
		},
		{
			name: "reasoning missing",
			err:  acpclient.ErrReasoningEffortRequired,
			code: apperror.CodeACPReasoningEffortRequired,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := apperror.CodeOf(runtimePoolError(tt.err)); got != tt.code {
				t.Fatalf("runtimePoolError() code = %q, want %q", got, tt.code)
			}
		})
	}
}

func TestACPRuntimeHandlerCloseRuntimeToleratesMissingRuntime(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := acpRuntimeQueries{
		bot: testBotRow(botID, acpEnabledBotMetadata()),
	}
	pool := &fakeACPRuntimePool{
		status: acpagent.RuntimeStatus{
			RuntimeID:             "rt_gone",
			AgentID:               acpprofile.AgentACPID,
			State:                 "idle",
			RuntimeOwnerAccountID: "user-1",
		},
		closeErr: acpagent.ErrRuntimeNotFound,
	}
	handler := newACPRuntimeHandler(
		pool,
		session.NewService(nil, queries, nil),
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequest(http.MethodDelete, "/bots/"+botID+"/acp-runtimes/rt_gone", nil)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/acp-runtimes/:runtime_id")
	ctx.SetParamNames("bot_id", "runtime_id")
	ctx.SetParamValues(botID, "rt_gone")

	if err := handler.CloseRuntime(ctx); err != nil {
		t.Fatalf("CloseRuntime() error = %v", err)
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if pool.closedBotID != botID || pool.closedRuntimeID != "rt_gone" {
		t.Fatalf("CloseRuntime call = %q %q", pool.closedBotID, pool.closedRuntimeID)
	}
}

func TestACPRuntimeHandlerCloseRuntimeToleratesReapedRuntimeLookup(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := acpRuntimeQueries{
		bot: testBotRow(botID, acpEnabledBotMetadata()),
	}
	pool := &fakeACPRuntimePool{
		statusErr: acpagent.ErrRuntimeNotFound,
	}
	handler := newACPRuntimeHandler(
		pool,
		session.NewService(nil, queries, nil),
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequest(http.MethodDelete, "/bots/"+botID+"/acp-runtimes/rt_gone", nil)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/acp-runtimes/:runtime_id")
	ctx.SetParamNames("bot_id", "runtime_id")
	ctx.SetParamValues(botID, "rt_gone")

	if err := handler.CloseRuntime(ctx); err != nil {
		t.Fatalf("CloseRuntime() error = %v", err)
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if pool.closedRuntimeID != "" {
		t.Fatalf("CloseRuntime should not reach the pool, got %q", pool.closedRuntimeID)
	}
}

func TestACPRuntimeHandlerRejectsNonACPSession(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "33333333-3333-3333-3333-333333333333"
	queries := acpRuntimeQueries{
		bot: testBotRow(botID, map[string]any{}),
		session: sqlc.BotSession{
			ID:       testUUID(sessionID),
			BotID:    testUUID(botID),
			Type:     session.TypeChat,
			Title:    "Chat",
			Metadata: testJSON(map[string]any{}),
		},
	}
	handler := NewACPRuntimeHandler(
		nil,
		session.NewService(nil, queries, nil),
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/bots/"+botID+"/sessions/"+sessionID+"/acp-runtime", nil)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/sessions/:session_id/acp-runtime")
	ctx.SetParamNames("bot_id", "session_id")
	ctx.SetParamValues(botID, sessionID)

	err := handler.GetRuntime(ctx)
	if err == nil {
		t.Fatalf("GetRuntime() error = nil, want %s", apperror.CodeACPRequestInvalid)
	}
	if got := apperror.CodeOf(err); got != apperror.CodeACPRequestInvalid {
		t.Fatalf("GetRuntime() code = %q, want %q", got, apperror.CodeACPRequestInvalid)
	}
}

func TestBuildACPMCPToolsURLUsesOnlyExplicitOrLoopbackBaseURL(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"

	t.Run("explicit base URL", func(t *testing.T) {
		t.Setenv("MEMOH_ACP_MCP_HTTP_BASE_URL", "https://memoh.example")
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/acp-runtime", nil)
		req.Header.Set("X-Forwarded-Host", "evil.example")
		got := buildACPMCPToolsURLFromRequest(req, botID)
		want := "https://memoh.example/bots/" + botID + "/tools"
		if got != want {
			t.Fatalf("tools URL = %q, want %q", got, want)
		}
	})

	t.Run("loopback request host", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:18080/acp-runtime", nil)
		req.Header.Set("X-Forwarded-Host", "evil.example")
		req.Header.Set("X-Forwarded-Proto", "https")
		got := buildACPMCPToolsURLFromRequest(req, botID)
		want := "http://127.0.0.1:18080/bots/" + botID + "/tools"
		if got != want {
			t.Fatalf("tools URL = %q, want %q", got, want)
		}
	})

	t.Run("non-loopback request host", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "https://memoh.example/acp-runtime", nil)
		req.Header.Set("X-Forwarded-Host", "evil.example")
		if got := buildACPMCPToolsURLFromRequest(req, botID); got != "" {
			t.Fatalf("tools URL = %q, want empty", got)
		}
	})
}

func testBotRow(botID string, metadata map[string]any) sqlc.GetBotByIDRow {
	return sqlc.GetBotByIDRow{
		ID:          testUUID(botID),
		OwnerUserID: testUUID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"),
		DisplayName: pgtype.Text{
			String: "bot",
			Valid:  true,
		},
		IsActive:  true,
		Status:    bots.BotStatusCreating,
		Metadata:  testJSON(metadata),
		CreatedAt: pgtype.Timestamptz{Valid: true},
		UpdatedAt: pgtype.Timestamptz{Valid: true},
	}
}
