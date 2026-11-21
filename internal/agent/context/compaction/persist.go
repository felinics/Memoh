package compaction

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/chat/checkpoint"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/runtimefence"
)

// rewindReplacedTurn moves the session to the checkpoint an edit or retry's
// replacement keeps, in a transaction fenced by the caller's runtime
// ownership. The request renders the history before the replaced turn, which
// only that checkpoint stands for; one that depends on the turn would fold
// into the summary rows the request does not show.
func (s *Service) rewindReplacedTurn(ctx context.Context, botID, sessionID, turnID string) error {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	sessionUUID, err := db.ParseUUID(sessionID)
	if err != nil {
		return err
	}
	turnUUID, err := db.ParseUUID(turnID)
	if err != nil {
		return err
	}
	var rewound bool
	if err := s.inSession(ctx, botID, sessionID, func(q dbstore.Queries) error {
		rewound, err = q.RewindHistoryCheckpointBeforeTurn(ctx, sqlc.RewindHistoryCheckpointBeforeTurnParams{
			SessionID: sessionUUID, BotID: botUUID, TurnID: turnUUID,
		})
		return err
	}); err != nil {
		return staleSource(err)
	}
	if rewound {
		s.logger.InfoContext(ctx, "compaction: rewound the checkpoint that depends on the replaced turn",
			slog.String("session_id", sessionID),
			slog.String("turn_id", turnID),
		)
	}
	return nil
}

// openLog records a pass before its summarizer call, fenced by the caller's
// runtime ownership.
func (s *Service) openLog(ctx context.Context, botID, sessionID string) (pgtype.UUID, error) {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return pgtype.UUID{}, err
	}
	sessionUUID, err := db.ParseUUID(sessionID)
	if err != nil {
		return pgtype.UUID{}, err
	}
	var logID pgtype.UUID
	if err := s.inSession(ctx, botID, sessionID, func(q dbstore.Queries) error {
		row, err := q.CreateCompactionLog(ctx, sqlc.CreateCompactionLogParams{BotID: botUUID, SessionID: sessionUUID})
		logID = row.ID
		return err
	}); err != nil {
		return pgtype.UUID{}, staleSource(err)
	}
	return logID, nil
}

// transactionRunner is a store that can run work in one transaction.
type transactionRunner interface {
	InTx(context.Context, func(dbstore.Queries) error) error
	SupportsTransactions() bool
}

// inSession runs fn in one transaction, fenced by the caller's runtime
// ownership when it holds one, so a run that lost it cannot write. Without a
// fence the transaction locks the bot before the session, as fenced writes
// do. A store without transactions runs fn directly.
func (s *Service) inSession(ctx context.Context, botID, sessionID string, fn func(dbstore.Queries) error) error {
	if _, ok := runtimefence.FromContext(ctx); ok {
		return runtimefence.InTransaction(ctx, s.queries, botID, sessionID, fn)
	}
	if txer, ok := s.queries.(transactionRunner); ok && txer.SupportsTransactions() {
		return txer.InTx(ctx, func(q dbstore.Queries) error {
			if err := runtimefence.LockBotForSessionWrite(ctx, q, botID); err != nil {
				return err
			}
			return fn(q)
		})
	}
	return fn(s.queries)
}

// staleSource classifies a write the database refused because the session
// or its history moved on: lost runtime ownership, a checkpoint that moved,
// or a log that is no longer pending.
func staleSource(err error) error {
	if errors.Is(err, checkpoint.ErrMoved) || errors.Is(err, runtimefence.ErrStale) || errors.Is(err, pgx.ErrNoRows) {
		return failed(ReasonStaleSource, err)
	}
	return err
}

func (s *Service) completeLog(ctx context.Context, q dbstore.Queries, logID pgtype.UUID, status, summary, errMsg string, messageCount int, usage []byte, modelID pgtype.UUID) error {
	_, err := q.CompleteCompactionLog(ctx, sqlc.CompleteCompactionLogParams{
		ID:           logID,
		Status:       status,
		Summary:      summary,
		MessageCount: int32(messageCount), //nolint:gosec // count always small
		ErrorMessage: errMsg,
		Usage:        usage,
		ModelID:      modelID,
	})
	if err != nil {
		s.logger.ErrorContext(ctx, "failed to complete compaction log", slog.String("error", err.Error()))
		return err
	}
	return nil
}
