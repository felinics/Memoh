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

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
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
		failedRunID        = "33333333-3333-3333-3333-333333333333"
		completedRunID     = "44444444-4444-4444-4444-444444444444"
		assistantMessageID = "55555555-5555-5555-5555-555555555555"
	)
	createdAt := time.Unix(1000, 0).UTC()
	queries := newContextLifecycleTestQueries()
	queries.lifecycleRows = []sqlc.ListRecentContextLifecyclesBySessionRow{
		{
			RunID:     testUUID(failedRunID),
			Status:    "failed_provider",
			ErrorCode: pgtype.Text{String: "workspace.unreachable", Valid: true},
			CreatedAt: pgtype.Timestamptz{Time: createdAt.Add(time.Minute), Valid: true},
			Snapshot: lifecycleSnapshotJSON(t, contextfrag.LifecycleSnapshot{
				Version: 1,
				Counts:  contextfrag.ManifestCounts{Fragments: 2, Messages: 1},
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
	if len(topLevel) != 1 || topLevel["turns"] == nil {
		t.Fatalf("top-level response = %#v, want only turns", topLevel)
	}
	var response ContextLifecycleResponse
	if err := json.Unmarshal(ctx.Response().Writer.(*httptest.ResponseRecorder).Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(response.Turns))
	}
	failed := response.Turns[0]
	if failed.RunID != failedRunID || failed.Status != "failed_provider" ||
		failed.ErrorCode != "workspace.unreachable" || failed.AssistantMessageID != "" ||
		failed.Snapshot.Counts.Fragments != 2 {
		t.Fatalf("failed run response = %#v", failed)
	}
	completed := response.Turns[1]
	if completed.RunID != completedRunID || completed.AssistantMessageID != assistantMessageID {
		t.Fatalf("completed run response = %#v, want assistant association", completed)
	}
	if len(queries.legacyParams) != 0 {
		t.Fatalf("legacy query calls = %d, want 0", len(queries.legacyParams))
	}
	if len(queries.lifecycleParams) != 1 || queries.lifecycleParams[0].MaxCount != 2 {
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
			Status:    "failed_provider",
			ErrorCode: pgtype.Text{String: "workspace.unreachable", Valid: true},
			CreatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true},
			Snapshot: lifecycleSnapshotJSON(t, contextfrag.LifecycleSnapshot{
				Version: 1,
				Counts:  contextfrag.ManifestCounts{Fragments: 1},
			}),
		}},
	}

	turns, err := loadContextLifecycleTurns(
		context.Background(),
		queries,
		pgtype.UUID{Bytes: [16]byte{2}, Valid: true},
		7,
	)
	if err != nil {
		t.Fatalf("load context lifecycle turns: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("turns = %d, want failed run without an assistant message", len(turns))
	}
	turn := turns[0]
	if turn.RunID != runID.String() || turn.Status != "failed_provider" ||
		turn.ErrorCode != "workspace.unreachable" || turn.AssistantMessageID != "" ||
		!turn.CreatedAt.Equal(createdAt) {
		t.Fatalf("turn = %#v, want run-keyed failed_provider lifecycle", turn)
	}
	if len(queries.legacyParams) != 0 {
		t.Fatalf("legacy query calls = %d, want 0 when run rows exist", len(queries.legacyParams))
	}
	if len(queries.lifecycleParams) != 1 || queries.lifecycleParams[0].MaxCount != 7 {
		t.Fatalf("run query params = %#v, want one call with limit 7", queries.lifecycleParams)
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

	turns, err := loadContextLifecycleTurns(
		context.Background(),
		queries,
		pgtype.UUID{Bytes: [16]byte{9}, Valid: true},
		2,
	)
	if err != nil {
		t.Fatalf("load context lifecycle turns: %v", err)
	}
	if len(turns) != 2 || turns[0].Snapshot.Counts.Fragments != 1 || turns[1].Snapshot.Counts.Fragments != 2 {
		t.Fatalf("turns = %#v, want query order bounded to two rows", turns)
	}
	if len(queries.legacyParams) != 0 {
		t.Fatalf("legacy query calls = %d, want 0 when run rows exist", len(queries.legacyParams))
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

	turns, err := loadContextLifecycleTurns(
		context.Background(),
		queries,
		pgtype.UUID{Bytes: [16]byte{5}, Valid: true},
		1,
	)
	if err != nil {
		t.Fatalf("load context lifecycle turns: %v", err)
	}
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
