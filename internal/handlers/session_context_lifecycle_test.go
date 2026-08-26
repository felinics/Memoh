package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
	"github.com/memohai/memoh/internal/apperror"
	"github.com/memohai/memoh/internal/bots"
	session "github.com/memohai/memoh/internal/chat/thread"
	"github.com/memohai/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/memohai/memoh/internal/db/store"
)

const (
	lifecycleTestBotID     = "11111111-1111-1111-1111-111111111111"
	lifecycleTestSessionID = "22222222-2222-2222-2222-222222222222"
)

type contextLifecycleQueryStub struct {
	dbstore.Queries
	bot             sqlc.GetBotByIDRow
	session         sqlc.BotSession
	lifecycleRows   []sqlc.ListRecentContextLifecyclesBySessionRow
	lifecycleErr    error
	lifecycleParams []sqlc.ListRecentContextLifecyclesBySessionParams
	legacyRows      []sqlc.ListRecentAssistantMessagesBySessionRow
	legacyErr       error
	unmaterialized  bool
	probeCalls      int
	legacyParams    []sqlc.ListRecentAssistantMessagesBySessionParams
}

func (q *contextLifecycleQueryStub) GetBotByID(_ context.Context, _ pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return q.bot, nil
}

func (q *contextLifecycleQueryStub) GetSessionByID(_ context.Context, _ pgtype.UUID) (sqlc.BotSession, error) {
	return q.session, nil
}

func (q *contextLifecycleQueryStub) ListRecentContextLifecyclesBySession(
	_ context.Context,
	arg sqlc.ListRecentContextLifecyclesBySessionParams,
) ([]sqlc.ListRecentContextLifecyclesBySessionRow, error) {
	q.lifecycleParams = append(q.lifecycleParams, arg)
	return q.lifecycleRows, q.lifecycleErr
}

func (q *contextLifecycleQueryStub) ListRecentAssistantMessagesBySession(
	_ context.Context,
	arg sqlc.ListRecentAssistantMessagesBySessionParams,
) ([]sqlc.ListRecentAssistantMessagesBySessionRow, error) {
	q.legacyParams = append(q.legacyParams, arg)
	return q.legacyRows, q.legacyErr
}

func (q *contextLifecycleQueryStub) HasUnmaterializedContextLifecycleMetadataBySession(context.Context, pgtype.UUID) (bool, error) {
	q.probeCalls++
	return q.unmaterialized, nil
}

func lifecycleSnapshotJSON(t *testing.T, snapshot contextfrag.LifecycleSnapshot) []byte {
	t.Helper()
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal lifecycle snapshot: %v", err)
	}
	return raw
}

func newContextLifecycleTestQueries() *contextLifecycleQueryStub {
	return &contextLifecycleQueryStub{
		bot: testBotRow(lifecycleTestBotID, map[string]any{}),
		session: sqlc.BotSession{
			ID:          testUUID(lifecycleTestSessionID),
			BotID:       testUUID(lifecycleTestBotID),
			Type:        session.TypeChat,
			SessionMode: session.TypeChat,
			RuntimeType: session.RuntimeModel,
		},
	}
}

func newContextLifecycleTestHandler(queries *contextLifecycleQueryStub) *SessionInfoHandler {
	return NewSessionInfoHandler(
		slog.New(slog.DiscardHandler),
		queries,
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
		nil,
		nil,
	)
}

func newContextLifecycleTestContext(t *testing.T, query string) echo.Context {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(
		http.MethodGet,
		"/bots/"+lifecycleTestBotID+"/sessions/"+lifecycleTestSessionID+"/context-lifecycle"+query,
		nil,
	)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/sessions/:session_id/context-lifecycle")
	ctx.SetParamNames("bot_id", "session_id")
	ctx.SetParamValues(lifecycleTestBotID, lifecycleTestSessionID)
	return ctx
}

func TestGetSessionContextLifecycleReturnsFailedRunWithoutAssistantMessage(t *testing.T) {
	t.Parallel()

	const (
		failedRunID          = "33333333-3333-3333-3333-333333333333"
		completedRunID       = "44444444-4444-4444-4444-444444444444"
		assistantMessageID   = "55555555-5555-5555-5555-555555555555"
		budgetErrorCode      = "context.budget_unsatisfied"
		failedFinalInputHash = "failed-before-assistant"
	)
	createdAt := time.Unix(1000, 0).UTC()
	queries := newContextLifecycleTestQueries()
	queries.lifecycleRows = []sqlc.ListRecentContextLifecyclesBySessionRow{
		{
			RunID:     testUUID(failedRunID),
			Status:    "failed_budget",
			ErrorCode: pgtype.Text{String: budgetErrorCode, Valid: true},
			CreatedAt: pgtype.Timestamptz{Time: createdAt.Add(time.Minute), Valid: true},
			Snapshot: lifecycleSnapshotJSON(t, contextfrag.LifecycleSnapshot{
				Version:        1,
				Counts:         contextfrag.ManifestCounts{Fragments: 2, Messages: 1},
				FinalInputHash: failedFinalInputHash,
			}),
		},
		{
			RunID:     testUUID(completedRunID),
			Status:    "completed",
			CreatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true},
			Snapshot: lifecycleSnapshotJSON(t, contextfrag.LifecycleSnapshot{
				Version:            1,
				AssistantMessageID: assistantMessageID,
			}),
		},
	}
	handler := newContextLifecycleTestHandler(queries)
	ctx := newContextLifecycleTestContext(t, "?limit=2")

	if err := handler.GetSessionContextLifecycle(ctx); err != nil {
		t.Fatalf("GetSessionContextLifecycle() error = %v", err)
	}
	if ctx.Response().Status != http.StatusOK {
		t.Fatalf("status = %d, want 200", ctx.Response().Status)
	}
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(ctx.Response().Writer.(*httptest.ResponseRecorder).Body.Bytes(), &topLevel); err != nil {
		t.Fatalf("decode top-level response: %v", err)
	}
	if topLevel["turns"] == nil || topLevel["aggregates"] == nil ||
		topLevel["limit"] == nil || topLevel["has_more"] == nil || topLevel["aggregate_scope"] == nil {
		t.Fatalf("top-level response = %#v, want turns, aggregates, and page coverage", topLevel)
	}
	var response ContextLifecycleResponse
	if err := json.Unmarshal(ctx.Response().Writer.(*httptest.ResponseRecorder).Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(response.Turns))
	}
	failed := response.Turns[0]
	if failed.RunID != failedRunID || failed.Status != "failed_budget" ||
		failed.ErrorCode != budgetErrorCode || failed.AssistantMessageID != "" ||
		failed.Snapshot.Counts.Fragments != 2 || failed.Snapshot.FinalInputHash != failedFinalInputHash {
		t.Fatalf("failed run response = %#v", failed)
	}
	completed := response.Turns[1]
	if completed.RunID != completedRunID || completed.AssistantMessageID != assistantMessageID {
		t.Fatalf("completed run response = %#v, want assistant association", completed)
	}
	if response.Aggregates.Turns != 2 {
		t.Fatalf("aggregate turns = %d, want 2", response.Aggregates.Turns)
	}
	if len(queries.legacyParams) != 0 {
		t.Fatalf("legacy query calls = %#v, want none when run rows exist", queries.legacyParams)
	}
	if queries.probeCalls != 1 {
		t.Fatalf("era probe calls = %d, want exactly one", queries.probeCalls)
	}
	if len(queries.lifecycleParams) != 1 || queries.lifecycleParams[0].MaxCount != 3 {
		t.Fatalf("run query params = %#v, want limit 2", queries.lifecycleParams)
	}
}

func TestLoadContextLifecycleTurnsPrefersRunRowsWithoutAssistantMessage(t *testing.T) {
	t.Parallel()

	runID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	createdAt := time.Unix(1000, 0).UTC()
	queries := &contextLifecycleQueryStub{
		lifecycleRows: []sqlc.ListRecentContextLifecyclesBySessionRow{{
			RunID:     runID,
			Status:    "failed_budget",
			ErrorCode: pgtype.Text{String: "context.budget_unsatisfied", Valid: true},
			CreatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true},
			Snapshot: lifecycleSnapshotJSON(t, contextfrag.LifecycleSnapshot{
				Version:        1,
				Counts:         contextfrag.ManifestCounts{Fragments: 1},
				FinalInputHash: "failed-before-assistant",
			}),
		}},
	}

	load, err := loadContextLifecycleTurns(
		context.Background(),
		queries,
		pgtype.UUID{Bytes: [16]byte{2}, Valid: true},
		7,
	)
	if err != nil {
		t.Fatalf("load context lifecycle turns: %v", err)
	}
	turns := load.Turns
	if len(turns) != 1 {
		t.Fatalf("turns = %d, want failed run without an assistant message", len(turns))
	}
	turn := turns[0]
	if turn.RunID != runID.String() || turn.Status != "failed_budget" ||
		turn.ErrorCode != "context.budget_unsatisfied" || turn.AssistantMessageID != "" ||
		!turn.CreatedAt.Equal(createdAt) {
		t.Fatalf("turn = %#v, want run-keyed failed_budget lifecycle", turn)
	}
	if turn.Snapshot.FinalInputHash != "failed-before-assistant" {
		t.Fatalf("snapshot = %#v, want persisted run snapshot", turn.Snapshot)
	}
	if len(queries.legacyParams) != 0 {
		t.Fatalf("legacy query calls = %#v, want none when run rows exist", queries.legacyParams)
	}
	if queries.probeCalls != 1 {
		t.Fatalf("era probe calls = %d, want exactly one", queries.probeCalls)
	}
	if len(queries.lifecycleParams) != 1 || queries.lifecycleParams[0].MaxCount != 8 {
		t.Fatalf("run query params = %#v, want one probe call with limit+1", queries.lifecycleParams)
	}
}

func TestLoadContextLifecycleTurnsPreservesRunOrderingAndLimit(t *testing.T) {
	t.Parallel()

	rows := make([]sqlc.ListRecentContextLifecyclesBySessionRow, 0, 3)
	for i := byte(1); i <= 3; i++ {
		rows = append(rows, sqlc.ListRecentContextLifecyclesBySessionRow{
			RunID:     pgtype.UUID{Bytes: [16]byte{i}, Valid: true},
			Status:    "completed",
			CreatedAt: pgtype.Timestamptz{Time: time.Unix(int64(100-i), 0).UTC(), Valid: true},
			Snapshot: lifecycleSnapshotJSON(t, contextfrag.LifecycleSnapshot{
				Version: 1,
				Counts:  contextfrag.ManifestCounts{Fragments: int(i)},
			}),
		})
	}
	queries := &contextLifecycleQueryStub{lifecycleRows: rows}

	load, err := loadContextLifecycleTurns(
		context.Background(),
		queries,
		pgtype.UUID{Bytes: [16]byte{9}, Valid: true},
		2,
	)
	if err != nil {
		t.Fatalf("load context lifecycle turns: %v", err)
	}
	turns := load.Turns
	if load.LegacySource || !load.HasMore {
		t.Fatalf("coverage = legacy:%t has_more:%t, want run-keyed page with more rows", load.LegacySource, load.HasMore)
	}
	if len(turns) != 2 || turns[0].Snapshot.Counts.Fragments != 1 || turns[1].Snapshot.Counts.Fragments != 2 {
		t.Fatalf("turns = %#v, want query order bounded to two rows", turns)
	}
	if len(queries.legacyParams) != 0 {
		t.Fatalf("legacy query calls = %#v, want none when run rows exist", queries.legacyParams)
	}
	if queries.probeCalls != 1 {
		t.Fatalf("era probe calls = %d, want exactly one", queries.probeCalls)
	}
}

func TestLoadContextLifecycleTurnsFallsBackOnlyWhenRunRowsDoNotExist(t *testing.T) {
	t.Parallel()

	runID := pgtype.UUID{Bytes: [16]byte{4}, Valid: true}
	createdAt := time.Unix(1000, 0).UTC()
	queries := &contextLifecycleQueryStub{
		legacyRows: []sqlc.ListRecentAssistantMessagesBySessionRow{
			legacyLifecycleRow(t, runID, createdAt, &contextfrag.LifecycleSnapshot{
				Version: 1,
				Counts:  contextfrag.ManifestCounts{Messages: 3},
			}),
		},
	}

	load, err := loadContextLifecycleTurns(
		context.Background(),
		queries,
		pgtype.UUID{Bytes: [16]byte{5}, Valid: true},
		1,
	)
	if err != nil {
		t.Fatalf("load context lifecycle turns: %v", err)
	}
	turns := load.Turns
	if len(turns) != 1 || turns[0].RunID != runID.String() || turns[0].Status != "" ||
		turns[0].ErrorCode != "" || turns[0].AssistantMessageID == "" ||
		turns[0].Snapshot.Counts.Messages != 3 {
		t.Fatalf("turns = %#v, want legacy assistant metadata fallback", turns)
	}
	if len(queries.lifecycleParams) != 1 || len(queries.legacyParams) != 1 {
		t.Fatalf("query calls = run:%d legacy:%d, want one each", len(queries.lifecycleParams), len(queries.legacyParams))
	}
}

func TestLoadContextLifecycleTurnsDoesNotMaskRunQueryFailure(t *testing.T) {
	t.Parallel()

	tests := map[string]*contextLifecycleQueryStub{
		"query": {lifecycleErr: errors.New("run store unavailable")},
		"decode": {
			lifecycleRows: []sqlc.ListRecentContextLifecyclesBySessionRow{{
				RunID:    pgtype.UUID{Bytes: [16]byte{6}, Valid: true},
				Snapshot: []byte(`{"version":"invalid"}`),
			}},
		},
	}
	for name, queries := range tests {
		queries := queries
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := loadContextLifecycleTurns(
				context.Background(),
				queries,
				pgtype.UUID{Bytes: [16]byte{7}, Valid: true},
				1,
			)
			if err == nil {
				t.Fatal("expected run-table failure")
			}
			if len(queries.legacyParams) != 0 {
				t.Fatalf("legacy query calls = %d, want no fallback", len(queries.legacyParams))
			}
		})
	}
}

func TestLegacyLifecycleTurnsFromRowsFiltersAndOrders(t *testing.T) {
	t.Parallel()

	base := time.Unix(1000, 0).UTC()
	rows := []sqlc.ListRecentAssistantMessagesBySessionRow{
		legacyLifecycleRow(t, pgtype.UUID{Bytes: [16]byte{3}, Valid: true}, base.Add(3*time.Minute), &contextfrag.LifecycleSnapshot{
			Version: 1,
			Counts:  contextfrag.ManifestCounts{Fragments: 2},
		}),
		legacyLifecycleRow(t, pgtype.UUID{Bytes: [16]byte{2}, Valid: true}, base.Add(2*time.Minute), nil),
		legacyLifecycleRow(t, pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, base.Add(time.Minute), &contextfrag.LifecycleSnapshot{
			Version: 1,
			Counts:  contextfrag.ManifestCounts{Fragments: 1},
		}),
	}

	turns := legacyLifecycleTurnsFromRows(rows, 10)
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want rows with lifecycle snapshots only", len(turns))
	}
	if turns[0].Snapshot.Counts.Fragments != 2 || turns[1].Snapshot.Counts.Fragments != 1 {
		t.Fatalf("turns must preserve newest-first query order: %#v", turns)
	}
	limited := legacyLifecycleTurnsFromRows(rows, 1)
	if len(limited) != 1 || limited[0].Snapshot.Counts.Fragments != 2 {
		t.Fatalf("limit must keep the newest lifecycle turn: %#v", limited)
	}
}

func TestLegacyLifecycleTurnsFromRowsSupportsLegacyAndMemoryOnlySnapshots(t *testing.T) {
	t.Parallel()

	base := time.Unix(1000, 0).UTC()
	rows := []sqlc.ListRecentAssistantMessagesBySessionRow{
		legacyLifecycleRow(t, pgtype.UUID{Bytes: [16]byte{2}, Valid: true}, base.Add(time.Minute), &contextfrag.LifecycleSnapshot{
			Version: 1,
			MemoryRecall: &contextfrag.MemoryRecallTrace{
				ProviderID: "provider-1",
				CacheState: "miss",
				Result: contextfrag.MemoryRecallResultTrace{
					Count: 1,
					Refs:  []string{"memory-1"},
				},
			},
		}),
		legacyLifecycleRow(t, pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, base, &contextfrag.LifecycleSnapshot{
			Version:        1,
			FinalInputHash: "legacy-snapshot",
		}),
	}

	turns := legacyLifecycleTurnsFromRows(rows, 10)
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want memory and legacy snapshots", len(turns))
	}
	if turns[0].Snapshot.MemoryRecall == nil || turns[0].Snapshot.MemoryRecall.ProviderID != "provider-1" ||
		turns[0].Snapshot.MemoryRecall.Result.Count != 1 {
		t.Fatalf("memory-only snapshot = %#v", turns[0].Snapshot)
	}
	if turns[1].Snapshot.MemoryRecall != nil || turns[1].Snapshot.FinalInputHash != "legacy-snapshot" {
		t.Fatalf("legacy snapshot changed compatibility semantics: %#v", turns[1].Snapshot)
	}
}

func TestAggregateContextLifecycle(t *testing.T) {
	t.Parallel()

	turns := []ContextLifecycleTurn{
		{Snapshot: contextfrag.LifecycleSnapshot{
			CacheReadTokens:  100,
			CacheWriteTokens: 10,
			CacheComparison:  &contextfrag.CacheComparison{Outcome: contextfrag.CacheOutcomeHit},
			Selection:        contextfrag.SelectionTrace{DropReasons: map[string]int{"can_drop": 3}},
			Mutations: []contextfrag.MutationRecord{
				{Kind: contextfrag.MutationBeforeModelCallHook},
				{Kind: contextfrag.MutationMidTaskPrune},
			},
		}},
		{Snapshot: contextfrag.LifecycleSnapshot{
			CacheReadTokens: 0,
			CacheComparison: &contextfrag.CacheComparison{Outcome: contextfrag.CacheOutcomeMissSamePrefix},
			Selection:       contextfrag.SelectionTrace{DropReasons: map[string]int{"can_drop": 1, "trust_gate:external_in_system_slot": 1}},
		}},
		{Snapshot: contextfrag.LifecycleSnapshot{
			CacheComparison: &contextfrag.CacheComparison{Outcome: contextfrag.CacheOutcomeFirstObservation},
		}},
	}

	agg := aggregateContextLifecycle(turns)
	if agg.Turns != 3 {
		t.Fatalf("turns = %d, want 3", agg.Turns)
	}
	if agg.TotalCacheReadTokens != 100 || agg.TotalCacheWriteTokens != 10 {
		t.Fatalf("cache totals = %d/%d", agg.TotalCacheReadTokens, agg.TotalCacheWriteTokens)
	}
	if agg.DropReasons["can_drop"] != 4 || agg.DropReasons["trust_gate:external_in_system_slot"] != 1 {
		t.Fatalf("drop reasons = %#v", agg.DropReasons)
	}
	if agg.MutationKinds["before_model_call_hook"] != 1 || agg.MutationKinds["mid_task_prune"] != 1 {
		t.Fatalf("mutation kinds = %#v", agg.MutationKinds)
	}
}

func TestGetSessionContextLifecycleMapsLoadFailureTo500(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		configure       func(*contextLifecycleQueryStub)
		wantLegacyCalls int
		wantCause       string
	}{
		"run query": {
			configure: func(queries *contextLifecycleQueryStub) {
				queries.lifecycleErr = errors.New("private database detail")
			},
			wantCause: "private database detail",
		},
		"run snapshot decode": {
			configure: func(queries *contextLifecycleQueryStub) {
				queries.lifecycleRows = []sqlc.ListRecentContextLifecyclesBySessionRow{{
					RunID:    pgtype.UUID{Bytes: [16]byte{8}, Valid: true},
					Snapshot: []byte(`{"version":"invalid"}`),
				}}
			},
			wantCause: "decode lifecycle snapshot",
		},
		"legacy query": {
			configure: func(queries *contextLifecycleQueryStub) {
				queries.legacyErr = errors.New("private legacy database detail")
			},
			wantLegacyCalls: 1,
			wantCause:       "private legacy database detail",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			queries := newContextLifecycleTestQueries()
			test.configure(queries)
			err := newContextLifecycleTestHandler(queries).GetSessionContextLifecycle(newContextLifecycleTestContext(t, ""))
			problem, ok := apperror.ProblemFrom(err, "request-1")
			if !ok || problem.Code != string(apperror.CodeContextLifecycleLoadFailed) || problem.Status != http.StatusInternalServerError {
				t.Fatalf("error = %#v, want context lifecycle load Problem", err)
			}
			cause := apperror.CauseOf(err)
			if cause == nil || !strings.Contains(cause.Error(), test.wantCause) {
				t.Fatalf("cause = %v, want private cause containing %q", cause, test.wantCause)
			}
			if strings.Contains(problem.Detail, test.wantCause) {
				t.Fatalf("problem detail exposed private cause: %#v", problem)
			}
			if len(queries.legacyParams) != test.wantLegacyCalls {
				t.Fatalf("legacy query calls = %d, want %d", len(queries.legacyParams), test.wantLegacyCalls)
			}
		})
	}
}

func TestContextLifecycleLimitAndRoute(t *testing.T) {
	t.Parallel()

	tests := map[string]int{
		"":           50,
		"?limit=0":   50,
		"?limit=-1":  50,
		"?limit=bad": 50,
		"?limit=1":   1,
		"?limit=201": 200,
	}
	for query, want := range tests {
		if got := contextLifecycleLimit(newContextLifecycleTestContext(t, query)); got != want {
			t.Fatalf("contextLifecycleLimit(%q) = %d, want %d", query, got, want)
		}
	}

	e := echo.New()
	(&SessionInfoHandler{}).Register(e)
	for _, route := range e.Routes() {
		if route.Method == http.MethodGet && route.Path == "/bots/:bot_id/sessions/:session_id/context-lifecycle" {
			return
		}
	}
	t.Fatal("context lifecycle GET route was not registered")
}

func legacyLifecycleRow(
	t *testing.T,
	runID pgtype.UUID,
	at time.Time,
	snapshot *contextfrag.LifecycleSnapshot,
) sqlc.ListRecentAssistantMessagesBySessionRow {
	t.Helper()
	metadata := map[string]any{}
	if snapshot != nil {
		metadata[contextfrag.MetadataContextLifecycleKey] = snapshot
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	return sqlc.ListRecentAssistantMessagesBySessionRow{
		ID:        pgtype.UUID{Bytes: [16]byte{byte(at.Unix() % 256)}, Valid: true}, //nolint:gosec // test fixture
		RunID:     runID,
		Role:      "assistant",
		Metadata:  raw,
		CreatedAt: pgtype.Timestamptz{Time: at, Valid: true},
	}
}
