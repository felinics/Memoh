package decision

import (
	"context"
	"errors"

	runtimefence "github.com/memohai/memoh/domains/agent/chat/session/fence"
)

type Store interface {
	LockBotForSessionWrite(context.Context, string) error
	LockSessionDecisionSequence(context.Context, string, string) error
}

type Transactor interface {
	InDecisionTransaction(context.Context, func(Store) error) error
}

// InCreateTransaction serializes per-session decision short IDs before the
// INSERT statement takes its MVCC snapshot.
func InCreateTransaction(ctx context.Context, transactor Transactor, botID, sessionID string, create func(Store) error) error {
	if create == nil {
		return errors.New("decision create callback is required")
	}
	if transactor == nil {
		return runtimefence.ErrTransactionsUnsupported
	}
	return transactor.InDecisionTransaction(ctx, func(store Store) error {
		if err := store.LockBotForSessionWrite(ctx, botID); err != nil {
			return err
		}
		if err := store.LockSessionDecisionSequence(ctx, botID, sessionID); err != nil {
			return err
		}
		return create(store)
	})
}
