package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/bots"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/settings"
)

func TestLatestContextCompositionUsesNewestTurn(t *testing.T) {
	t.Parallel()

	latest := ContextLifecycleTurn{RunID: "r2", Snapshot: contextfrag.LifecycleSnapshot{
		Breakdown: []contextfrag.KindBreakdown{
			{Kind: contextfrag.KindConversationEvent, Fragments: 4, TokenEstimate: 900},
			{Kind: contextfrag.KindSystemPrompt, Fragments: 2, TokenEstimate: 300},
		},
		ToolDefs: []contextfrag.ToolDefAccounting{
			{Provider: "native", Name: "send_message", Bytes: 400, TokenEstimate: 100},
			{Provider: "mcp", Name: "jira_search", Bytes: 1600, TokenEstimate: 400},
			{Provider: "native", Name: "exec", Bytes: 800, TokenEstimate: 200},
		},
	}}
	older := ContextLifecycleTurn{RunID: "r1", Snapshot: contextfrag.LifecycleSnapshot{
		Breakdown: []contextfrag.KindBreakdown{{Kind: contextfrag.KindSystemPrompt, Fragments: 1, TokenEstimate: 1}},
	}}

	breakdown, buckets, _ := latestContextComposition([]ContextLifecycleTurn{latest, older})
	if len(breakdown) != 2 || breakdown[0].Kind != contextfrag.KindConversationEvent {
		t.Fatalf("breakdown = %+v, want latest turn's rows", breakdown)
	}
	want := []ToolDefBucket{
		{Provider: "mcp", Tools: 1, TokenEstimate: 400},
		{Provider: "native", Tools: 2, TokenEstimate: 300},
	}
	if len(buckets) != len(want) {
		t.Fatalf("buckets = %+v, want %+v", buckets, want)
	}
	for i := range want {
		if buckets[i] != want[i] {
			t.Fatalf("buckets[%d] = %+v, want %+v", i, buckets[i], want[i])
		}
	}
}

func TestLatestContextCompositionEmpty(t *testing.T) {
	t.Parallel()

	breakdown, buckets, plan := latestContextComposition(nil)
	if breakdown != nil || buckets != nil || plan != nil {
		t.Fatalf("empty turns must produce nil composition, got %+v %+v %+v", breakdown, buckets, plan)
	}
}

func TestLatestContextCompositionTakesBudgetPlanFromNewestTurn(t *testing.T) {
	t.Parallel()

	newest := &contextfrag.ContextBudgetPlan{Window: 200000, OutputReserve: 8000, ToolDefsCost: 1200}
	turns := []ContextLifecycleTurn{
		{RunID: "r2", Snapshot: contextfrag.LifecycleSnapshot{BudgetPlan: newest}},
		{RunID: "r1", Snapshot: contextfrag.LifecycleSnapshot{
			Breakdown:  []contextfrag.KindBreakdown{{Kind: contextfrag.KindSystemPrompt, Fragments: 1, TokenEstimate: 1}},
			BudgetPlan: &contextfrag.ContextBudgetPlan{Window: 8000},
		}},
	}

	breakdown, buckets, plan := latestContextComposition(turns)
	if breakdown != nil || buckets != nil {
		t.Fatalf("composition = %+v %+v, want the newest turn's empty composition", breakdown, buckets)
	}
	if plan != newest {
		t.Fatalf("budget plan = %+v, want the newest turn's plan %+v", plan, newest)
	}
}

func TestContextCompactionInfoDerivation(t *testing.T) {
	t.Parallel()

	window := int64(80000)
	cases := []struct {
		name          string
		settings      settings.Settings
		plan          *contextfrag.ContextBudgetPlan
		contextWindow *int64
		want          CompactionInfo
	}{
		{
			name:          "budget plan window wins over the resolved model window",
			settings:      settings.Settings{CompactionEnabled: true},
			plan:          &contextfrag.ContextBudgetPlan{Window: 200000},
			contextWindow: &window,
			want:          CompactionInfo{Enabled: true, AutoTokens: 100000, HardTokens: 150000},
		},
		{
			name:          "resolved model window is the fallback denominator",
			settings:      settings.Settings{CompactionEnabled: true},
			plan:          &contextfrag.ContextBudgetPlan{},
			contextWindow: &window,
			want:          CompactionInfo{Enabled: true, AutoTokens: 40000, HardTokens: 60000},
		},
		{
			name:     "configured threshold moves only the auto mark",
			settings: settings.Settings{CompactionEnabled: true, CompactionThreshold: 90000},
			plan:     &contextfrag.ContextBudgetPlan{Window: 200000},
			want:     CompactionInfo{Enabled: true, AutoTokens: 90000, HardTokens: 150000},
		},
		{
			name:     "disabled compaction still reports marks",
			settings: settings.Settings{CompactionThreshold: 90000},
			plan:     &contextfrag.ContextBudgetPlan{Window: 200000},
			want:     CompactionInfo{AutoTokens: 90000, HardTokens: 150000},
		},
		{
			name:     "no budget leaves the marks unset",
			settings: settings.Settings{CompactionEnabled: true},
			want:     CompactionInfo{Enabled: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := contextCompactionInfo(tc.settings, tc.plan, tc.contextWindow); got != tc.want {
				t.Fatalf("contextCompactionInfo() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

type sessionInfoQueryStub struct {
	dbstore.Queries
	bot           sqlc.GetBotByIDRow
	session       sqlc.BotSession
	lifecycleRows []sqlc.ListRecentContextLifecyclesBySessionRow
	settingsRow   sqlc.GetSettingsByBotIDRow
	settingsCalls int
}

func (q *sessionInfoQueryStub) GetBotByID(_ context.Context, _ pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return q.bot, nil
}

func (q *sessionInfoQueryStub) GetSessionByID(_ context.Context, _ pgtype.UUID) (sqlc.BotSession, error) {
	return q.session, nil
}

func (*sessionInfoQueryStub) CountMessagesBySession(_ context.Context, _ pgtype.UUID) (int64, error) {
	return 7, nil
}

func (*sessionInfoQueryStub) GetLatestAssistantUsage(_ context.Context, _ pgtype.UUID) (int64, error) {
	return 123000, nil
}

func (*sessionInfoQueryStub) GetSessionCacheStats(_ context.Context, _ pgtype.UUID) (sqlc.GetSessionCacheStatsRow, error) {
	return sqlc.GetSessionCacheStatsRow{}, nil
}

func (*sessionInfoQueryStub) GetSessionUsedSkills(_ context.Context, _ pgtype.UUID) ([]string, error) {
	return nil, nil
}

func (q *sessionInfoQueryStub) ListRecentContextLifecyclesBySession(
	_ context.Context,
	_ sqlc.ListRecentContextLifecyclesBySessionParams,
) ([]sqlc.ListRecentContextLifecyclesBySessionRow, error) {
	return q.lifecycleRows, nil
}

func (*sessionInfoQueryStub) HasUnmaterializedContextLifecycleMetadataBySession(
	context.Context,
	pgtype.UUID,
) (bool, error) {
	return false, nil
}

func (q *sessionInfoQueryStub) GetSettingsByBotID(_ context.Context, _ pgtype.UUID) (sqlc.GetSettingsByBotIDRow, error) {
	q.settingsCalls++
	return q.settingsRow, nil
}

func TestGetSessionInfoReportsBudgetPlanAndCompactionMarks(t *testing.T) {
	t.Parallel()

	queries := &sessionInfoQueryStub{
		bot: testBotRow(lifecycleTestBotID, map[string]any{}),
		session: sqlc.BotSession{
			ID:          testUUID(lifecycleTestSessionID),
			BotID:       testUUID(lifecycleTestBotID),
			Type:        session.TypeChat,
			SessionMode: session.TypeChat,
			RuntimeType: session.RuntimeModel,
		},
		lifecycleRows: []sqlc.ListRecentContextLifecyclesBySessionRow{{
			RunID:     testUUID("33333333-3333-3333-3333-333333333333"),
			Status:    "completed",
			CreatedAt: pgtype.Timestamptz{Valid: true},
			Snapshot: lifecycleSnapshotJSON(t, contextfrag.LifecycleSnapshot{
				Version:    1,
				Breakdown:  []contextfrag.KindBreakdown{{Kind: contextfrag.KindSystemPrompt, Fragments: 1, TokenEstimate: 300}},
				BudgetPlan: &contextfrag.ContextBudgetPlan{Window: 200000, OutputReserve: 8000, ToolDefsCost: 1200},
			}),
		}},
		settingsRow: sqlc.GetSettingsByBotIDRow{
			BotID:               testUUID(lifecycleTestBotID),
			Language:            "auto",
			ReasoningEffort:     "medium",
			CompactionEnabled:   true,
			CompactionThreshold: 90000,
		},
	}
	logger := slog.New(slog.DiscardHandler)
	handler := NewSessionInfoHandler(
		logger,
		queries,
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
		nil,
		settings.NewService(logger, queries, nil, nil),
	)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/bots/"+lifecycleTestBotID+"/sessions/"+lifecycleTestSessionID+"/status", nil)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/sessions/:session_id/status")
	ctx.SetParamNames("bot_id", "session_id")
	ctx.SetParamValues(lifecycleTestBotID, lifecycleTestSessionID)

	if err := handler.GetSessionInfo(ctx); err != nil {
		t.Fatalf("GetSessionInfo() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var response SessionInfoResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	plan := response.ContextUsage.BudgetPlan
	if plan == nil || plan.Window != 200000 || plan.OutputReserve != 8000 || plan.ToolDefsCost != 1200 {
		t.Fatalf("budget plan = %+v, want the persisted plan", plan)
	}
	compaction := response.ContextUsage.Compaction
	if compaction == nil || !compaction.Enabled || compaction.AutoTokens != 90000 || compaction.HardTokens != 150000 {
		t.Fatalf("compaction = %+v, want enabled marks at 90000/150000", compaction)
	}
	if queries.settingsCalls != 1 {
		t.Fatalf("settings loads = %d, want exactly one per request", queries.settingsCalls)
	}
}
