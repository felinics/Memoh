package decision

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/runtimefence"
)

type transactionRunner interface {
	InTx(context.Context, func(dbstore.Queries) error) error
}

type transactionCapability interface {
	SupportsTransactions() bool
}

type sequenceLocker interface {
	LockSessionDecisionSequence(context.Context, sqlc.LockSessionDecisionSequenceParams) (pgtype.UUID, error)
}

// InCreateTransaction serializes per-session decision short IDs before the
// INSERT statement takes its MVCC snapshot. Non-transactional test stores
// retain their own local serialization.
func InCreateTransaction(ctx context.Context, queries dbstore.Queries, botID, sessionID string, create func(dbstore.Queries) error) error {
	if create == nil {
		return errors.New("decision create callback is required")
	}
	if _, fenced := runtimefence.FromContext(ctx); fenced {
		return runtimefence.InTransaction(ctx, queries, botID, sessionID, func(txQueries dbstore.Queries) error {
			return lockAndCreate(ctx, txQueries, botID, sessionID, create)
		})
	}
	txer, txOK := queries.(transactionRunner)
	capability, capabilityOK := queries.(transactionCapability)
	if !txOK || !capabilityOK || !capability.SupportsTransactions() {
		return create(queries)
	}
	return txer.InTx(ctx, func(txQueries dbstore.Queries) error {
		return lockAndCreate(ctx, txQueries, botID, sessionID, create)
	})
}

func lockAndCreate(ctx context.Context, queries dbstore.Queries, botID, sessionID string, create func(dbstore.Queries) error) error {
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	pgSessionID, err := db.ParseUUID(sessionID)
	if err != nil {
		return err
	}
	if err := runtimefence.LockBotForSessionWrite(ctx, queries, botID); err != nil {
		return err
	}
	locker, ok := queries.(sequenceLocker)
	if !ok {
		return errors.New("decision store does not support session sequence locking")
	}
	if _, err := locker.LockSessionDecisionSequence(ctx, sqlc.LockSessionDecisionSequenceParams{
		SessionID: pgSessionID,
		BotID:     pgBotID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return runtimefence.ErrStale
		}
		return err
	}
	return create(queries)
}
