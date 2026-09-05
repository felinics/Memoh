package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/bots"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/settings"
)

// providerCollisionQueries serves two providers that expose the same model
// name with different context windows.
type providerCollisionQueries struct {
	*sessionInfoQueryStub
	models map[pgtype.UUID]sqlc.Model
}

func (q *providerCollisionQueries) GetModelByID(_ context.Context, id pgtype.UUID) (sqlc.Model, error) {
	if model, ok := q.models[id]; ok {
		return model, nil
	}
	return sqlc.Model{}, pgx.ErrNoRows
}

// A pane override to a same-named model on another provider must not keep
// the plan the previous provider's wider window produced.
func TestGetSessionInfoDropsThePlanWhenASameNamedModelHasASmallerWindow(t *testing.T) {
	t.Parallel()

	modelA := testUUID("10000000-0000-0000-0000-000000000001")
	modelB := testUUID("10000000-0000-0000-0000-000000000002")
	providerA := testUUID("20000000-0000-0000-0000-000000000001")
	providerB := testUUID("20000000-0000-0000-0000-000000000002")
	queries := &providerCollisionQueries{
		sessionInfoQueryStub: &sessionInfoQueryStub{
			bot: testBotRow(lifecycleTestBotID, map[string]any{}),
			session: sqlc.BotSession{
				ID: testUUID(lifecycleTestSessionID), BotID: testUUID(lifecycleTestBotID),
				Type: session.TypeChat, SessionMode: session.TypeChat, RuntimeType: session.RuntimeModel,
			},
			lifecycleRows: []sqlc.ListRecentContextLifecyclesBySessionRow{{
				RunID:  testUUID("30000000-0000-0000-0000-000000000001"),
				Status: "completed",
				Snapshot: lifecycleSnapshotJSON(t, contextfrag.LifecycleSnapshot{
					Version: 2, Model: "gpt-4o", ClientType: "openai-completions",
					BudgetPlan: &contextfrag.ContextBudgetPlan{Window: 128000, OutputReserve: 8000},
				}),
			}},
			settingsRow: sqlc.GetSettingsByBotIDRow{
				BotID: testUUID(lifecycleTestBotID), ChatModelID: modelA,
				Language: "auto", ReasoningEffort: "medium",
			},
		},
		models: map[pgtype.UUID]sqlc.Model{
			modelA: {ID: modelA, ModelID: "gpt-4o", ProviderID: providerA, Type: "chat", Enable: true, Config: []byte(`{"context_window":128000}`)},
			modelB: {ID: modelB, ModelID: "gpt-4o", ProviderID: providerB, Type: "chat", Enable: true, Config: []byte(`{"context_window":32000}`)},
		},
	}
	logger := slog.New(slog.DiscardHandler)
	handler := NewSessionInfoHandler(logger, queries, bots.NewService(logger, queries), newTestAdminAccountService("admin"), models.NewService(logger, queries), settings.NewService(logger, queries, nil, nil))
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/bots/"+lifecycleTestBotID+"/sessions/"+lifecycleTestSessionID+"/status?model_id="+modelB.String(), nil)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/sessions/:session_id/status")
	ctx.SetParamNames("bot_id", "session_id")
	ctx.SetParamValues(lifecycleTestBotID, lifecycleTestSessionID)
	if err := handler.GetSessionInfo(ctx); err != nil {
		t.Fatal(err)
	}
	var response SessionInfoResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ContextUsage.ContextWindow == nil || *response.ContextUsage.ContextWindow != 32000 {
		t.Fatalf("override not resolved: %s", rec.Body.String())
	}
	if response.ContextUsage.BudgetPlan != nil {
		t.Fatalf("provider switch still applies the old plan: %s → %s resolved window %d, retained plan window %d", providerA.String(), providerB.String(), *response.ContextUsage.ContextWindow, response.ContextUsage.BudgetPlan.Window)
	}
}
