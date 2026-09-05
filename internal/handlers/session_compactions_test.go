package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

type sessionCompactionStub struct {
	*contextLifecycleAccessStub
	rows         []sqlc.BotHistoryMessageCompact
	params       []sqlc.ListCompactionLogsBySessionParams
	beforeParams []sqlc.ListCompactionLogsBySessionBeforeParams
}

func compactionRows(count int) []sqlc.BotHistoryMessageCompact {
	started := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	rows := make([]sqlc.BotHistoryMessageCompact, 0, count)
	for i := range count {
		at := started.Add(-time.Duration(i) * time.Hour)
		rows = append(rows, sqlc.BotHistoryMessageCompact{
			ID:            pgtype.UUID{Bytes: [16]byte{9, byte(i + 1)}, Valid: true},
			BotID:         testUUID(lifecycleTestBotID),
			SessionID:     testUUID(lifecycleTestSessionID),
			Status:        "ok",
			Summary:       "The user asked for a listing; the agent listed the temp dir.",
			MessageCount:  12,
			Usage:         []byte(`{"inputTokens":4100,"outputTokens":320}`),
			AnchorStartMs: at.Add(-time.Hour).UnixMilli(),
			AnchorEndMs:   at.Add(-time.Minute).UnixMilli(),
			ArtifactLevel: 1,
			StartedAt:     pgtype.Timestamptz{Time: at, Valid: true},
			CompletedAt:   pgtype.Timestamptz{Time: at.Add(9 * time.Second), Valid: true},
		})
	}
	return rows
}

func (s *sessionCompactionStub) ListCompactionLogsBySession(_ context.Context, arg sqlc.ListCompactionLogsBySessionParams) ([]sqlc.BotHistoryMessageCompact, error) {
	s.params = append(s.params, arg)
	return s.rows[:min(int(arg.Limit), len(s.rows))], nil
}

func (s *sessionCompactionStub) ListCompactionLogsBySessionBefore(_ context.Context, arg sqlc.ListCompactionLogsBySessionBeforeParams) ([]sqlc.BotHistoryMessageCompact, error) {
	s.beforeParams = append(s.beforeParams, arg)
	var older []sqlc.BotHistoryMessageCompact
	for _, row := range s.rows {
		if row.StartedAt.Time.Before(arg.BeforeStartedAt.Time) {
			older = append(older, row)
		}
	}
	return older[:min(int(arg.Limit), len(older))], nil
}

func TestGetSessionCompactionsListsTheSessionsRunsForAReader(t *testing.T) {
	t.Parallel()

	queries := &sessionCompactionStub{contextLifecycleAccessStub: newContextLifecycleAccessStub(t, "chat"), rows: compactionRows(1)}
	handler := newContextLifecycleGranteeHandler(queries)
	ctx := newContextLifecycleGranteeContext(t, "/compactions", false)
	if err := handler.GetSessionCompactions(ctx); err != nil {
		t.Fatalf("GetSessionCompactions: %v", err)
	}
	var response SessionCompactionsResponse
	if err := json.Unmarshal(ctx.Response().Writer.(*httptest.ResponseRecorder).Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ctx.Response().Status != http.StatusOK || len(response.Items) != 1 || response.HasMore || response.NextCursor != "" {
		t.Fatalf("status %d response %#v", ctx.Response().Status, response)
	}
	item := response.Items[0]
	if item.Status != "ok" || item.MessageCount != 12 || item.Level != 1 || item.AnchorEndMS == 0 || item.CompletedAt == nil || item.Summary == "" {
		t.Fatalf("item = %#v", item)
	}
	usage, ok := item.Usage.(map[string]any)
	if !ok || usage["inputTokens"] != float64(4100) {
		t.Fatalf("usage = %#v", item.Usage)
	}
	if len(queries.params) != 1 || queries.params[0].BotID != testUUID(lifecycleTestBotID) || queries.params[0].SessionID != testUUID(lifecycleTestSessionID) || queries.params[0].Limit != sessionCompactionsDefaultLimit+1 {
		t.Fatalf("params = %#v", queries.params)
	}
}

func TestGetSessionCompactionsPagesByKeysetCursor(t *testing.T) {
	t.Parallel()

	queries := &sessionCompactionStub{contextLifecycleAccessStub: newContextLifecycleAccessStub(t, "chat"), rows: compactionRows(3)}
	handler := newContextLifecycleGranteeHandler(queries)
	first := newContextLifecycleGranteeContext(t, "/compactions?limit=2", false)
	if err := handler.GetSessionCompactions(first); err != nil {
		t.Fatalf("first page: %v", err)
	}
	var page SessionCompactionsResponse
	if err := json.Unmarshal(first.Response().Writer.(*httptest.ResponseRecorder).Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page.Items) != 2 || !page.HasMore || page.NextCursor == "" || queries.params[0].Limit != 3 {
		t.Fatalf("first page = %#v (params %#v)", page, queries.params)
	}

	second := newContextLifecycleGranteeContext(t, "/compactions?limit=2&before="+page.NextCursor, false)
	if err := handler.GetSessionCompactions(second); err != nil {
		t.Fatalf("second page: %v", err)
	}
	var older SessionCompactionsResponse
	if err := json.Unmarshal(second.Response().Writer.(*httptest.ResponseRecorder).Body.Bytes(), &older); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(older.Items) != 1 || older.HasMore || older.NextCursor != "" || older.Items[0].ID != page.Items[1].ID && older.Items[0].StartedAt.After(page.Items[1].StartedAt) {
		t.Fatalf("second page = %#v", older)
	}
	if len(queries.beforeParams) != 1 || !queries.beforeParams[0].BeforeStartedAt.Time.Equal(page.Items[1].StartedAt) || queries.beforeParams[0].BeforeID.String() != page.Items[1].ID {
		t.Fatalf("cursor params = %#v, want the oldest item of the first page", queries.beforeParams)
	}

	invalid := newContextLifecycleGranteeContext(t, "/compactions?before=not-a-cursor", false)
	if err := handler.GetSessionCompactions(invalid); err == nil {
		t.Fatalf("an invalid cursor must be rejected")
	}
}
