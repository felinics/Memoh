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
	botID, sessionID, err := validateAgentStep(ctx, s, step)
	if err != nil {
		return nil, err
	}
	if !step.Interrupted {
		step.Messages = append([]PersistInput(nil), step.Messages...)
		for i := range step.Messages {
			step.Messages[i] = s.prepareMarkdownMedia(ctx, step.Messages[i])
		}
	}
	var persisted []Message
	err = runtimefence.InTransaction(ctx, s.queries, botID, sessionID, func(queries dbstore.Queries) error {
		var txErr error
		persisted, txErr = s.persistAgentStepTx(ctx, queries, step)
		return txErr
	})
	if err != nil {
		return nil, err
	}
	for _, message := range persisted {
		s.publishMessageCreated(message)
	}
	return persisted, nil
}

func (s *DBService) persistAgentStepTx(ctx context.Context, queries dbstore.Queries, step AgentStep) ([]Message, error) {
	if _, _, err := validateAgentStep(ctx, s, step); err != nil {
		return nil, err
	}
	if queries == nil {
		return nil, errors.New("persistence transaction is not configured")
	}
	runID := strings.TrimSpace(step.RunID)
	botID := strings.TrimSpace(step.Messages[0].BotID)
	sessionID := strings.TrimSpace(step.Messages[0].SessionID)
	fence, _ := runtimefence.FromContext(ctx)
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
	writer, ok := queries.(agentStepQueries)
	if !ok {
		return nil, errors.New("persistence store does not support agent step writes")
	}
	params := sqlc.LockSessionRunForAgentStepCommitParams{
		RunID: pgRunID, BotID: pgBotID, SessionID: pgSessionID,
		FencingToken: fence.Token, Interrupted: step.Interrupted,
	}
	_, lockErr := writer.LockSessionRunForAgentStepCommit(ctx, params)
	if errors.Is(lockErr, pgx.ErrNoRows) {
		return nil, ErrAgentStepNotWritable
	} else if lockErr != nil {
		return nil, fmt.Errorf("lock session run for agent step: %w", lockErr)
	}

	txService := *s
	txService.queries = queries
	txService.publisher = nil
	return txService.persistBoundMessages(ctx, step.Messages, step.Replacement)
}

func validateAgentStep(ctx context.Context, s *DBService, step AgentStep) (string, string, error) {
	if s == nil || s.queries == nil {
		return "", "", errors.New("message service is not configured")
	}
	if len(step.Messages) == 0 {
		return "", "", errors.New("agent step requires messages")
	}
	runID := strings.TrimSpace(step.RunID)
	botID := strings.TrimSpace(step.Messages[0].BotID)
	sessionID := strings.TrimSpace(step.Messages[0].SessionID)
	if runID == "" || botID == "" || sessionID == "" {
		return "", "", errors.New("agent step requires run, bot, and session ids")
	}
	for _, input := range step.Messages {
		if input.SkipHistoryTurn || strings.TrimSpace(input.RunID) != runID ||
			strings.TrimSpace(input.BotID) != botID || strings.TrimSpace(input.SessionID) != sessionID {
			return "", "", errors.New("agent step messages must be turn-bound and share one run and session")
		}
	}
	if _, ok := runtimefence.FromContext(ctx); !ok {
		return "", "", errors.New("agent step requires a runtime persistence fence")
	}
	return botID, sessionID, nil
}
