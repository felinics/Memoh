package chat

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	usagepersistence "github.com/memohai/memoh/domains/agent/chat/usage/persistence"
	botpersistence "github.com/memohai/memoh/domains/api/bot/persistence"
	httpfixture "github.com/memohai/memoh/domains/api/http/internal/test"
	"github.com/memohai/memoh/internal/apperror"
)

type tokenUsageBotQueries struct {
	bot botpersistence.Record
}

type tokenUsageReader struct {
	usagepersistence.Reader
	listCalled  bool
	usageCalled bool
	modelFilter usagepersistence.Filter
	listFilter  usagepersistence.Filter
	pagination  usagepersistence.Pagination
	usageFilter usagepersistence.Filter
	usageRows   []usagepersistence.Daily
}

func (q *tokenUsageReader) ListRecords(_ context.Context, filter usagepersistence.Filter, pagination usagepersistence.Pagination) (usagepersistence.Page, error) {
	q.listCalled = true
	q.listFilter = filter
	q.pagination = pagination
	return usagepersistence.Page{
		Items: []usagepersistence.Record{{
			ID:          "55555555-5555-5555-5555-555555555555",
			SessionID:   "66666666-6666-6666-6666-666666666666",
			SessionType: "acp_agent",
			ModelSlug:   "codex",
			ModelName:   "Codex",
		}},
		Total: 1,
	}, nil
}

func (q *tokenUsageReader) GetDaily(_ context.Context, filter usagepersistence.Filter) ([]usagepersistence.Daily, error) {
	q.usageCalled = true
	q.usageFilter = filter
	return q.usageRows, nil
}

func (q *tokenUsageReader) GetByModel(_ context.Context, filter usagepersistence.Filter) ([]usagepersistence.Model, error) {
	q.modelFilter = filter
	return nil, nil
}

func TestGetTokenUsageSeparatesACPAgentBucket(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	botQueries := &tokenUsageBotQueries{bot: httpfixture.BotRow(botID, map[string]any{})}
	reader := &tokenUsageReader{
		usageRows: []usagepersistence.Daily{
			{
				SessionType:  "acp_agent",
				Day:          time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
				InputTokens:  7,
				OutputTokens: 4,
			},
		},
	}
	handler := NewTokenUsageHandler(
		slog.Default(),
		reader,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(botQueries.bot)),
		httpfixture.NewAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/bots/"+botID+"/token-usage?from=2026-05-01&to=2026-05-02&session_type=acp_agent", nil)
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/token-usage")
	ctx.SetParamNames("bot_id")
	ctx.SetParamValues(botID)

	if err := handler.GetTokenUsage(ctx); err != nil {
		t.Fatalf("GetTokenUsage() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !reader.usageCalled {
		t.Fatalf("expected usage query to run")
	}
	if reader.usageFilter.SessionType != "acp_agent" {
		t.Fatalf("usage session type = %q, want acp_agent", reader.usageFilter.SessionType)
	}
	if reader.modelFilter.SessionType != "acp_agent" {
		t.Fatalf("by-model session type = %q, want acp_agent", reader.modelFilter.SessionType)
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
	botQueries := &tokenUsageBotQueries{bot: httpfixture.BotRow(botID, map[string]any{})}
	reader := &tokenUsageReader{}
	handler := NewTokenUsageHandler(
		slog.Default(),
		reader,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(botQueries.bot)),
		httpfixture.NewAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/bots/"+botID+"/token-usage/records?from=2026-05-01&to=2026-05-02&session_type=acp_agent", nil)
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/token-usage/records")
	ctx.SetParamNames("bot_id")
	ctx.SetParamValues(botID)

	if err := handler.ListTokenUsageRecords(ctx); err != nil {
		t.Fatalf("ListTokenUsageRecords() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !reader.listCalled {
		t.Fatalf("expected list query to run")
	}
	if reader.listFilter.SessionType != "acp_agent" {
		t.Fatalf("list session type = %q, want acp_agent", reader.listFilter.SessionType)
	}
}

func TestListTokenUsageRecordsAllowsDiscussFilter(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	botQueries := &tokenUsageBotQueries{bot: httpfixture.BotRow(botID, map[string]any{})}
	reader := &tokenUsageReader{}
	handler := NewTokenUsageHandler(
		slog.Default(),
		reader,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(botQueries.bot)),
		httpfixture.NewAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/bots/"+botID+"/token-usage/records?from=2026-05-01&to=2026-05-02&session_type=discuss", nil)
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/token-usage/records")
	ctx.SetParamNames("bot_id")
	ctx.SetParamValues(botID)

	if err := handler.ListTokenUsageRecords(ctx); err != nil {
		t.Fatalf("ListTokenUsageRecords() error = %v", err)
	}
	if reader.listFilter.SessionType != "discuss" {
		t.Fatalf("list session type = %q, want discuss", reader.listFilter.SessionType)
	}
}

func TestListTokenUsageRecordsRejectsUnknownSessionType(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	botQueries := &tokenUsageBotQueries{bot: httpfixture.BotRow(botID, map[string]any{})}
	reader := &tokenUsageReader{}
	handler := NewTokenUsageHandler(
		slog.Default(),
		reader,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(botQueries.bot)),
		httpfixture.NewAdminAccountService("admin"),
	)

	e := echo.New()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/bots/"+botID+"/token-usage/records?from=2026-05-01&to=2026-05-02&session_type=conversation", nil)
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/token-usage/records")
	ctx.SetParamNames("bot_id")
	ctx.SetParamValues(botID)

	err := handler.ListTokenUsageRecords(ctx)
	if err == nil {
		t.Fatalf("ListTokenUsageRecords() error = nil, want HTTP 400")
	}
	if apperror.KindOf(err) != apperror.KindInvalid {
		t.Fatalf("ListTokenUsageRecords() error = %v, want KindInvalid", err)
	}
	if reader.listCalled {
		t.Fatalf("usage queries should not run for invalid session_type")
	}
}
