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

type agentFailureQueries interface {
	FindAgentFailureCheckpoint(context.Context, sqlc.FindAgentFailureCheckpointParams) (pgtype.UUID, error)
}

type agentStepQueries interface {
	LockSessionRunForAgentStepCommit(context.Context, sqlc.LockSessionRunForAgentStepCommitParams) (pgtype.UUID, error)
}

// PersistAgentStep appends one SDK step in a runtime-fenced transaction.
// Complete steps precede abort intent; interrupted checkpoints remain writable
// until terminal finalization for cancellation paths without recorded intent.
func (s *DBService) PersistAgentStep(ctx context.Context, step AgentStep) ([]Message, error) {
	return s.persistAgentStep(ctx, step, false, false)
}

// PersistAgentReplacementStep keeps retry/edit output hidden until the true
// final boundary. Both step kinds use the same fenced persistence transaction.
func (s *DBService) PersistAgentReplacementStep(ctx context.Context, step AgentStep) ([]Message, error) {
	return s.persistAgentStep(ctx, step, true, false)
}

// PersistAgentFailure appends only the error checkpoint, retaining a request
// that may already have committed even if its transaction response was lost.
func (s *DBService) PersistAgentFailure(ctx context.Context, step AgentStep) ([]Message, error) {
	if len(step.Messages) < 1 || len(step.Messages) > 2 {
		return nil, errors.New("invalid failure checkpoint")
	}
	for i, msg := range step.Messages {
		if i == len(step.Messages)-1 {
			code, _ := msg.Metadata[HistoryErrorCodeMetadataKey].(string)
			if msg.Role != "assistant" || strings.TrimSpace(code) == "" {
				return nil, errors.New("failure checkpoint requires an assistant error")
			}
		} else if msg.Role != "user" {
			return nil, errors.New("failure checkpoint contains unexpected output")
		}
	}
	step.Interrupted = true
	return s.persistAgentStep(ctx, step, false, true)
}

func (s *DBService) persistAgentStep(ctx context.Context, step AgentStep, replacement, failure bool) ([]Message, error) {
	botID, sessionID, err := validateAgentStepMode(ctx, s, step, replacement)
	if err != nil {
		return nil, err
	}
	var persisted []Message
	err = runtimefence.InTransaction(ctx, s.queries, botID, sessionID, func(queries dbstore.Queries) error {
		var txErr error
		persisted, txErr = s.persistAgentStepTx(ctx, queries, step, replacement, failure)
		return txErr
	})
	if err != nil {
		return nil, err
	}
	if !replacement {
		for _, message := range persisted {
			s.publishMessageCreated(message)
		}
	}
	return persisted, nil
}

func (s *DBService) persistAgentStepTx(ctx context.Context, queries dbstore.Queries, step AgentStep, replacement, failure bool) ([]Message, error) {
	if _, _, err := validateAgentStepMode(ctx, s, step, replacement); err != nil {
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
	if failure {
		turnID, err := dbpkg.ParseUUID(step.Messages[0].TurnID)
		if err != nil {
			return nil, err
		}
		finder, ok := queries.(agentFailureQueries)
		if !ok {
			return nil, errors.New("persistence store does not support failure checkpoints")
		}
		code, _ := step.Messages[len(step.Messages)-1].Metadata[HistoryErrorCodeMetadataKey].(string)
		id, findErr := finder.FindAgentFailureCheckpoint(ctx, sqlc.FindAgentFailureCheckpointParams{
			BotID: pgBotID, SessionID: pgSessionID, RunID: pgRunID, TurnID: turnID, ErrorCode: code,
		})
		if findErr == nil {
			prior, err := txService.GetByIDBySession(ctx, sessionID, uuidString(id))
			if err != nil {
				return nil, err
			}
			return []Message{prior}, nil
		}
		if !errors.Is(findErr, pgx.ErrNoRows) {
			return nil, findErr
		}
		turn, err := queries.GetHistoryTurnByID(ctx, sqlc.GetHistoryTurnByIDParams{SessionID: pgSessionID, OldTurnID: turnID})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			if turn.SupersededByTurnID.Valid {
				return nil, ErrAgentStepNotWritable
			}
			checkpoint := step.Messages[len(step.Messages)-1]
			checkpoint.TurnRequestMessageID = uuidString(turn.RequestMessageID)
			step.Messages = []PersistInput{checkpoint}
		}
	}
	turnRequestMessageID := strings.TrimSpace(step.Messages[0].TurnRequestMessageID)
	persisted := make([]Message, 0, len(step.Messages))
	for _, original := range step.Messages {
		input := original
		input.TurnRequestMessageID = turnRequestMessageID
		message, err := txService.persist(ctx, input)
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(strings.TrimSpace(input.Role), "user") {
			turnRequestMessageID = message.ID
		}
		persisted = append(persisted, message)
	}
	return persisted, nil
}

// FinalizeAgentReplacement atomically selects the completed replacement turn.
// Queue coordination is transient and does not own this database transaction.
func (s *DBService) FinalizeAgentReplacement(ctx context.Context, sessionID string, replacement TurnReplacement, requestMessageID, assistantMessageID string) error {
	if s == nil || s.queries == nil {
		return errors.New("replacement persistence is not configured")
	}
	fence, ok := runtimefence.FromContext(ctx)
	if !ok {
		return errors.New("agent replacement requires a runtime persistence fence")
	}
	return runtimefence.InTransaction(ctx, s.queries, fence.BotID, sessionID, func(queries dbstore.Queries) error {
		requestMessageID = strings.TrimSpace(requestMessageID)
		assistantMessageID = strings.TrimSpace(assistantMessageID)
		if requestMessageID == "" || assistantMessageID == "" {
			return errors.New("agent replacement requires request and assistant message ids")
		}
		txService := *s
		txService.queries = queries
		txService.publisher = nil
		replacement.RequestMessageID = requestMessageID
		return txService.replacePersistedRound(ctx, strings.TrimSpace(sessionID), []Message{
			{ID: requestMessageID, Role: "user"},
			{ID: assistantMessageID, Role: "assistant"},
		}, replacement)
	})
}

func validateAgentStepMode(ctx context.Context, s *DBService, step AgentStep, replacement bool) (string, string, error) {
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
		if input.SkipHistoryTurn != replacement || strings.TrimSpace(input.RunID) != runID ||
			strings.TrimSpace(input.BotID) != botID || strings.TrimSpace(input.SessionID) != sessionID {
			return "", "", errors.New("agent step messages must share one run, session, and history visibility mode")
		}
	}
	if _, ok := runtimefence.FromContext(ctx); !ok {
		return "", "", errors.New("agent step requires a runtime persistence fence")
	}
	return botID, sessionID, nil
}
