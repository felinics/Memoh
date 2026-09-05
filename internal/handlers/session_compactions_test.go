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
	params []sqlc.ListCompactionLogsBySessionParams
}

func (s *sessionCompactionStub) ListCompactionLogsBySession(_ context.Context, arg sqlc.ListCompactionLogsBySessionParams) ([]sqlc.BotHistoryMessageCompact, error) {
	s.params = append(s.params, arg)
	started := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	return []sqlc.BotHistoryMessageCompact{{
		ID:            pgtype.UUID{Bytes: [16]byte{9}, Valid: true},
		BotID:         testUUID(lifecycleTestBotID),
		SessionID:     testUUID(lifecycleTestSessionID),
		Status:        "ok",
		Summary:       "The user asked for a listing; the agent listed the temp dir.",
		MessageCount:  12,
		Usage:         []byte(`{"inputTokens":4100,"outputTokens":320}`),
		AnchorStartMs: started.Add(-time.Hour).UnixMilli(),
		AnchorEndMs:   started.Add(-time.Minute).UnixMilli(),
		ArtifactLevel: 1,
		StartedAt:     pgtype.Timestamptz{Time: started, Valid: true},
		CompletedAt:   pgtype.Timestamptz{Time: started.Add(9 * time.Second), Valid: true},
	}}, nil
}

func TestGetSessionCompactionsListsTheSessionsRunsForAReader(t *testing.T) {
	t.Parallel()

	queries := &sessionCompactionStub{contextLifecycleAccessStub: newContextLifecycleAccessStub(t, "chat")}
	handler := newContextLifecycleGranteeHandler(queries)
	ctx := newContextLifecycleGranteeContext(t, "/compactions", false)
	if err := handler.GetSessionCompactions(ctx); err != nil {
		t.Fatalf("GetSessionCompactions: %v", err)
	}
	var response SessionCompactionsResponse
	if err := json.Unmarshal(ctx.Response().Writer.(*httptest.ResponseRecorder).Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ctx.Response().Status != http.StatusOK || len(response.Items) != 1 {
		t.Fatalf("status %d items %#v", ctx.Response().Status, response.Items)
	}
	item := response.Items[0]
	if item.Status != "ok" || item.MessageCount != 12 || item.Level != 1 || item.AnchorEndMS == 0 || item.CompletedAt == nil || item.Summary == "" {
		t.Fatalf("item = %#v", item)
	}
	usage, ok := item.Usage.(map[string]any)
	if !ok || usage["inputTokens"] != float64(4100) {
		t.Fatalf("usage = %#v", item.Usage)
	}
	if len(queries.params) != 1 || queries.params[0].BotID != testUUID(lifecycleTestBotID) || queries.params[0].SessionID != testUUID(lifecycleTestSessionID) || queries.params[0].Limit != sessionCompactionsLimit {
		t.Fatalf("params = %#v", queries.params)
	}
}
