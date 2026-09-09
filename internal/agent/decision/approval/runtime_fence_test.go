package approval

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

type staleApprovalQueries struct {
	dbstore.Queries
	approveCalled bool
}

func (*staleApprovalQueries) SupportsTransactions() bool { return true }

func (q *staleApprovalQueries) InTx(_ context.Context, fn func(dbstore.Queries) error) error {
	return fn(q)
}

func (*staleApprovalQueries) LockBotForSessionWrite(_ context.Context, id pgtype.UUID) (pgtype.UUID, error) {
	return id, nil
}

func (*staleApprovalQueries) LockSessionRuntimeFence(context.Context, sqlc.LockSessionRuntimeFenceParams) (int64, error) {
	return 0, pgx.ErrNoRows
}

func (q *staleApprovalQueries) ApproveToolApprovalRequest(context.Context, sqlc.ApproveToolApprovalRequestParams) (sqlc.ToolApprovalRequest, error) {
	q.approveCalled = true
	return sqlc.ToolApprovalRequest{}, nil
}

func TestApproveRejectsStaleRuntimeFence(t *testing.T) {
	t.Parallel()

	queries := &staleApprovalQueries{}
	service := NewService(nil, queries, nil)
	ctx := runtimefence.WithContext(context.Background(), runtimefence.Fence{
		BotID:     "11111111-1111-1111-1111-111111111111",
		SessionID: "22222222-2222-2222-2222-222222222222",
		Token:     4,
	})
	_, err := service.Approve(ctx, "33333333-3333-3333-3333-333333333333", "", "")
	if !errors.Is(err, runtimefence.ErrStale) {
		t.Fatalf("Approve() error = %v, want ErrStale", err)
	}
	if queries.approveCalled {
		t.Fatal("stale approval updated the request")
	}
}
