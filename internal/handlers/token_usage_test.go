package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type tokenUsageQueries struct {
	dbstore.Queries
	bot         sqlc.GetBotByIDRow
	listCalled  bool
	countCalled bool
	usageCalled bool
	modelParams sqlc.GetTokenUsageByModelParams
	listParams  sqlc.ListTokenUsageRecordsParams
	countParams sqlc.CountTokenUsageRecordsParams
	usageParams sqlc.GetTokenUsageByDayAndTypeParams
	usageRows   []sqlc.GetTokenUsageByDayAndTypeRow
}

func (q *tokenUsageQueries) GetBotByID(_ context.Context, _ pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return q.bot, nil
}

func (q *tokenUsageQueries) ListTokenUsageRecords(_ context.Context, arg sqlc.ListTokenUsageRecordsParams) ([]sqlc.ListTokenUsageRecordsRow, error) {
	q.listCalled = true
	q.listParams = arg
	return []sqlc.ListTokenUsageRecordsRow{
		{
			ID:          testUUID("55555555-5555-5555-5555-555555555555"),
			SessionID:   testUUID("66666666-6666-6666-6666-666666666666"),
			SessionType: "acp_agent",
			ModelSlug:   "codex",
			ModelName:   "Codex",
		},
	}, nil
}

func (q *tokenUsageQueries) CountTokenUsageRecords(_ context.Context, arg sqlc.CountTokenUsageRecordsParams) (int64, error) {
	q.countCalled = true
	q.countParams = arg
	return 1, nil
}

func (q *tokenUsageQueries) GetTokenUsageByDayAndType(_ context.Context, arg sqlc.GetTokenUsageByDayAndTypeParams) ([]sqlc.GetTokenUsageByDayAndTypeRow, error) {
	q.usageCalled = true
	q.usageParams = arg
	return q.usageRows, nil
}

func (q *tokenUsageQueries) GetTokenUsageByModel(_ context.Context, arg sqlc.GetTokenUsageByModelParams) ([]sqlc.GetTokenUsageByModelRow, error) {
	q.modelParams = arg
	return nil, nil
}

func TestGetTokenUsageSeparatesACPAgentBucket(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := &tokenUsageQueries{
		bot: testBotRow(botID, map[string]any{}),
		usageRows: []sqlc.GetTokenUsageByDayAndTypeRow{
			{
				SessionType:  "acp_agent",
				Day:          pgtype.Date{Time: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), Valid: true},
				InputTokens:  7,
				OutputTokens: 4,
			},
		},
	}
	handler := NewTokenUsageHandler(
		slog.Default(),
		queries,
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/bots/"+botID+"/token-usage?from=2026-05-01&to=2026-05-02&session_type=acp_agent", nil)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/token-usage")
	ctx.SetParamNames("bot_id")
	ctx.SetParamValues(botID)

	if err := handler.GetTokenUsage(ctx); err != nil {
		t.Fatalf("GetTokenUsage() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !queries.usageCalled {
		t.Fatalf("expected usage query to run")
	}
	if !queries.usageParams.SessionType.Valid || queries.usageParams.SessionType.String != "acp_agent" {
		t.Fatalf("usage session type = %#v, want acp_agent", queries.usageParams.SessionType)
	}
	if !queries.modelParams.SessionType.Valid || queries.modelParams.SessionType.String != "acp_agent" {
		t.Fatalf("by-model session type = %#v, want acp_agent", queries.modelParams.SessionType)
	}
	var resp TokenUsageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Chat) != 0 {
		t.Fatalf("chat usage = %+v, want empty when SQL returns ACP runtime usage", resp.Chat)
	}
	if len(resp.ACPAgent) != 1 || resp.ACPAgent[0].InputTokens != 7 || resp.ACPAgent[0].OutputTokens != 4 {
		t.Fatalf("acp_agent usage = %+v, want ACP runtime totals", resp.ACPAgent)
	}
}

func TestListTokenUsageRecordsAllowsACPAgentFilter(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := &tokenUsageQueries{bot: testBotRow(botID, map[string]any{})}
	handler := NewTokenUsageHandler(
		slog.Default(),
		queries,
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/bots/"+botID+"/token-usage/records?from=2026-05-01&to=2026-05-02&session_type=acp_agent", nil)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/token-usage/records")
	ctx.SetParamNames("bot_id")
	ctx.SetParamValues(botID)

	if err := handler.ListTokenUsageRecords(ctx); err != nil {
		t.Fatalf("ListTokenUsageRecords() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !queries.listCalled || !queries.countCalled {
		t.Fatalf("expected list and count queries to run")
	}
	if !queries.listParams.SessionType.Valid || queries.listParams.SessionType.String != "acp_agent" {
		t.Fatalf("list session type = %#v, want acp_agent", queries.listParams.SessionType)
	}
	if !queries.countParams.SessionType.Valid || queries.countParams.SessionType.String != "acp_agent" {
		t.Fatalf("count session type = %#v, want acp_agent", queries.countParams.SessionType)
	}
}

func TestListTokenUsageRecordsAllowsDiscussFilter(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := &tokenUsageQueries{bot: testBotRow(botID, map[string]any{})}
	handler := NewTokenUsageHandler(
		slog.Default(),
		queries,
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/bots/"+botID+"/token-usage/records?from=2026-05-01&to=2026-05-02&session_type=discuss", nil)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/token-usage/records")
	ctx.SetParamNames("bot_id")
	ctx.SetParamValues(botID)

	if err := handler.ListTokenUsageRecords(ctx); err != nil {
		t.Fatalf("ListTokenUsageRecords() error = %v", err)
	}
	if !queries.listParams.SessionType.Valid || queries.listParams.SessionType.String != "discuss" {
		t.Fatalf("list session type = %#v, want discuss", queries.listParams.SessionType)
	}
}

func TestListTokenUsageRecordsRejectsUnknownSessionType(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := &tokenUsageQueries{bot: testBotRow(botID, map[string]any{})}
	handler := NewTokenUsageHandler(
		slog.Default(),
		queries,
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/bots/"+botID+"/token-usage/records?from=2026-05-01&to=2026-05-02&session_type=conversation", nil)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/token-usage/records")
	ctx.SetParamNames("bot_id")
	ctx.SetParamValues(botID)

	err := handler.ListTokenUsageRecords(ctx)
	if err == nil {
		t.Fatalf("ListTokenUsageRecords() error = nil, want HTTP 400")
	}
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusBadRequest {
		t.Fatalf("ListTokenUsageRecords() error = %v, want HTTP 400", err)
	}
	if queries.listCalled || queries.countCalled {
		t.Fatalf("usage queries should not run for invalid session_type")
	}
}
