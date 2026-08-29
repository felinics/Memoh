package input

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type lifecycleQueries struct {
	dbstore.Queries
	mu         sync.Mutex
	cancelArg  sqlc.CancelPendingUserInputsBySessionParams
	cancelCall int
}

func (q *lifecycleQueries) CancelPendingUserInputsBySession(_ context.Context, arg sqlc.CancelPendingUserInputsBySessionParams) ([]sqlc.UserInputRequest, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cancelArg = arg
	q.cancelCall++
	return []sqlc.UserInputRequest{{
		ID:               mustTestUUID("33333333-3333-3333-3333-333333333333"),
		BotID:            arg.BotID,
		SessionID:        arg.SessionID,
		ToolCallID:       "ask-1",
		ToolName:         "ask_user",
		ShortID:          1,
		Status:           StatusCanceled,
		InputJson:        []byte(`{"questions":[]}`),
		UiPayloadJson:    []byte(`{"questions":[]}`),
		ResultJson:       arg.ResultJson,
		ProviderMetadata: []byte(`{}`),
	}}, nil
}

func TestCancelPendingForSession(t *testing.T) {
	t.Parallel()

	queries := &lifecycleQueries{}
	svc := NewService(slog.New(slog.DiscardHandler), queries)

	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "22222222-2222-2222-2222-222222222222"
	cancelled, err := svc.CancelPendingForSession(context.Background(), botID, sessionID, "runtime closed")
	if err != nil {
		t.Fatalf("CancelPendingForSession() error = %v", err)
	}
	if len(cancelled) != 1 || cancelled[0].Status != StatusCanceled {
		t.Fatalf("cancelled = %#v, want one canceled request", cancelled)
	}
	if queries.cancelCall != 1 {
		t.Fatalf("cancel query calls = %d, want 1", queries.cancelCall)
	}
	if len(queries.cancelArg.ResultJson) == 0 {
		t.Fatal("empty canceled result payload")
	}

	if _, err := svc.CancelPendingForSession(context.Background(), botID, "not-a-uuid", "r"); err == nil {
		t.Fatal("CancelPendingForSession accepted a malformed session id")
	}
}

func mustTestUUID(value string) pgtype.UUID {
	id, err := db.ParseUUID(value)
	if err != nil {
		panic(err)
	}
	return id
}
