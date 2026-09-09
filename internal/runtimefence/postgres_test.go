package runtimefence

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

const (
	testBotID     = "11111111-1111-1111-1111-111111111111"
	testSessionID = "22222222-2222-2222-2222-222222222222"
)

type transactionTestQueries struct {
	dbstore.Queries
	supportsTransactions bool
	lockErr              error
	inTransaction        bool
	lockCalled           bool
	parentLockCalled     bool
}

type activationTestQueries struct {
	dbstore.Queries
	current              int64
	toolApprovalCancel   sqlc.SupersedePendingToolApprovalsBySessionParams
	userInputCancel      sqlc.SupersedePendingUserInputsBySessionParams
	toolApprovalCanceled bool
	userInputCanceled    bool
	toolApprovalClaimed  bool
	userInputClaimed     bool
	toolApprovalClaim    sqlc.ClaimToolApprovalRequestForRuntimeParams
	userInputClaim       sqlc.ClaimUserInputRequestForRuntimeParams
	preservedApproval    sqlc.ToolApprovalRequest
	preservedInput       sqlc.UserInputRequest
	parentLockCalled     bool
}

func (*activationTestQueries) SupportsTransactions() bool { return true }

func (q *activationTestQueries) InTx(_ context.Context, fn func(dbstore.Queries) error) error {
	return fn(q)
}

func (q *activationTestQueries) LockBotForSessionWrite(_ context.Context, id pgtype.UUID) (pgtype.UUID, error) {
	q.parentLockCalled = true
	return id, nil
}

func (q *activationTestQueries) LockSessionRuntimeFenceForActivation(context.Context, sqlc.LockSessionRuntimeFenceForActivationParams) (int64, error) {
	if !q.parentLockCalled {
		return 0, errors.New("session lock acquired before bot parent lock")
	}
	return q.current, nil
}

func (q *activationTestQueries) ActivateSessionRuntimeFence(_ context.Context, arg sqlc.ActivateSessionRuntimeFenceParams) (int64, error) {
	q.current = arg.RuntimeFencingToken
	return q.current, nil
}

func (q *activationTestQueries) SupersedePendingToolApprovalsBySession(_ context.Context, arg sqlc.SupersedePendingToolApprovalsBySessionParams) ([]sqlc.ToolApprovalRequest, error) {
	q.toolApprovalCancel = arg
	q.toolApprovalCanceled = true
	return nil, nil
}

func (q *activationTestQueries) SupersedePendingUserInputsBySession(_ context.Context, arg sqlc.SupersedePendingUserInputsBySessionParams) ([]sqlc.UserInputRequest, error) {
	q.userInputCancel = arg
	q.userInputCanceled = true
	return nil, nil
}

func (q *activationTestQueries) ClaimToolApprovalRequestForRuntime(_ context.Context, arg sqlc.ClaimToolApprovalRequestForRuntimeParams) (sqlc.ToolApprovalRequest, error) {
	q.toolApprovalClaimed = true
	q.toolApprovalClaim = arg
	return q.preservedApproval, nil
}

func (q *activationTestQueries) ClaimUserInputRequestForRuntime(_ context.Context, arg sqlc.ClaimUserInputRequestForRuntimeParams) (sqlc.UserInputRequest, error) {
	q.userInputClaimed = true
	q.userInputClaim = arg
	return q.preservedInput, nil
}

func (q *transactionTestQueries) SupportsTransactions() bool {
	return q.supportsTransactions
}

func (q *transactionTestQueries) InTx(_ context.Context, fn func(dbstore.Queries) error) error {
	q.inTransaction = true
	return fn(q)
}

func (q *transactionTestQueries) LockBotForSessionWrite(_ context.Context, id pgtype.UUID) (pgtype.UUID, error) {
	q.parentLockCalled = true
	return id, nil
}

func (q *transactionTestQueries) LockSessionRuntimeFence(_ context.Context, arg sqlc.LockSessionRuntimeFenceParams) (int64, error) {
	if !q.parentLockCalled {
		return 0, errors.New("session lock acquired before bot parent lock")
	}
	q.lockCalled = true
	if arg.RuntimeFencingToken != 7 {
		return 0, errors.New("unexpected token")
	}
	if q.lockErr != nil {
		return 0, q.lockErr
	}
	return arg.RuntimeFencingToken, nil
}

func TestInTransactionRequiresRealTransactionCapability(t *testing.T) {
	t.Parallel()

	queries := &transactionTestQueries{}
	ctx := WithContext(context.Background(), Fence{BotID: testBotID, SessionID: testSessionID, Token: 7})
	called := false
	err := InTransaction(ctx, queries, testBotID, testSessionID, func(dbstore.Queries) error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrTransactionsUnsupported) {
		t.Fatalf("InTransaction() error = %v, want ErrTransactionsUnsupported", err)
	}
	if called || queries.inTransaction || queries.lockCalled {
		t.Fatal("unsupported transaction store executed fenced write")
	}
}

func TestInTransactionLocksFenceBeforeCallback(t *testing.T) {
	t.Parallel()

	queries := &transactionTestQueries{supportsTransactions: true}
	ctx := WithContext(context.Background(), Fence{BotID: testBotID, SessionID: testSessionID, Token: 7})
	called := false
	err := InTransaction(ctx, queries, testBotID, testSessionID, func(got dbstore.Queries) error {
		called = true
		if got != queries || !queries.lockCalled {
			t.Fatal("callback ran before the fence lock")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("InTransaction() error = %v", err)
	}
	if !called || !queries.inTransaction {
		t.Fatal("fenced transaction callback did not run")
	}
}

func TestInTransactionMapsMissingFenceRowToStale(t *testing.T) {
	t.Parallel()

	queries := &transactionTestQueries{supportsTransactions: true, lockErr: pgx.ErrNoRows}
	ctx := WithContext(context.Background(), Fence{BotID: testBotID, SessionID: testSessionID, Token: 7})
	err := InTransaction(ctx, queries, testBotID, testSessionID, func(dbstore.Queries) error {
		t.Fatal("stale fence executed callback")
		return nil
	})
	if !errors.Is(err, ErrStale) {
		t.Fatalf("InTransaction() error = %v, want ErrStale", err)
	}
}

func TestActivateWithOptionsPreservesOnlyTheSelectedDecisionKind(t *testing.T) {
	t.Parallel()

	const decisionID = "33333333-3333-3333-3333-333333333333"
	tests := []struct {
		name               string
		kind               string
		wantToolPreserved  bool
		wantInputPreserved bool
	}{
		{name: "tool approval", kind: DecisionToolApproval, wantToolPreserved: true},
		{name: "user input", kind: DecisionUserInput, wantInputPreserved: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			queries := &activationTestQueries{
				current: 6,
				preservedApproval: sqlc.ToolApprovalRequest{
					BotID: mustTestUUID(t, testBotID), SessionID: mustTestUUID(t, testSessionID), Status: "pending",
				},
				preservedInput: sqlc.UserInputRequest{
					BotID: mustTestUUID(t, testBotID), SessionID: mustTestUUID(t, testSessionID), Status: "pending",
				},
			}
			err := ActivateWithOptions(context.Background(), queries, Fence{
				BotID: testBotID, SessionID: testSessionID, Token: 7,
			}, ActivationOptions{PreserveDecisions: []PreservedDecision{{Kind: tt.kind, ID: decisionID}}})
			if err != nil {
				t.Fatalf("ActivateWithOptions() error = %v", err)
			}
			if !queries.toolApprovalCanceled || !queries.userInputCanceled {
				t.Fatal("activation did not run both superseded-decision cleanup queries")
			}
			if (len(queries.toolApprovalCancel.PreserveIds) > 0) != tt.wantToolPreserved {
				t.Fatalf("tool approval preserve ids = %v, want preserved=%v", queries.toolApprovalCancel.PreserveIds, tt.wantToolPreserved)
			}
			if (len(queries.userInputCancel.PreserveIds) > 0) != tt.wantInputPreserved {
				t.Fatalf("user input preserve ids = %v, want preserved=%v", queries.userInputCancel.PreserveIds, tt.wantInputPreserved)
			}
			if queries.toolApprovalClaimed != tt.wantToolPreserved {
				t.Fatalf("tool approval claimed = %v, want %v", queries.toolApprovalClaimed, tt.wantToolPreserved)
			}
			if queries.userInputClaimed != tt.wantInputPreserved {
				t.Fatalf("user input claimed = %v, want %v", queries.userInputClaimed, tt.wantInputPreserved)
			}
			if tt.wantToolPreserved && queries.toolApprovalCancel.PreserveIds[0].String() != decisionID {
				t.Fatalf("tool approval preserve id = %q", queries.toolApprovalCancel.PreserveIds[0].String())
			}
			if tt.wantInputPreserved && queries.userInputCancel.PreserveIds[0].String() != decisionID {
				t.Fatalf("user input preserve id = %q", queries.userInputCancel.PreserveIds[0].String())
			}
			if tt.wantToolPreserved && (!queries.toolApprovalClaim.RuntimeFencingToken.Valid || queries.toolApprovalClaim.RuntimeFencingToken.Int64 != 7) {
				t.Fatalf("tool approval claim token = %#v", queries.toolApprovalClaim.RuntimeFencingToken)
			}
			if tt.wantInputPreserved && (!queries.userInputClaim.RuntimeFencingToken.Valid || queries.userInputClaim.RuntimeFencingToken.Int64 != 7) {
				t.Fatalf("user input claim token = %#v", queries.userInputClaim.RuntimeFencingToken)
			}
		})
	}
}

func mustTestUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		t.Fatalf("parse UUID %q: %v", value, err)
	}
	return id
}
