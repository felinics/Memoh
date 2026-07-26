package decision

import (
	"context"
	"errors"
	"testing"

	runtimefence "github.com/memohai/memoh/domains/agent/chat/session/fence"
)

type transactionStore struct {
	order []string
}

func (s *transactionStore) InDecisionTransaction(_ context.Context, fn func(Store) error) error {
	return fn(s)
}

func (s *transactionStore) LockBotForSessionWrite(context.Context, string) error {
	s.order = append(s.order, "bot")
	return nil
}

func (s *transactionStore) LockSessionDecisionSequence(context.Context, string, string) error {
	s.order = append(s.order, "session")
	return nil
}

func TestInCreateTransactionLocksBotBeforeSession(t *testing.T) {
	store := &transactionStore{}
	called := false
	err := InCreateTransaction(t.Context(), store, "bot", "session", func(Store) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("InCreateTransaction() error = %v", err)
	}
	if !called || len(store.order) != 2 || store.order[0] != "bot" || store.order[1] != "session" {
		t.Fatalf("order = %#v, called = %v", store.order, called)
	}
}

func TestInCreateTransactionRejectsMissingTransactor(t *testing.T) {
	err := InCreateTransaction(t.Context(), nil, "bot", "session", func(Store) error { return nil })
	if !errors.Is(err, runtimefence.ErrTransactionsUnsupported) {
		t.Fatalf("InCreateTransaction() error = %v", err)
	}
}
