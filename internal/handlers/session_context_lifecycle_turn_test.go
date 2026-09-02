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
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/settings"
)

const lifecycleTestRunID = "55555555-5555-5555-5555-555555555555"

type lifecycleTurnQueryStub struct {
	sessionInfoQueryStub
	runRow    *sqlc.GetContextLifecycleBySessionAndRunIDRow
	legacyRow *sqlc.GetAssistantContextLifecycleBySessionAndRunIDRow
}

func (q *lifecycleTurnQueryStub) GetContextLifecycleBySessionAndRunID(
	_ context.Context,
	arg sqlc.GetContextLifecycleBySessionAndRunIDParams,
) (sqlc.GetContextLifecycleBySessionAndRunIDRow, error) {
	if q.runRow == nil || q.runRow.RunID != arg.RunID || arg.SessionID != testUUID(lifecycleTestSessionID) {
		return sqlc.GetContextLifecycleBySessionAndRunIDRow{}, pgx.ErrNoRows
	}
	return *q.runRow, nil
}

func (q *lifecycleTurnQueryStub) GetAssistantContextLifecycleBySessionAndRunID(
	_ context.Context,
	arg sqlc.GetAssistantContextLifecycleBySessionAndRunIDParams,
) (sqlc.GetAssistantContextLifecycleBySessionAndRunIDRow, error) {
	if q.legacyRow == nil || q.legacyRow.RunID != arg.RunID || arg.SessionID != testUUID(lifecycleTestSessionID) {
		return sqlc.GetAssistantContextLifecycleBySessionAndRunIDRow{}, pgx.ErrNoRows
	}
	return *q.legacyRow, nil
}

func newLifecycleTurnStub() *lifecycleTurnQueryStub {
	return &lifecycleTurnQueryStub{sessionInfoQueryStub: sessionInfoQueryStub{
		bot: testBotRow(lifecycleTestBotID, map[string]any{}),
		session: sqlc.BotSession{
			ID:          testUUID(lifecycleTestSessionID),
			BotID:       testUUID(lifecycleTestBotID),
			Type:        session.TypeChat,
			SessionMode: session.TypeChat,
			RuntimeType: session.RuntimeModel,
		},
	}}
}

func decisionsSnapshot(t *testing.T) contextfrag.LifecycleSnapshot {
	t.Helper()
	return contextfrag.LifecycleSnapshot{
		Version:   2,
		Breakdown: []contextfrag.KindBreakdown{{Kind: contextfrag.KindConversationEvent, Fragments: 2, TokenEstimate: 340}},
		SelectionDecisions: []contextfrag.SelectionDecision{
			{ID: "message.001", Decision: contextfrag.DecisionSelected, TokenEstimate: 170},
			{ID: "message.002", Decision: contextfrag.DecisionDropped, Reason: "history_budget", TokenEstimate: 170},
		},
	}
}

func callLifecycleTurn(t *testing.T, queries *lifecycleTurnQueryStub, runID string) (*httptest.ResponseRecorder, error) {
	t.Helper()
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
	req := httptest.NewRequest(http.MethodGet, "/bots/"+lifecycleTestBotID+"/sessions/"+lifecycleTestSessionID+"/context-lifecycle/"+runID, nil)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "user-1")
	ctx.SetPath("/bots/:bot_id/sessions/:session_id/context-lifecycle/:run_id")
	ctx.SetParamNames("bot_id", "session_id", "run_id")
	ctx.SetParamValues(lifecycleTestBotID, lifecycleTestSessionID, runID)
	return rec, handler.GetSessionContextLifecycleTurn(ctx)
}

func TestGetSessionContextLifecycleTurnReturnsFullSnapshot(t *testing.T) {
	t.Parallel()

	queries := newLifecycleTurnStub()
	queries.runRow = &sqlc.GetContextLifecycleBySessionAndRunIDRow{
		RunID:     testUUID(lifecycleTestRunID),
		Status:    "completed",
		CreatedAt: pgtype.Timestamptz{Valid: true},
		Snapshot:  lifecycleSnapshotJSON(t, decisionsSnapshot(t)),
	}

	rec, err := callLifecycleTurn(t, queries, lifecycleTestRunID)
	if err != nil {
		t.Fatalf("GetSessionContextLifecycleTurn() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var turn ContextLifecycleTurn
	if err := json.Unmarshal(rec.Body.Bytes(), &turn); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if turn.RunID != lifecycleTestRunID || turn.Status != "completed" {
		t.Fatalf("turn = %+v, want run %s completed", turn, lifecycleTestRunID)
	}
	if len(turn.Snapshot.SelectionDecisions) != 2 || turn.Snapshot.SelectionDecisions[1].Reason != "history_budget" {
		t.Fatalf("decisions = %+v, want the persisted selection decisions", turn.Snapshot.SelectionDecisions)
	}
}

func TestGetSessionContextLifecycleTurnFallsBackToLegacyMetadata(t *testing.T) {
	t.Parallel()

	queries := newLifecycleTurnStub()
	metadata, err := json.Marshal(map[string]json.RawMessage{"context_lifecycle": lifecycleSnapshotJSON(t, decisionsSnapshot(t))})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	queries.legacyRow = &sqlc.GetAssistantContextLifecycleBySessionAndRunIDRow{
		ID:        testUUID("66666666-6666-6666-6666-666666666666"),
		RunID:     testUUID(lifecycleTestRunID),
		Metadata:  metadata,
		CreatedAt: pgtype.Timestamptz{Valid: true},
	}

	rec, err := callLifecycleTurn(t, queries, lifecycleTestRunID)
	if err != nil {
		t.Fatalf("GetSessionContextLifecycleTurn() error = %v", err)
	}
	var turn ContextLifecycleTurn
	if err := json.Unmarshal(rec.Body.Bytes(), &turn); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if turn.AssistantMessageID != "66666666-6666-6666-6666-666666666666" || len(turn.Snapshot.SelectionDecisions) != 2 {
		t.Fatalf("turn = %+v, want the legacy assistant snapshot with its decisions", turn)
	}
}

func TestGetSessionContextLifecycleTurnRejectsUnknownAndInvalidRuns(t *testing.T) {
	t.Parallel()

	if _, err := callLifecycleTurn(t, newLifecycleTurnStub(), lifecycleTestRunID); apperror.CodeOf(err) != apperror.CodeContextLifecycleNotFound {
		t.Fatalf("unknown run error = %v, want %s", err, apperror.CodeContextLifecycleNotFound)
	}
	if _, err := callLifecycleTurn(t, newLifecycleTurnStub(), "not-a-uuid"); apperror.CodeOf(err) != apperror.CodeContextLifecycleRequestInvalid {
		t.Fatalf("invalid run error = %v, want %s", err, apperror.CodeContextLifecycleRequestInvalid)
	}
}
