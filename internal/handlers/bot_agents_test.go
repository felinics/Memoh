package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	agentfeedback "github.com/felinics/memoh/internal/agent/decision/feedback"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

const (
	botAgentsTestBotID   = "11111111-1111-1111-1111-111111111111"
	botAgentsTestCodexID = "33333333-3333-3333-3333-333333333333"
	botAgentsTestACPID   = "44444444-4444-4444-4444-444444444444"
)

// botAgentsQueries backs both the bots service (authorization) and the
// botagents service with an in-memory row set.
type botAgentsQueries struct {
	dbstore.Queries
	bot          sqlc.GetBotByIDRow
	rows         []sqlc.BotAgent
	createParams *sqlc.CreateBotAgentParams
}

func (*botAgentsQueries) SupportsTransactions() bool { return false }

func (q *botAgentsQueries) GetBotByID(context.Context, pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return q.bot, nil
}

func (q *botAgentsQueries) ListBotAgents(context.Context, pgtype.UUID) ([]sqlc.BotAgent, error) {
	return q.rows, nil
}

func (q *botAgentsQueries) GetBotAgentByID(_ context.Context, params sqlc.GetBotAgentByIDParams) (sqlc.BotAgent, error) {
	for _, row := range q.rows {
		if row.ID == params.ID {
			return row, nil
		}
	}
	return sqlc.BotAgent{}, pgx.ErrNoRows
}

func (q *botAgentsQueries) CreateBotAgent(_ context.Context, params sqlc.CreateBotAgentParams) (sqlc.BotAgent, error) {
	q.createParams = &params
	return sqlc.BotAgent{
		ID:        testUUID(botAgentsTestCodexID),
		BotID:     params.BotID,
		Name:      params.Name,
		Runtime:   params.Runtime,
		Enabled:   params.Enabled,
		Metadata:  params.Metadata,
		CreatedAt: pgtype.Timestamptz{Valid: true},
		UpdatedAt: pgtype.Timestamptz{Valid: true},
	}, nil
}

func (q *botAgentsQueries) UpdateBotAgent(_ context.Context, params sqlc.UpdateBotAgentParams) (sqlc.BotAgent, error) {
	for _, row := range q.rows {
		if row.ID == params.ID {
			row.Name = params.Name
			row.Enabled = params.Enabled
			return row, nil
		}
	}
	return sqlc.BotAgent{}, pgx.ErrNoRows
}

// dependencyDriver is a direct runtime that declares a managed dependency.
type dependencyDriver struct {
	runtimeType string
	depID       string
	version     string
}

func (d dependencyDriver) RuntimeType() string { return d.runtimeType }

func (dependencyDriver) Prompt(context.Context, external.PromptInput) (external.PromptResult, error) {
	return external.PromptResult{}, nil
}

func (d dependencyDriver) RequiredDependency() (string, string) { return d.depID, d.version }

// plainDriver is a runtime without a dependency declaration (the generic ACP
// runtime, or a direct runtime whose CLI is not yet managed).
type plainDriver struct {
	runtimeType string
}

func (d plainDriver) RuntimeType() string { return d.runtimeType }

func (plainDriver) Prompt(context.Context, external.PromptInput) (external.PromptResult, error) {
	return external.PromptResult{}, nil
}

func botAgentRow(id, runtime string, enabled bool) sqlc.BotAgent {
	return sqlc.BotAgent{
		ID:        testUUID(id),
		BotID:     testUUID(botAgentsTestBotID),
		Name:      runtime,
		Runtime:   runtime,
		Enabled:   enabled,
		Metadata:  testJSON(map[string]any{"provider": runtime}),
		CreatedAt: pgtype.Timestamptz{Valid: true},
		UpdatedAt: pgtype.Timestamptz{Valid: true},
	}
}

func newBotAgentsTestHandler(queries *botAgentsQueries, drivers ...external.Driver) *BotAgentsHandler {
	queries.bot = testBotRow(botAgentsTestBotID, nil)
	return NewBotAgentsHandler(
		slog.Default(),
		botagents.NewService(nil, queries),
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
		external.Drivers(drivers),
	)
}

func botAgentsRequest(t *testing.T, method, path, body string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	var reader *bytes.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	rec := httptest.NewRecorder()
	ctx := testAuthContext(echo.New(), req, rec, "admin")
	return ctx, rec
}

func decodeBotAgent(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode response %s: %v", raw, err)
	}
	return got
}

func assertDependency(t *testing.T, agent map[string]any, depID, version string) {
	t.Helper()
	dependency, ok := agent["dependency"].(map[string]any)
	if !ok {
		t.Fatalf("agent %v: dependency = %#v, want object", agent["runtime"], agent["dependency"])
	}
	if dependency["dependency_id"] != depID || dependency["required_version"] != version {
		t.Fatalf("agent %v: dependency = %#v, want %s@%s", agent["runtime"], dependency, depID, version)
	}
}

func assertNoDependency(t *testing.T, agent map[string]any) {
	t.Helper()
	if value, ok := agent["dependency"]; ok {
		t.Fatalf("agent %v: dependency = %#v, want the field omitted", agent["runtime"], value)
	}
}

func TestBotAgentsHandlerListReportsRuntimeDependency(t *testing.T) {
	queries := &botAgentsQueries{rows: []sqlc.BotAgent{
		botAgentRow(botAgentsTestCodexID, botagents.RuntimeCodex, true),
		botAgentRow(botAgentsTestACPID, botagents.RuntimeACP, true),
	}}
	handler := newBotAgentsTestHandler(queries,
		dependencyDriver{runtimeType: botagents.RuntimeCodex, depID: "codex", version: "0.151.0"},
		plainDriver{runtimeType: "acp_agent"},
	)

	ctx, rec := botAgentsRequest(t, http.MethodGet, "/bots/"+botAgentsTestBotID+"/agents", "")
	ctx.SetParamNames("bot_id")
	ctx.SetParamValues(botAgentsTestBotID)
	if err := handler.List(ctx); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var got struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(got.Items))
	}
	assertDependency(t, got.Items[0], "codex", "0.151.0")
	assertNoDependency(t, got.Items[1])
}

func TestBotAgentsHandlerGetDependencyFollowsDriverDeclaration(t *testing.T) {
	tests := []struct {
		name    string
		driver  external.Driver
		wantDep bool
	}{
		{name: "driver declares dependency", driver: dependencyDriver{runtimeType: botagents.RuntimeClaudeCode, depID: "claude-code", version: "2.1.0"}, wantDep: true},
		{name: "driver without declaration", driver: plainDriver{runtimeType: botagents.RuntimeClaudeCode}, wantDep: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			queries := &botAgentsQueries{rows: []sqlc.BotAgent{
				botAgentRow(botAgentsTestCodexID, botagents.RuntimeClaudeCode, true),
			}}
			handler := newBotAgentsTestHandler(queries, tc.driver)

			ctx, rec := botAgentsRequest(t, http.MethodGet, "/bots/"+botAgentsTestBotID+"/agents/"+botAgentsTestCodexID, "")
			ctx.SetParamNames("bot_id", "id")
			ctx.SetParamValues(botAgentsTestBotID, botAgentsTestCodexID)
			if err := handler.Get(ctx); err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
			}
			got := decodeBotAgent(t, rec.Body.Bytes())
			if tc.wantDep {
				assertDependency(t, got, "claude-code", "2.1.0")
			} else {
				assertNoDependency(t, got)
			}
		})
	}
}

func TestBotAgentsHandlerCreateHonorsEnabled(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantEnabled bool
	}{
		{name: "enabled omitted", body: `{"name":"Codex","runtime":"codex"}`, wantEnabled: true},
		{name: "enabled false held for preflight", body: `{"name":"Codex","runtime":"codex","enabled":false}`, wantEnabled: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			queries := &botAgentsQueries{}
			handler := newBotAgentsTestHandler(queries,
				dependencyDriver{runtimeType: botagents.RuntimeCodex, depID: "codex", version: "0.151.0"},
			)

			ctx, rec := botAgentsRequest(t, http.MethodPost, "/bots/"+botAgentsTestBotID+"/agents", tc.body)
			ctx.SetParamNames("bot_id")
			ctx.SetParamValues(botAgentsTestBotID)
			if err := handler.Create(ctx); err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusCreated, rec.Body.String())
			}
			if queries.createParams == nil {
				t.Fatal("CreateBotAgent was not called")
			}
			if queries.createParams.Enabled != tc.wantEnabled {
				t.Fatalf("persisted enabled = %v, want %v", queries.createParams.Enabled, tc.wantEnabled)
			}
			got := decodeBotAgent(t, rec.Body.Bytes())
			if got["enabled"] != tc.wantEnabled {
				t.Fatalf("response enabled = %#v, want %v", got["enabled"], tc.wantEnabled)
			}
			assertDependency(t, got, "codex", "0.151.0")
		})
	}
}

func TestBotAgentsHandlerUpdateReportsRuntimeDependency(t *testing.T) {
	queries := &botAgentsQueries{rows: []sqlc.BotAgent{
		botAgentRow(botAgentsTestCodexID, botagents.RuntimeCodex, false),
	}}
	handler := newBotAgentsTestHandler(queries,
		dependencyDriver{runtimeType: botagents.RuntimeCodex, depID: "codex", version: "0.151.0"},
	)

	ctx, rec := botAgentsRequest(t, http.MethodPatch, "/bots/"+botAgentsTestBotID+"/agents/"+botAgentsTestCodexID, `{"enabled":true}`)
	ctx.SetParamNames("bot_id", "id")
	ctx.SetParamValues(botAgentsTestBotID, botAgentsTestCodexID)
	if err := handler.Update(ctx); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	got := decodeBotAgent(t, rec.Body.Bytes())
	if got["enabled"] != true {
		t.Fatalf("response enabled = %#v, want true", got["enabled"])
	}
	assertDependency(t, got, "codex", "0.151.0")
}

// catalogDriver is a direct runtime whose model catalog call fails the way
// drivers report failures: stable agent feedback, or a plain error.
type catalogDriver struct {
	plainDriver
	err error
}

func (d catalogDriver) ModelCatalog(context.Context, string, string) (external.ModelCatalog, error) {
	return external.ModelCatalog{}, d.err
}

func listModelsRequest(t *testing.T) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	ctx, rec := botAgentsRequest(t, http.MethodGet, "/bots/"+botAgentsTestBotID+"/agents/"+botAgentsTestCodexID+"/models", "")
	ctx.SetParamNames("bot_id", "id")
	ctx.SetParamValues(botAgentsTestBotID, botAgentsTestCodexID)
	return ctx, rec
}

func TestBotAgentsHandlerListModelsPassesThroughRuntimeFeedback(t *testing.T) {
	feedback := agentfeedback.New(
		agentfeedback.CodeAgentDependencyMissing,
		"no codex launcher in workspace",
		http.StatusConflict,
		"chat.externalAgent.dependencyMissing",
		"Codex 0.151.0 is not installed in the workspace",
		map[string]string{"dep_id": "codex", "required_version": "0.151.0", "install_task_id": "task-1"},
	)
	queries := &botAgentsQueries{rows: []sqlc.BotAgent{
		botAgentRow(botAgentsTestCodexID, botagents.RuntimeCodex, true),
	}}
	handler := newBotAgentsTestHandler(queries, catalogDriver{
		plainDriver: plainDriver{runtimeType: botagents.RuntimeCodex},
		err:         feedback,
	})

	ctx, rec := listModelsRequest(t)
	err := handler.ListModels(ctx)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusConflict {
		t.Fatalf("ListModels() error = %#v, want HTTP %d carrying the feedback", err, http.StatusConflict)
	}
	if httpErr.Message != feedback {
		t.Fatalf("ListModels() message = %#v, want the driver feedback passed through", httpErr.Message)
	}

	ctx.Echo().DefaultHTTPErrorHandler(err, ctx)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	var got struct {
		Code string            `json:"code"`
		Args map[string]string `json:"args"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response %s: %v", rec.Body.String(), err)
	}
	if got.Code != agentfeedback.CodeAgentDependencyMissing {
		t.Fatalf("code = %q, want %q: %s", got.Code, agentfeedback.CodeAgentDependencyMissing, rec.Body.String())
	}
	for key, want := range map[string]string{"dep_id": "codex", "required_version": "0.151.0", "install_task_id": "task-1"} {
		if got.Args[key] != want {
			t.Fatalf("args[%s] = %q, want %q: %s", key, got.Args[key], want, rec.Body.String())
		}
	}
}

func TestBotAgentsHandlerListModelsWrapsPlainRuntimeErrors(t *testing.T) {
	queries := &botAgentsQueries{rows: []sqlc.BotAgent{
		botAgentRow(botAgentsTestCodexID, botagents.RuntimeCodex, true),
	}}
	handler := newBotAgentsTestHandler(queries, catalogDriver{
		plainDriver: plainDriver{runtimeType: botagents.RuntimeCodex},
		err:         errors.New("app-server exited before the handshake"),
	})

	ctx, _ := listModelsRequest(t)
	err := handler.ListModels(ctx)
	problem, ok := apperror.ProblemFrom(err, "")
	if !ok || problem.Code != string(apperror.CodeExternalRuntimeUnavailable) || problem.Status != http.StatusServiceUnavailable {
		t.Fatalf("ListModels() error = %v, want %d %s", err, http.StatusServiceUnavailable, apperror.CodeExternalRuntimeUnavailable)
	}
}
