package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	acpagent "github.com/memohai/memoh/domains/agent/acp"
	acpclient "github.com/memohai/memoh/domains/agent/acp/client"
	acpprofile "github.com/memohai/memoh/domains/agent/acp/profile"
	session "github.com/memohai/memoh/domains/agent/chat/thread"
	acpfeedback "github.com/memohai/memoh/domains/agent/decision/feedback"
	"github.com/memohai/memoh/domains/api/bot"
	agenthttp "github.com/memohai/memoh/domains/api/http/agent"
	"github.com/memohai/memoh/domains/api/http/httpfixture"
	"github.com/memohai/memoh/internal/apperror"
)

type acpRuntimeQueries struct {
	stubThreadStore
	bot         bot.Record
	session     session.Thread
	permissions []byte
}

func (q acpRuntimeQueries) InTransaction(_ context.Context, fn func(session.Store) error) error {
	return fn(q)
}

func (q acpRuntimeQueries) GetBotMetadata(_ context.Context, _ string) (map[string]any, error) {
	return botMetadataMap(q.bot), nil
}

func (q acpRuntimeQueries) GetThread(_ context.Context, _ string) (session.Thread, error) {
	return q.session, nil
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

func (p *fakeACPRuntimePool) CloseRuntime(botID, runtimeID string) error {
	p.closedBotID = botID
	p.closedRuntimeID = runtimeID
	return p.closeErr
}

func TestACPRuntimeHandlerReturnsIdleStatus(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "22222222-2222-2222-2222-222222222222"
	queries := acpRuntimeQueries{
		bot: httpfixture.BotRow(botID, acpEnabledBotMetadata()),
		session: session.Thread{
			ID:    sessionID,
			BotID: botID,
			Type:  session.TypeACPAgent,
			Title: "Codex",
			RuntimeMetadata: map[string]any{
				"acp_agent_id":             acpprofile.AgentCodexID,
				"project_path":             "/data/app",
				"runtime_owner_account_id": "user-1",
			},
		},
	}
	handler := agenthttp.NewACPRuntimeHandler(
		nil,
		newThreadServiceForTest(queries),
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/bots/"+botID+"/sessions/"+sessionID+"/acp-runtime", nil)
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(e, req, rec, "user-1")
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
	if got["agent_id"] != acpprofile.AgentCodexID || got["project_path"] != "/data/app" {
		t.Fatalf("runtime metadata = %#v", got)
	}
}

func TestACPRuntimeHandlerEnsureStartsRuntimeAndReturnsModels(t *testing.T) {
	t.Setenv("MEMOH_ACP_MCP_HTTP_BASE_URL", "http://example.com")

	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "44444444-4444-4444-4444-444444444444"
	queries := acpRuntimeQueries{
		bot: httpfixture.BotRow(botID, acpEnabledBotMetadata()),
		session: session.Thread{
			ID:    sessionID,
			BotID: botID,
			Type:  session.TypeACPAgent,
			Title: "Codex",
			RuntimeMetadata: map[string]any{
				"acp_agent_id":             acpprofile.AgentCodexID,
				"project_path":             "/data/app",
				"runtime_owner_account_id": "user-1",
			},
		},
	}
	pool := &fakeACPRuntimePool{
		status: acpagent.RuntimeStatus{
			SessionID:   sessionID,
			AgentID:     acpprofile.AgentCodexID,
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
	handler := agenthttp.NewACPRuntimeHandlerForTest(
		pool,
		newThreadServiceForTest(queries),
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/bots/"+botID+"/sessions/"+sessionID+"/acp-runtime", nil)
	req.Header.Set("Authorization", "Bearer token-1")
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/sessions/:session_id/acp-runtime")
	ctx.SetParamNames("bot_id", "session_id")
	ctx.SetParamValues(botID, sessionID)

	if err := handler.EnsureRuntime(ctx); err != nil {
		t.Fatalf("EnsureRuntime() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if pool.ensureInput.BotID != botID || pool.ensureInput.SessionID != sessionID || pool.ensureInput.AgentID != acpprofile.AgentCodexID || pool.ensureInput.ProjectPath != "/data/app" {
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
		bot: httpfixture.BotRow(botID, acpEnabledBotMetadata()),
		session: session.Thread{
			ID:    sessionID,
			BotID: botID,
			Type:  session.TypeACPAgent,
			Title: "Codex",
			Metadata: map[string]any{
				"acp_agent_id": acpprofile.AgentCodexID,
				"project_path": "/data/app",
			},
		},
	}
	pool := &fakeACPRuntimePool{}
	handler := agenthttp.NewACPRuntimeHandlerForTest(
		pool,
		newThreadServiceForTest(queries),
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/bots/"+botID+"/sessions/"+sessionID+"/acp-runtime", nil)
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/sessions/:session_id/acp-runtime")
	ctx.SetParamNames("bot_id", "session_id")
	ctx.SetParamValues(botID, sessionID)

	err := handler.EnsureRuntime(ctx)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusConflict {
		t.Fatalf("EnsureRuntime() error = %v, want %d", err, http.StatusConflict)
	}
	feedback, ok := httpErr.Message.(*acpfeedback.Error)
	if !ok || feedback.Code != acpfeedback.CodeRuntimeOwnerMissing {
		t.Fatalf("feedback = %#v, want runtime owner missing", httpErr.Message)
	}
	if pool.ensureInput.BotID != "" {
		t.Fatalf("pool should not be called without runtime owner: %#v", pool.ensureInput)
	}
}

func TestACPRuntimeHandlerEnsureRejectsDifferentRuntimeOwner(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "77777777-7777-7777-7777-777777777777"
	actorUserID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	runtimeOwnerID := "cccccccc-cccc-cccc-cccc-cccccccccccc"
	queries := acpRuntimeQueries{
		bot: httpfixture.BotRow(botID, acpEnabledBotMetadata()),
		session: session.Thread{
			ID:    sessionID,
			BotID: botID,
			Type:  session.TypeACPAgent,
			Title: "Codex",
			RuntimeMetadata: map[string]any{
				"acp_agent_id":             acpprofile.AgentCodexID,
				"project_path":             "/data/app",
				"runtime_owner_account_id": runtimeOwnerID,
			},
		},
		permissions: []byte(`["workspace_exec"]`),
	}
	pool := &fakeACPRuntimePool{}
	handler := agenthttp.NewACPRuntimeHandlerForTest(
		pool,
		newThreadServiceForTest(queries),
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("user"),
	)

	e := echo.New()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/bots/"+botID+"/sessions/"+sessionID+"/acp-runtime", nil)
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(e, req, rec, actorUserID)
	ctx.SetPath("/bots/:bot_id/sessions/:session_id/acp-runtime")
	ctx.SetParamNames("bot_id", "session_id")
	ctx.SetParamValues(botID, sessionID)

	err := handler.EnsureRuntime(ctx)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusForbidden {
		t.Fatalf("EnsureRuntime() error = %v, want HTTP 403", err)
	}
	if pool.ensureInput.BotID != "" {
		t.Fatalf("pool should not be called for a different runtime owner: %#v", pool.ensureInput)
	}
}

func TestAuthorizeACPRuntimeSessionAccess(t *testing.T) {
	t.Run("owner with workspace exec", func(t *testing.T) {
		err := agenthttp.AuthorizeACPRuntimeSessionAccess(
			"user-1",
			[]string{bot.PermissionWorkspaceExec},
			"user-1",
		)
		if err != nil {
			t.Fatalf("agenthttp.AuthorizeACPRuntimeSessionAccess() error = %v", err)
		}
	})

	t.Run("manage cannot bypass owner mismatch", func(t *testing.T) {
		err := agenthttp.AuthorizeACPRuntimeSessionAccess(
			"user-1",
			[]string{bot.PermissionManage},
			"user-2",
		)
		var httpErr *echo.HTTPError
		if !errors.As(err, &httpErr) || httpErr.Code != http.StatusForbidden {
			t.Fatalf("agenthttp.AuthorizeACPRuntimeSessionAccess() error = %v, want HTTP 403", err)
		}
	})

	t.Run("owner still needs workspace exec", func(t *testing.T) {
		err := agenthttp.AuthorizeACPRuntimeSessionAccess(
			"user-1",
			[]string{bot.PermissionChat},
			"user-1",
		)
		var httpErr *echo.HTTPError
		if !errors.As(err, &httpErr) || httpErr.Code != http.StatusForbidden {
			t.Fatalf("agenthttp.AuthorizeACPRuntimeSessionAccess() error = %v, want HTTP 403", err)
		}
	})

	t.Run("different owner is forbidden", func(t *testing.T) {
		err := agenthttp.AuthorizeACPRuntimeSessionAccess(
			"user-1",
			[]string{bot.PermissionWorkspaceExec},
			"user-2",
		)
		var httpErr *echo.HTTPError
		if !errors.As(err, &httpErr) || httpErr.Code != http.StatusForbidden {
			t.Fatalf("agenthttp.AuthorizeACPRuntimeSessionAccess() error = %v, want HTTP 403", err)
		}
	})
}

func TestACPRuntimeHandlerSetModel(t *testing.T) {
	t.Setenv("MEMOH_ACP_MCP_HTTP_BASE_URL", "http://example.com")

	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "55555555-5555-5555-5555-555555555555"
	queries := acpRuntimeQueries{
		bot: httpfixture.BotRow(botID, acpEnabledBotMetadata()),
		session: session.Thread{
			ID:    sessionID,
			BotID: botID,
			Type:  session.TypeACPAgent,
			Title: "Codex",
			RuntimeMetadata: map[string]any{
				"acp_agent_id":             acpprofile.AgentCodexID,
				"project_path":             "/data/app",
				"runtime_owner_account_id": "user-1",
			},
		},
	}
	pool := &fakeACPRuntimePool{
		status: acpagent.RuntimeStatus{
			SessionID:   sessionID,
			AgentID:     acpprofile.AgentCodexID,
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
	handler := agenthttp.NewACPRuntimeHandlerForTest(
		pool,
		newThreadServiceForTest(queries),
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequestWithContext(t.Context(),
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
	ctx := httpfixture.AuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/sessions/:session_id/acp-runtime/model")
	ctx.SetParamNames("bot_id", "session_id")
	ctx.SetParamValues(botID, sessionID)

	if err := handler.SetModel(ctx); err != nil {
		t.Fatalf("SetModel() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if pool.setModelInput.BotID != botID || pool.setModelInput.SessionID != sessionID || pool.setModelInput.AgentID != acpprofile.AgentCodexID || pool.setModelInput.ProjectPath != "/data/app" {
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
		bot: httpfixture.BotRow(botID, acpEnabledBotMetadata()),
		session: session.Thread{
			ID:    sessionID,
			BotID: botID,
			Type:  session.TypeACPAgent,
			RuntimeMetadata: map[string]any{
				"acp_agent_id":             acpprofile.AgentCodexID,
				"project_path":             "/data/app",
				"runtime_owner_account_id": "user-1",
			},
		},
	}
	pool := &fakeACPRuntimePool{status: acpagent.RuntimeStatus{
		SessionID: sessionID,
		AgentID:   acpprofile.AgentCodexID,
		State:     "idle",
		Reasoning: &acpclient.ReasoningState{
			Supported:     true,
			CurrentEffort: "low",
			Available:     []acpclient.ReasoningEffortInfo{{ID: "low", Name: "Low"}},
		},
	}}
	handler := agenthttp.NewACPRuntimeHandlerForTest(
		pool,
		newThreadServiceForTest(queries),
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPatch,
		"/bots/"+botID+"/sessions/"+sessionID+"/acp-runtime/reasoning",
		bytes.NewBufferString(`{"reasoning_effort":"low"}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	requestCtx, cancelRequest := context.WithCancel(req.Context())
	cancelRequest()
	req = req.WithContext(requestCtx)
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(e, req, rec, "user-1")
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
				acpprofile.AgentCodexID: map[string]any{"enabled": true, "setup_mode": "self"},
			},
		},
	}
}

func TestACPRuntimeHandlerCreateRuntime(t *testing.T) {
	t.Setenv("MEMOH_ACP_MCP_HTTP_BASE_URL", "http://example.com")

	botID := "11111111-1111-1111-1111-111111111111"
	queries := acpRuntimeQueries{
		bot: httpfixture.BotRow(botID, acpEnabledBotMetadata()),
	}
	pool := &fakeACPRuntimePool{
		status: acpagent.RuntimeStatus{
			RuntimeID:      "rt_warm",
			AgentID:        acpprofile.AgentCodexID,
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
	handler := agenthttp.NewACPRuntimeHandlerForTest(
		pool,
		newThreadServiceForTest(queries),
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPost,
		"/bots/"+botID+"/acp-runtimes",
		bytes.NewBufferString(`{"acp_agent_id":"codex"}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("Authorization", "Bearer token-3")
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/acp-runtimes")
	ctx.SetParamNames("bot_id")
	ctx.SetParamValues(botID)

	if err := handler.CreateRuntime(ctx); err != nil {
		t.Fatalf("CreateRuntime() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if pool.createInput.BotID != botID || pool.createInput.AgentID != acpprofile.AgentCodexID || pool.createInput.ProjectPath != "/data" {
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

func TestACPRuntimeHandlerSetRuntimeModelRejectsDifferentRuntimeOwner(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := acpRuntimeQueries{
		bot:         httpfixture.BotRow(botID, acpEnabledBotMetadata()),
		permissions: []byte(`["workspace_exec"]`),
	}
	pool := &fakeACPRuntimePool{
		status: acpagent.RuntimeStatus{
			RuntimeID:             "rt_warm",
			AgentID:               acpprofile.AgentCodexID,
			ProjectPath:           "/data",
			RuntimeOwnerAccountID: "owner-1",
			State:                 "idle",
		},
	}
	handler := agenthttp.NewACPRuntimeHandlerForTest(
		pool,
		newThreadServiceForTest(queries),
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("user"),
	)

	e := echo.New()
	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPatch,
		"/bots/"+botID+"/acp-runtimes/rt_warm/model",
		bytes.NewBufferString(`{"model_id":"gpt-5.1-codex-high"}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/acp-runtimes/:runtime_id/model")
	ctx.SetParamNames("bot_id", "runtime_id")
	ctx.SetParamValues(botID, "rt_warm")

	err := handler.SetRuntimeModel(ctx)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusForbidden {
		t.Fatalf("SetRuntimeModel() error = %v, want HTTP 403", err)
	}
	if pool.modelID != "" {
		t.Fatalf("pool should not be called for a different runtime owner: model=%q", pool.modelID)
	}
}

func TestACPRuntimeHandlerCreateRuntimeRejectsDisabledAgent(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := acpRuntimeQueries{
		bot: httpfixture.BotRow(botID, map[string]any{}),
	}
	pool := &fakeACPRuntimePool{}
	handler := agenthttp.NewACPRuntimeHandlerForTest(
		pool,
		newThreadServiceForTest(queries),
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPost,
		"/bots/"+botID+"/acp-runtimes",
		bytes.NewBufferString(`{"acp_agent_id":"codex"}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/acp-runtimes")
	ctx.SetParamNames("bot_id")
	ctx.SetParamValues(botID)

	err := handler.CreateRuntime(ctx)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusForbidden {
		t.Fatalf("CreateRuntime() error = %v, want %d", err, http.StatusForbidden)
	}
	if pool.createInput.BotID != "" {
		t.Fatalf("pool should not be called for a disabled agent: %#v", pool.createInput)
	}
}

func TestACPRuntimeHandlerCreateRuntimeRejectsUnconfiguredAgent(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := acpRuntimeQueries{
		bot: httpfixture.BotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{"enabled": true, "setup_mode": "api_key"},
				},
			},
		}),
	}
	pool := &fakeACPRuntimePool{}
	handler := agenthttp.NewACPRuntimeHandlerForTest(
		pool,
		newThreadServiceForTest(queries),
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPost,
		"/bots/"+botID+"/acp-runtimes",
		bytes.NewBufferString(`{"acp_agent_id":"codex"}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/acp-runtimes")
	ctx.SetParamNames("bot_id")
	ctx.SetParamValues(botID)

	err := handler.CreateRuntime(ctx)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusBadRequest {
		t.Fatalf("CreateRuntime() error = %v, want %d", err, http.StatusBadRequest)
	}
	feedback, ok := httpErr.Message.(*acpfeedback.Error)
	if !ok || feedback.Code != acpfeedback.CodeAgentNotConfigured {
		t.Fatalf("feedback = %#v, want code %s", httpErr.Message, acpfeedback.CodeAgentNotConfigured)
	}
	if pool.createInput.BotID != "" {
		t.Fatalf("pool should not be called for an unconfigured agent: %#v", pool.createInput)
	}
}

func TestACPRuntimeHandlerCreateRuntimeMapsCapToTooManyRequests(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := acpRuntimeQueries{
		bot: httpfixture.BotRow(botID, acpEnabledBotMetadata()),
	}
	pool := &fakeACPRuntimePool{createErr: acpagent.ErrTooManyRuntimes}
	handler := agenthttp.NewACPRuntimeHandlerForTest(
		pool,
		newThreadServiceForTest(queries),
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPost,
		"/bots/"+botID+"/acp-runtimes",
		bytes.NewBufferString(`{"acp_agent_id":"codex"}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/acp-runtimes")
	ctx.SetParamNames("bot_id")
	ctx.SetParamValues(botID)

	err := handler.CreateRuntime(ctx)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusTooManyRequests {
		t.Fatalf("CreateRuntime() error = %v, want %d", err, http.StatusTooManyRequests)
	}
}

func TestACPRuntimeHandlerCreateRuntimeRedactsStartFailure(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := acpRuntimeQueries{
		bot: httpfixture.BotRow(botID, acpEnabledBotMetadata()),
	}
	pool := &fakeACPRuntimePool{createErr: errors.New("start /Users/alice/.codex/auth.json failed with token sk-secret")}
	handler := agenthttp.NewACPRuntimeHandlerForTest(
		pool,
		newThreadServiceForTest(queries),
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPost,
		"/bots/"+botID+"/acp-runtimes",
		bytes.NewBufferString(`{"acp_agent_id":"codex"}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/acp-runtimes")
	ctx.SetParamNames("bot_id")
	ctx.SetParamValues(botID)

	err := handler.CreateRuntime(ctx)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusInternalServerError {
		t.Fatalf("CreateRuntime() error = %v, want %d", err, http.StatusInternalServerError)
	}
	feedback, ok := httpErr.Message.(*acpfeedback.Error)
	if !ok || feedback.Code != acpfeedback.CodeRuntimeStartFailed {
		t.Fatalf("feedback = %#v, want code %s", httpErr.Message, acpfeedback.CodeRuntimeStartFailed)
	}
	if strings.Contains(feedback.Message, "/Users/alice") || strings.Contains(feedback.Message, "sk-secret") {
		t.Fatalf("runtime start feedback leaked raw error: %q", feedback.Message)
	}
}

func TestACPRuntimeHandlerSetRuntimeModelAllowsReset(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := acpRuntimeQueries{
		bot: httpfixture.BotRow(botID, acpEnabledBotMetadata()),
	}
	pool := &fakeACPRuntimePool{
		status: acpagent.RuntimeStatus{
			RuntimeID:             "rt_warm",
			AgentID:               acpprofile.AgentCodexID,
			State:                 "idle",
			RuntimeOwnerAccountID: "user-1",
		},
	}
	handler := agenthttp.NewACPRuntimeHandlerForTest(
		pool,
		newThreadServiceForTest(queries),
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPatch,
		"/bots/"+botID+"/acp-runtimes/rt_warm/model",
		bytes.NewBufferString(`{"model_id":""}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(e, req, rec, "user-1")
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
	queries := acpRuntimeQueries{bot: httpfixture.BotRow(botID, acpEnabledBotMetadata())}
	pool := &fakeACPRuntimePool{status: acpagent.RuntimeStatus{
		RuntimeID:             "rt_warm",
		AgentID:               acpprofile.AgentCodexID,
		State:                 "idle",
		RuntimeOwnerAccountID: "user-1",
	}}
	handler := agenthttp.NewACPRuntimeHandlerForTest(
		pool,
		newThreadServiceForTest(queries),
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPatch,
		"/bots/"+botID+"/acp-runtimes/rt_warm/reasoning",
		bytes.NewBufferString(`{"reasoning_effort":"low"}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(e, req, rec, "user-1")
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

func TestACPRuntimeHandlerRuntimeNotFoundMapsTo404(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := acpRuntimeQueries{
		bot: httpfixture.BotRow(botID, acpEnabledBotMetadata()),
	}
	pool := &fakeACPRuntimePool{statusErr: acpagent.ErrRuntimeNotFound}
	handler := agenthttp.NewACPRuntimeHandlerForTest(
		pool,
		newThreadServiceForTest(queries),
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/bots/"+botID+"/acp-runtimes/rt_gone", nil)
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(e, req, rec, "user-1")
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

func TestACPRuntimeHandlerCloseRuntimeToleratesMissingRuntime(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := acpRuntimeQueries{
		bot: httpfixture.BotRow(botID, acpEnabledBotMetadata()),
	}
	pool := &fakeACPRuntimePool{
		status: acpagent.RuntimeStatus{
			RuntimeID:             "rt_gone",
			AgentID:               acpprofile.AgentCodexID,
			State:                 "idle",
			RuntimeOwnerAccountID: "user-1",
		},
		closeErr: acpagent.ErrRuntimeNotFound,
	}
	handler := agenthttp.NewACPRuntimeHandlerForTest(
		pool,
		newThreadServiceForTest(queries),
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/bots/"+botID+"/acp-runtimes/rt_gone", nil)
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(e, req, rec, "user-1")
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

func TestACPRuntimeHandlerRejectsNonACPSession(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "33333333-3333-3333-3333-333333333333"
	queries := acpRuntimeQueries{
		bot: httpfixture.BotRow(botID, map[string]any{}),
		session: session.Thread{
			ID:       sessionID,
			BotID:    botID,
			Type:     session.TypeChat,
			Title:    "Chat",
			Metadata: map[string]any{},
		},
	}
	handler := agenthttp.NewACPRuntimeHandler(
		nil,
		newThreadServiceForTest(queries),
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/bots/"+botID+"/sessions/"+sessionID+"/acp-runtime", nil)
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/sessions/:session_id/acp-runtime")
	ctx.SetParamNames("bot_id", "session_id")
	ctx.SetParamValues(botID, sessionID)

	err := handler.GetRuntime(ctx)
	if err == nil {
		t.Fatalf("GetRuntime() error = nil, want HTTP 400")
	}
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusBadRequest {
		t.Fatalf("GetRuntime() error = %v, want HTTP 400", err)
	}
}

func TestBuildACPMCPToolsURLUsesOnlyExplicitOrLoopbackBaseURL(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"

	t.Run("explicit base URL", func(t *testing.T) {
		t.Setenv("MEMOH_ACP_MCP_HTTP_BASE_URL", "https://memoh.example")
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://127.0.0.1/acp-runtime", nil)
		req.Header.Set("X-Forwarded-Host", "evil.example")
		got := agenthttp.BuildACPMCPToolsURLFromRequest(req, botID)
		want := "https://memoh.example/bots/" + botID + "/tools"
		if got != want {
			t.Fatalf("tools URL = %q, want %q", got, want)
		}
	})

	t.Run("loopback request host", func(t *testing.T) {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://127.0.0.1:18080/acp-runtime", nil)
		req.Header.Set("X-Forwarded-Host", "evil.example")
		req.Header.Set("X-Forwarded-Proto", "https")
		got := agenthttp.BuildACPMCPToolsURLFromRequest(req, botID)
		want := "http://127.0.0.1:18080/bots/" + botID + "/tools"
		if got != want {
			t.Fatalf("tools URL = %q, want %q", got, want)
		}
	})

	t.Run("non-loopback request host", func(t *testing.T) {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://memoh.example/acp-runtime", nil)
		req.Header.Set("X-Forwarded-Host", "evil.example")
		if got := agenthttp.BuildACPMCPToolsURLFromRequest(req, botID); got != "" {
			t.Fatalf("tools URL = %q, want empty", got)
		}
	})
}
