package message

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/runtimefence"
)

var ErrAgentStepNotWritable = errors.New("agent step is no longer writable")

type agentStepQueries interface {
	LockSessionRunForAgentStepCommit(context.Context, sqlc.LockSessionRunForAgentStepCommitParams) (pgtype.UUID, error)
}

// PersistAgentStep appends one SDK step in a runtime-fenced transaction.
// Complete steps precede abort intent; interrupted checkpoints remain writable
// until terminal finalization for cancellation paths without recorded intent.
func (s *DBService) PersistAgentStep(ctx context.Context, step AgentStep) ([]Message, error) {
	if s == nil || s.queries == nil {
		return nil, errors.New("message service is not configured")
	}
	if len(step.Messages) == 0 {
		return nil, errors.New("agent step requires messages")
	}
	runID := strings.TrimSpace(step.RunID)
	botID := strings.TrimSpace(step.Messages[0].BotID)
	sessionID := strings.TrimSpace(step.Messages[0].SessionID)
	if runID == "" || botID == "" || sessionID == "" {
		return nil, errors.New("agent step requires run, bot, and session ids")
	}
	for _, input := range step.Messages {
		if input.SkipHistoryTurn || strings.TrimSpace(input.RunID) != runID ||
			strings.TrimSpace(input.BotID) != botID || strings.TrimSpace(input.SessionID) != sessionID {
			return nil, errors.New("agent step messages must share one visible run and session")
		}
	}
	fence, ok := runtimefence.FromContext(ctx)
	if !ok {
		return nil, errors.New("agent step requires a runtime persistence fence")
	}
	pgRunID, err := dbpkg.ParseUUID(runID)
	if err != nil {
		return nil, fmt.Errorf("invalid agent step run id: %w", err)
	}
	pgBotID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		return nil, fmt.Errorf("invalid agent step bot id: %w", err)
	}
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return nil, fmt.Errorf("invalid agent step session id: %w", err)
	}

	var persisted []Message
	err = runtimefence.InTransaction(ctx, s.queries, botID, sessionID, func(queries dbstore.Queries) error {
		writer, ok := queries.(agentStepQueries)
		if !ok {
			return errors.New("persistence store does not support agent step writes")
		}
		params := sqlc.LockSessionRunForAgentStepCommitParams{
			RunID: pgRunID, BotID: pgBotID, SessionID: pgSessionID,
			FencingToken: fence.Token, Interrupted: step.Interrupted,
		}
		_, lockErr := writer.LockSessionRunForAgentStepCommit(ctx, params)
		if errors.Is(lockErr, pgx.ErrNoRows) {
			return ErrAgentStepNotWritable
		} else if lockErr != nil {
			return fmt.Errorf("lock session run for agent step: %w", lockErr)
		}

		txService := *s
		txService.queries = queries
		txService.publisher = nil
		turnRequestMessageID := strings.TrimSpace(step.Messages[0].TurnRequestMessageID)
		persisted = make([]Message, 0, len(step.Messages))
		for _, original := range step.Messages {
			input := original
			input.TurnRequestMessageID = turnRequestMessageID
			message, err := txService.persist(ctx, input)
			if err != nil {
				return err
			}
			if strings.EqualFold(strings.TrimSpace(input.Role), "user") {
				turnRequestMessageID = message.ID
			}
			persisted = append(persisted, message)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, message := range persisted {
		s.publishMessageCreated(message)
	}
	return persisted, nil
}
