package decision

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type createTransactionQueries struct {
	dbstore.Queries
	parentLocked  bool
	sessionLocked bool
}

func (*createTransactionQueries) SupportsTransactions() bool { return true }

func (q *createTransactionQueries) InTx(_ context.Context, fn func(dbstore.Queries) error) error {
	return fn(q)
}

func (q *createTransactionQueries) LockBotForSessionWrite(_ context.Context, id pgtype.UUID) (pgtype.UUID, error) {
	q.parentLocked = true
	return id, nil
}

func (q *createTransactionQueries) LockSessionDecisionSequence(_ context.Context, _ sqlc.LockSessionDecisionSequenceParams) (pgtype.UUID, error) {
	if !q.parentLocked {
		return pgtype.UUID{}, errors.New("session locked before bot parent")
	}
	q.sessionLocked = true
	return pgtype.UUID{Valid: true}, nil
}

func TestInCreateTransactionLocksBotBeforeSession(t *testing.T) {
	t.Parallel()

	queries := &createTransactionQueries{}
	called := false
	err := InCreateTransaction(context.Background(), queries,
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
		func(dbstore.Queries) error {
			called = true
			if !queries.parentLocked || !queries.sessionLocked {
				t.Fatal("create callback ran before parent and session locks")
			}
			return nil
		})
	if err != nil {
		t.Fatalf("InCreateTransaction() error = %v", err)
	}
	if !called {
		t.Fatal("create callback did not run")
	}
}
