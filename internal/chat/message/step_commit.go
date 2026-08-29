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
	var persisted []Message
	err = runtimefence.InTransaction(ctx, s.queries, botID, sessionID, func(queries dbstore.Queries) error {
		var txErr error
		persisted, txErr = s.PersistAgentStepTx(ctx, queries, step)
		return txErr
	})
	if err != nil {
		return nil, err
	}
	s.PublishAgentStep(persisted)
	return persisted, nil
}

// PublishAgentStep emits the post-commit notifications normally owned by
// PersistAgentStep. Coordinator-owned transactions call this after their outer
// commit; keeping it here preserves one publication path for all message rows.
func (s *DBService) PublishAgentStep(messages []Message) {
	for _, message := range messages {
		s.publishMessageCreated(message)
	}
}

// PersistAgentStepTx persists an agent step inside the caller's transaction.
// Publishing remains the outer operation's responsibility and happens only
// after that transaction commits.
func (s *DBService) PersistAgentStepTx(ctx context.Context, queries dbstore.Queries, step AgentStep) ([]Message, error) {
	return s.persistAgentStepTx(ctx, queries, step, false)
}

// PersistAgentReplacementStepTx appends a retry/edit step without projecting
// it into visible history. The queue coordinator owns the surrounding
// transaction, so a step and any queue claim transition succeed or roll back
// together.
func (s *DBService) PersistAgentReplacementStepTx(ctx context.Context, queries dbstore.Queries, step AgentStep) ([]Message, error) {
	return s.persistAgentStepTx(ctx, queries, step, true)
}

func (s *DBService) persistAgentStepTx(ctx context.Context, queries dbstore.Queries, step AgentStep, replacement bool) ([]Message, error) {
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

// FinalizeAgentReplacementTx makes the accumulated hidden retry/edit output
// the canonical visible turn. It deliberately does not open a transaction;
// the caller must run it inside the same coordinator transaction that
// terminalizes R0 and assigns a follow-up continuation.
func (s *DBService) FinalizeAgentReplacementTx(
	ctx context.Context,
	queries dbstore.Queries,
	sessionID string,
	replacement TurnReplacement,
	requestMessageID string,
	assistantMessageID string,
) error {
	if s == nil || s.queries == nil || queries == nil {
		return errors.New("replacement persistence transaction is not configured")
	}
	if _, ok := runtimefence.FromContext(ctx); !ok {
		return errors.New("agent replacement requires a runtime persistence fence")
	}
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
}

func validateAgentStep(ctx context.Context, s *DBService, step AgentStep) (string, string, error) {
	return validateAgentStepMode(ctx, s, step, false)
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
