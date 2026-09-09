package input

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/runtimefence"
)

type staleUserInputQueries struct {
	dbstore.Queries
	cancelCalled bool
}

func (*staleUserInputQueries) SupportsTransactions() bool { return true }

func (q *staleUserInputQueries) InTx(_ context.Context, fn func(dbstore.Queries) error) error {
	return fn(q)
}

func (*staleUserInputQueries) LockBotForSessionWrite(_ context.Context, id pgtype.UUID) (pgtype.UUID, error) {
	return id, nil
}

func (*staleUserInputQueries) LockSessionRuntimeFence(context.Context, sqlc.LockSessionRuntimeFenceParams) (int64, error) {
	return 0, pgx.ErrNoRows
}

func (q *staleUserInputQueries) CancelUserInputRequest(context.Context, sqlc.CancelUserInputRequestParams) (sqlc.UserInputRequest, error) {
	q.cancelCalled = true
	return sqlc.UserInputRequest{}, nil
}

func TestCancelRejectsStaleRuntimeFence(t *testing.T) {
	t.Parallel()

	queries := &staleUserInputQueries{}
	service := NewService(nil, queries)
	ctx := runtimefence.WithContext(context.Background(), runtimefence.Fence{
		BotID:     "11111111-1111-1111-1111-111111111111",
		SessionID: "22222222-2222-2222-2222-222222222222",
		Token:     4,
	})
	_, err := service.Cancel(ctx, CancelInput{RequestID: "33333333-3333-3333-3333-333333333333"})
	if !errors.Is(err, runtimefence.ErrStale) {
		t.Fatalf("Cancel() error = %v, want ErrStale", err)
	}
	if queries.cancelCalled {
		t.Fatal("stale user input response updated the request")
	}
}
