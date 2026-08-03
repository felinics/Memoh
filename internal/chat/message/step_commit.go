package message

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbpkg "github.com/memohai/memoh/internal/db"
	"github.com/memohai/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/memohai/memoh/internal/db/store"
	"github.com/memohai/memoh/internal/runtimefence"
)

var ErrAgentStepNotWritable = errors.New("agent step is no longer writable")

type agentStepCommitQueries interface {
	CreateAgentStepCommit(context.Context, sqlc.CreateAgentStepCommitParams) (sqlc.AgentStepCommit, error)
	GetAgentStepCommit(context.Context, sqlc.GetAgentStepCommitParams) (sqlc.AgentStepCommit, error)
	LockSessionRunForAgentStepCommit(context.Context, sqlc.LockSessionRunForAgentStepCommitParams) (pgtype.UUID, error)
}

// PersistAgentStep appends a complete SDK step and its idempotency marker in
// one runtime-fenced transaction. It locks the run row before reserving a new
// marker, which linearizes this commit against RequestSessionRunAbort.
func (s *DBService) PersistAgentStep(ctx context.Context, step AgentStepCommit) ([]Message, bool, error) {
	if s == nil || s.queries == nil {
		return nil, false, errors.New("message service is not configured")
	}
	if step.StepIndex < 0 || len(step.Messages) == 0 {
		return nil, false, errors.New("agent step requires a non-negative index and messages")
	}
	if len(step.Messages) > math.MaxInt32 {
		return nil, false, errors.New("agent step has too many messages")
	}
	runID := strings.TrimSpace(step.RunID)
	botID := strings.TrimSpace(step.Messages[0].BotID)
	sessionID := strings.TrimSpace(step.Messages[0].SessionID)
	if runID == "" || botID == "" || sessionID == "" {
		return nil, false, errors.New("agent step requires run, bot, and session ids")
	}
	for _, input := range step.Messages {
		if input.SkipHistoryTurn || strings.TrimSpace(input.RunID) != runID ||
			strings.TrimSpace(input.BotID) != botID || strings.TrimSpace(input.SessionID) != sessionID {
			return nil, false, errors.New("agent step messages must share one visible run and session")
		}
	}
	fence, ok := runtimefence.FromContext(ctx)
	if !ok {
		return nil, false, errors.New("agent step requires a runtime persistence fence")
	}
	pgRunID, err := dbpkg.ParseUUID(runID)
	if err != nil {
		return nil, false, fmt.Errorf("invalid agent step run id: %w", err)
	}
	pgBotID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		return nil, false, fmt.Errorf("invalid agent step bot id: %w", err)
	}
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return nil, false, fmt.Errorf("invalid agent step session id: %w", err)
	}
	messageCount := int32(len(step.Messages)) //nolint:gosec // bounded by the MaxInt32 check above

	var persisted []Message
	committed := false
	err = runtimefence.InTransaction(ctx, s.queries, botID, sessionID, func(queries dbstore.Queries) error {
		writer, ok := queries.(agentStepCommitQueries)
		if !ok {
			return errors.New("persistence store does not support agent step commits")
		}
		key := sqlc.GetAgentStepCommitParams{RunID: pgRunID, StepIndex: int64(step.StepIndex)}
		if _, err := writer.GetAgentStepCommit(ctx, key); err == nil {
			return nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("read agent step commit: %w", err)
		}
		if _, err := writer.LockSessionRunForAgentStepCommit(ctx, sqlc.LockSessionRunForAgentStepCommitParams{
			RunID: pgRunID, BotID: pgBotID, SessionID: pgSessionID, FencingToken: fence.Token,
		}); errors.Is(err, pgx.ErrNoRows) {
			return ErrAgentStepNotWritable
		} else if err != nil {
			return fmt.Errorf("lock session run for agent step: %w", err)
		}
		if _, err := writer.CreateAgentStepCommit(ctx, sqlc.CreateAgentStepCommitParams{
			RunID: pgRunID, StepIndex: int64(step.StepIndex), MessageCount: messageCount,
		}); errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return fmt.Errorf("create agent step commit: %w", err)
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
		committed = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if committed {
		for _, message := range persisted {
			s.publishMessageCreated(message)
		}
	}
	return persisted, committed, nil
}
