package message

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

type boundReplacementQueries interface {
	LockHistoryTurnReplacementSession(context.Context, sqlc.LockHistoryTurnReplacementSessionParams) (pgtype.UUID, error)
	CloneHistoryTurnRequestForRetry(context.Context, sqlc.CloneHistoryTurnRequestForRetryParams) (pgtype.UUID, error)
	SelectBoundHistoryTurnReplacement(context.Context, sqlc.SelectBoundHistoryTurnReplacementParams) (pgtype.UUID, error)
	GetRunHistoryTurnRequestMessageID(context.Context, sqlc.GetRunHistoryTurnRequestMessageIDParams) (pgtype.UUID, error)
}

// persistBoundMessages is shared by step commits and terminal round commits.
// A retry copies its request before appending output; an edit writes the new
// request first. Every row has its final turn and sequence at insertion time.
// The caller owns the transaction and publishes only after it commits.
func (s *DBService) persistBoundMessages(ctx context.Context, inputs []PersistInput, replacement *TurnReplacement) ([]Message, error) {
	if len(inputs) == 0 {
		return nil, errors.New("turn persistence requires messages")
	}
	for _, input := range inputs {
		if input.SkipHistoryTurn {
			return nil, errors.New("turn persistence cannot skip turn ownership")
		}
	}
	requestID := strings.TrimSpace(inputs[0].TurnRequestMessageID)
	persisted := make([]Message, 0, len(inputs)+1)
	if replacement != nil {
		cloned, anchor, err := s.prepareBoundReplacement(ctx, inputs[0], *replacement)
		if err != nil {
			return nil, err
		}
		persisted = append(persisted, cloned...)
		requestID = anchor
	}
	for _, original := range inputs {
		input := original
		input.TurnRequestMessageID = requestID
		if replacement != nil && requestID == "" {
			if !strings.EqualFold(strings.TrimSpace(input.Role), "user") || IsInternalFeedback(input.Metadata) {
				return nil, errors.New("replacement requires a request before its output")
			}
			input.TurnID = replacement.ReplacementTurnID
			input.TurnPosition = replacement.ReplacementTurnPosition
		}
		message, err := s.persist(ctx, input)
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(strings.TrimSpace(input.Role), "user") && !IsInternalFeedback(input.Metadata) {
			requestID = message.ID
		}
		persisted = append(persisted, message)
	}
	if replacement != nil {
		if err := s.selectBoundReplacement(ctx, inputs[0], *replacement); err != nil {
			return nil, err
		}
	}
	return persisted, nil
}

// prepareBoundReplacement runs inside the first write transaction. The session
// lock keeps target validation, request copying and selection one operation.
func (s *DBService) prepareBoundReplacement(ctx context.Context, input PersistInput, replacement TurnReplacement) ([]Message, string, error) {
	if replacement.ReplacementTurnPosition == nil || *replacement.ReplacementTurnPosition <= 0 ||
		strings.TrimSpace(replacement.OldTurnID) == strings.TrimSpace(replacement.ReplacementTurnID) {
		return nil, "", errors.New("replacement requires a distinct turn and a positive position")
	}
	botID, err := dbpkg.ParseUUID(input.BotID)
	if err != nil {
		return nil, "", err
	}
	sessionID, err := dbpkg.ParseUUID(input.SessionID)
	if err != nil {
		return nil, "", err
	}
	writer, ok := s.queries.(boundReplacementQueries)
	if !ok {
		return nil, "", errors.New("message store does not support bound replacements")
	}
	if _, err := writer.LockHistoryTurnReplacementSession(ctx, sqlc.LockHistoryTurnReplacementSessionParams{
		BotID: botID, SessionID: sessionID,
	}); err != nil {
		return nil, "", fmt.Errorf("lock replacement session: %w", err)
	}
	oldTurn, err := s.queries.GetLatestVisibleHistoryTurnBySession(ctx, sessionID)
	if err != nil {
		return nil, "", err
	}
	if oldTurn.ID.String() != strings.TrimSpace(replacement.OldTurnID) {
		return nil, "", errors.New("replacement target is no longer the latest turn")
	}
	if strings.TrimSpace(replacement.RequestMessageID) == "" {
		return nil, "", nil // Edit or retry of a failed user hook writes a new request.
	}
	turnID, err := dbpkg.ParseUUID(replacement.ReplacementTurnID)
	if err != nil {
		return nil, "", err
	}
	requestID, err := dbpkg.ParseUUID(replacement.RequestMessageID)
	if err != nil {
		return nil, "", err
	}
	runID, err := parseOptionalUUID(input.RunID)
	if err != nil {
		return nil, "", err
	}
	clonedID, err := writer.CloneHistoryTurnRequestForRetry(ctx, sqlc.CloneHistoryTurnRequestForRetryParams{
		BotID: botID, SessionID: sessionID, OldTurnID: oldTurn.ID,
		RequestMessageID: requestID, TurnID: turnID,
		TurnPosition: *replacement.ReplacementTurnPosition, RunID: runID,
	})
	if err != nil {
		return nil, "", fmt.Errorf("copy retry request: %w", err)
	}
	cloned, err := s.GetByIDBySession(ctx, input.SessionID, clonedID.String())
	if err != nil {
		return nil, "", err
	}
	return []Message{cloned}, cloned.ID, nil
}

func (s *DBService) selectBoundReplacement(ctx context.Context, input PersistInput, replacement TurnReplacement) error {
	writer, ok := s.queries.(boundReplacementQueries)
	if !ok {
		return errors.New("message store does not support bound replacements")
	}
	botID, err := dbpkg.ParseUUID(input.BotID)
	if err != nil {
		return err
	}
	sessionID, err := dbpkg.ParseUUID(input.SessionID)
	if err != nil {
		return err
	}
	oldTurnID, err := dbpkg.ParseUUID(replacement.OldTurnID)
	if err != nil {
		return err
	}
	turnID, err := dbpkg.ParseUUID(replacement.ReplacementTurnID)
	if err != nil {
		return err
	}
	runID, err := parseOptionalUUID(input.RunID)
	if err != nil {
		return err
	}
	if _, err := writer.SelectBoundHistoryTurnReplacement(ctx, sqlc.SelectBoundHistoryTurnReplacementParams{
		BotID: botID, SessionID: sessionID, OldTurnID: oldTurnID,
		ReplacementTurnID: turnID, ReplacementPosition: *replacement.ReplacementTurnPosition,
		RunID: runID, Reason: toPgText(replacement.Reason),
	}); err != nil {
		return fmt.Errorf("select bound replacement: %w", err)
	}
	return s.updateReplacementSessionMetadata(ctx, input.SessionID, replacement.SessionMetadata)
}

// GetRunTurnRequestMessageID follows applied input within the run. The root
// fallback also supports inbound requests persisted before run admission.
func (s *DBService) GetRunTurnRequestMessageID(ctx context.Context, sessionID, runID, rootTurnID string) (string, error) {
	reader, ok := s.queries.(boundReplacementQueries)
	if !ok {
		return "", errors.New("message store does not support run request lookup")
	}
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return "", err
	}
	pgRunID, err := dbpkg.ParseUUID(runID)
	if err != nil {
		return "", err
	}
	pgTurnID, err := dbpkg.ParseUUID(rootTurnID)
	if err != nil {
		return "", err
	}
	id, err := reader.GetRunHistoryTurnRequestMessageID(ctx, sqlc.GetRunHistoryTurnRequestMessageIDParams{
		SessionID: pgSessionID, RunID: pgRunID, RootTurnID: pgTurnID,
	})
	return id.String(), err
}

type runHistoryQueries interface {
	GetRunsHistoryRequestMessageID(context.Context, sqlc.GetRunsHistoryRequestMessageIDParams) (pgtype.UUID, error)
	RunsWroteHistoryMessages(context.Context, sqlc.RunsWroteHistoryMessagesParams) (bool, error)
}

// runHistoryScope parses a session and the runs and turns looked up in it.
func (s *DBService) runHistoryScope(sessionID string, runIDs, turnIDs []string) (runHistoryQueries, pgtype.UUID, []pgtype.UUID, []pgtype.UUID, error) {
	reader, ok := s.queries.(runHistoryQueries)
	if !ok {
		return nil, pgtype.UUID{}, nil, nil, errors.New("message store does not support run history lookup")
	}
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return nil, pgtype.UUID{}, nil, nil, err
	}
	parse := func(ids []string) ([]pgtype.UUID, error) {
		parsed := make([]pgtype.UUID, 0, len(ids))
		for _, id := range ids {
			value, err := dbpkg.ParseUUID(id)
			if err != nil {
				return nil, err
			}
			parsed = append(parsed, value)
		}
		return parsed, nil
	}
	runs, err := parse(runIDs)
	if err != nil {
		return nil, pgtype.UUID{}, nil, nil, err
	}
	turns, err := parse(turnIDs)
	if err != nil {
		return nil, pgtype.UUID{}, nil, nil, err
	}
	return reader, pgSessionID, runs, turns, nil
}

// GetRunsRequestMessageID resolves the latest visible input the runs applied:
// a request or steer they wrote, the request of a turn they were admitted
// with, or that of a turn their rows joined.
func (s *DBService) GetRunsRequestMessageID(ctx context.Context, sessionID string, runIDs, turnIDs []string) (string, error) {
	reader, pgSessionID, runs, turns, err := s.runHistoryScope(sessionID, runIDs, turnIDs)
	if err != nil {
		return "", err
	}
	id, err := reader.GetRunsHistoryRequestMessageID(ctx, sqlc.GetRunsHistoryRequestMessageIDParams{SessionID: pgSessionID, RunIds: runs, TurnIds: turns})
	return id.String(), err
}

// RunsWroteHistory reports whether any of the runs or turns left a history
// row in the session, whether or not that row is still visible.
func (s *DBService) RunsWroteHistory(ctx context.Context, sessionID string, runIDs, turnIDs []string) (bool, error) {
	reader, pgSessionID, runs, turns, err := s.runHistoryScope(sessionID, runIDs, turnIDs)
	if err != nil {
		return false, err
	}
	return reader.RunsWroteHistoryMessages(ctx, sqlc.RunsWroteHistoryMessagesParams{SessionID: pgSessionID, RunIds: runs, TurnIds: turns})
}
