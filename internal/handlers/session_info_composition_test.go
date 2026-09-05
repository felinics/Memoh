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
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/settings"
)

func TestContextCompositionBucketsToolDefsByProvider(t *testing.T) {
	t.Parallel()

	latest := contextfrag.LifecycleSnapshot{
		Breakdown: []contextfrag.KindBreakdown{
			{Kind: contextfrag.KindConversationEvent, Fragments: 4, TokenEstimate: 900},
			{Kind: contextfrag.KindSystemPrompt, Fragments: 2, TokenEstimate: 300},
		},
		ToolDefs: []contextfrag.ToolDefAccounting{
			{Provider: "native", Name: "send_message", Bytes: 400, TokenEstimate: 100},
			{Provider: "mcp", Name: "jira_search", Bytes: 1600, TokenEstimate: 400},
			{Provider: "native", Name: "exec", Bytes: 800, TokenEstimate: 200},
		},
	}

	breakdown, buckets, _ := contextComposition(latest)
	if len(breakdown) != 2 || breakdown[0].Kind != contextfrag.KindConversationEvent {
		t.Fatalf("breakdown = %+v, want the snapshot's rows", breakdown)
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

func TestContextCompositionEmpty(t *testing.T) {
	t.Parallel()

	breakdown, buckets, plan := contextComposition(contextfrag.LifecycleSnapshot{})
	if breakdown != nil || buckets != nil || plan != nil {
		t.Fatalf("empty snapshot must produce nil composition, got %+v %+v %+v", breakdown, buckets, plan)
	}
}

func TestContextCompactionInfoDerivation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		enabled   bool
		threshold int
		plan      *contextfrag.ContextBudgetPlan
		want      CompactionInfo
	}{
		{
			name:    "marks derive from the persisted plan window",
			enabled: true,
			plan:    &contextfrag.ContextBudgetPlan{Window: 200000},
			want:    CompactionInfo{Enabled: true, AutoTokens: 100000},
		},
		{
			name:      "configured threshold moves only the auto mark",
			enabled:   true,
			threshold: 90000,
			plan:      &contextfrag.ContextBudgetPlan{Window: 200000},
			want:      CompactionInfo{Enabled: true, AutoTokens: 90000},
		},
		{
			name:      "disabled compaction still reports the mark",
			threshold: 90000,
			plan:      &contextfrag.ContextBudgetPlan{Window: 200000},
			want:      CompactionInfo{AutoTokens: 90000},
		},
		{
			name:    "plan without a window leaves the mark unset",
			enabled: true,
			plan:    &contextfrag.ContextBudgetPlan{},
			want:    CompactionInfo{Enabled: true},
		},
		{
			name:    "no plan leaves the mark unset instead of guessing from the model window",
			enabled: true,
			want:    CompactionInfo{Enabled: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := contextCompactionInfo(tc.enabled, tc.threshold, tc.plan); got != tc.want {
				t.Fatalf("contextCompactionInfo() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestBudgetPlanApplies(t *testing.T) {
	t.Parallel()

	window := func(n int64) *int64 { return &n }
	cases := []struct {
		name           string
		model          string
		planWindow     int
		resolvedModel  string
		resolvedWindow *int64
		want           bool
	}{
		{name: "unknown resolved model keeps the plan", model: "gpt-5", want: true},
		{name: "legacy snapshot without a model keeps the plan", resolvedModel: "gpt-5", want: true},
		{name: "same model, any case, applies", model: "GPT-5", resolvedModel: "gpt-5", want: true},
		{name: "a pane override to another model drops the plan", model: "gpt-5", resolvedModel: "claude-opus-4", want: false},
		{name: "same name on a provider with a smaller window drops the plan", model: "gpt-4o", planWindow: 128_000, resolvedModel: "gpt-4o", resolvedWindow: window(32_000), want: false},
		{name: "a plan within the resolved window applies", model: "gpt-4o", planWindow: 120_000, resolvedModel: "gpt-4o", resolvedWindow: window(128_000), want: true},
		{name: "an unknown window falls back to the name", model: "gpt-4o", planWindow: 128_000, resolvedModel: "gpt-4o", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			snapshot := contextfrag.LifecycleSnapshot{Model: tc.model}
			if tc.planWindow > 0 {
				snapshot.BudgetPlan = &contextfrag.ContextBudgetPlan{Window: tc.planWindow}
			}
			if got := budgetPlanApplies(snapshot, tc.resolvedModel, tc.resolvedWindow); got != tc.want {
				t.Fatalf("budgetPlanApplies() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGetSessionInfoOmitsCompactionForACPRuntime(t *testing.T) {
	t.Parallel()

	queries := &sessionInfoQueryStub{
		bot: testBotRow(lifecycleTestBotID, map[string]any{}),
		session: sqlc.BotSession{
			ID:          testUUID(lifecycleTestSessionID),
			BotID:       testUUID(lifecycleTestBotID),
			Type:        session.TypeACPAgent,
			SessionMode: session.TypeChat,
			RuntimeType: session.RuntimeACPAgent,
		},
		lifecycleRows: []sqlc.ListRecentContextLifecyclesBySessionRow{{
			RunID:     testUUID("44444444-4444-4444-4444-444444444444"),
			Status:    "completed",
			CreatedAt: pgtype.Timestamptz{Valid: true},
			Snapshot: lifecycleSnapshotJSON(t, contextfrag.LifecycleSnapshot{
				Version:   1,
				Breakdown: []contextfrag.KindBreakdown{{Kind: contextfrag.KindRuntimeContext, Fragments: 3, TokenEstimate: 900}},
			}),
		}},
		settingsRow: sqlc.GetSettingsByBotIDRow{
			BotID:             testUUID(lifecycleTestBotID),
			Language:          "auto",
			ReasoningEffort:   "medium",
			CompactionEnabled: true,
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
	if len(response.ContextUsage.Breakdown) != 1 {
		t.Fatalf("breakdown = %+v, want the ACP composition", response.ContextUsage.Breakdown)
	}
	if response.ContextUsage.Compaction != nil {
		t.Fatalf("compaction = %+v, want none: Memoh never compacts ACP sessions", response.ContextUsage.Compaction)
	}
}

type sessionInfoQueryStub struct {
	dbstore.Queries
	bot           sqlc.GetBotByIDRow
	session       sqlc.BotSession
	lifecycleRows []sqlc.ListRecentContextLifecyclesBySessionRow
	legacyRows    []sqlc.ListRecentAssistantMessagesBySessionRow
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

func (q *sessionInfoQueryStub) GetLatestContextLifecycleBySession(_ context.Context, _ pgtype.UUID) ([]byte, error) {
	if len(q.lifecycleRows) == 0 {
		return nil, pgx.ErrNoRows
	}
	return q.lifecycleRows[0].Snapshot, nil
}

func (q *sessionInfoQueryStub) ListRecentAssistantMessagesBySession(
	_ context.Context,
	_ sqlc.ListRecentAssistantMessagesBySessionParams,
) ([]sqlc.ListRecentAssistantMessagesBySessionRow, error) {
	return q.legacyRows, nil
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
	if compaction == nil || !compaction.Enabled || compaction.AutoTokens != 90000 {
		t.Fatalf("compaction = %+v, want the enabled mark at 90000", compaction)
	}
	if queries.settingsCalls != 1 {
		t.Fatalf("settings loads = %d, want exactly one per request", queries.settingsCalls)
	}
}

func TestGetSessionInfoFallsBackToLegacyLifecycleMetadata(t *testing.T) {
	t.Parallel()

	metadata, err := json.Marshal(map[string]json.RawMessage{
		contextfrag.MetadataContextLifecycleKey: lifecycleSnapshotJSON(t, contextfrag.LifecycleSnapshot{
			Version:    2,
			Breakdown:  []contextfrag.KindBreakdown{{Kind: contextfrag.KindSystemPrompt, Fragments: 1, TokenEstimate: 300}},
			BudgetPlan: &contextfrag.ContextBudgetPlan{Window: 100000, OutputReserve: 4000},
		}),
	})
	if err != nil {
		t.Fatalf("marshal legacy metadata: %v", err)
	}
	queries := &sessionInfoQueryStub{
		bot: testBotRow(lifecycleTestBotID, map[string]any{}),
		session: sqlc.BotSession{
			ID:          testUUID(lifecycleTestSessionID),
			BotID:       testUUID(lifecycleTestBotID),
			Type:        session.TypeChat,
			SessionMode: session.TypeChat,
			RuntimeType: session.RuntimeModel,
		},
		legacyRows: []sqlc.ListRecentAssistantMessagesBySessionRow{{
			ID:        testUUID("77777777-7777-7777-7777-777777777777"),
			RunID:     testUUID("88888888-8888-8888-8888-888888888888"),
			Role:      "assistant",
			Metadata:  metadata,
			CreatedAt: pgtype.Timestamptz{Valid: true},
		}},
		settingsRow: sqlc.GetSettingsByBotIDRow{
			BotID:             testUUID(lifecycleTestBotID),
			Language:          "auto",
			ReasoningEffort:   "medium",
			CompactionEnabled: true,
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
	var response SessionInfoResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.ContextUsage.Breakdown) != 1 || response.ContextUsage.BudgetPlan == nil || response.ContextUsage.BudgetPlan.Window != 100000 {
		t.Fatalf("context usage = %+v, want the legacy metadata snapshot", response.ContextUsage)
	}
	if response.ContextUsage.Compaction == nil || response.ContextUsage.Compaction.AutoTokens != 50000 {
		t.Fatalf("compaction = %+v, want the mark derived from the legacy plan", response.ContextUsage.Compaction)
	}
}
