package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/felinics/memoh/internal/agent/turn"
	chatview "github.com/felinics/memoh/internal/agent/view"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// runRequestResolver reads only the interrupted run's own inputs and history.
type runRequestResolver interface {
	continuationTurnRequestMessageResolver
	RunWroteHistory(ctx context.Context, sessionID, runID, turnID string) (bool, error)
}

type visibleTurnReader interface {
	GetVisibleTurnByMessage(ctx context.Context, sessionID, messageID string) (messagepkg.HistoryTurn, error)
}

// resumeRequest is the request row a shutdown continuation answers.
type resumeRequest struct {
	// messageID is the request the interrupted run was answering.
	messageID string
	// restore means the interrupted run stopped before it wrote its request:
	// the continuation writes it under its own turn before running.
	restore bool
}

// Prefer input the run applied itself (including a steer or restored request),
// then its recorded request. Each continuation carries that anchor forward.
func (s *Service) interruptedRunRequest(ctx context.Context, row sqlc.SessionRun, data *resumeContext) (resumeRequest, error) {
	// A Discuss run answers rows its ingress already wrote; it never has a
	// request row of its own to resolve or restore.
	if data.SessionType == string(turn.ModeDiscuss) {
		return resumeRequest{}, nil
	}
	resolver, ok := s.messageService.(runRequestResolver)
	if !ok {
		return resumeRequest{}, nil
	}
	sessionID := row.SessionID.String()
	id, err := resolver.GetRunTurnRequestMessageID(ctx, sessionID, row.RunID.String(), row.TurnID.String())
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return resumeRequest{}, fmt.Errorf("resolve interrupted request: %w", err)
	}
	if id != "" {
		return resumeRequest{messageID: id}, nil
	}
	if recorded := strings.TrimSpace(data.RequestMessageID); recorded != "" {
		return s.recordedResumeRequest(ctx, sessionID, recorded)
	}
	wrote, err := resolver.RunWroteHistory(ctx, sessionID, row.RunID.String(), row.TurnID.String())
	if err != nil {
		return resumeRequest{}, fmt.Errorf("inspect interrupted history: %w", err)
	}
	if wrote {
		// The run wrote history whose request is no longer visible. Answering
		// it would file the continuation under some other turn.
		return resumeRequest{}, fmt.Errorf("%w: interrupted request is no longer in history", errResumeUnrecoverable)
	}
	return resumeRequest{restore: true}, nil
}

// recordedResumeRequest accepts the request an intent recorded only while it
// is still the request of a visible turn in the session.
func (s *Service) recordedResumeRequest(ctx context.Context, sessionID, messageID string) (resumeRequest, error) {
	reader, ok := s.messageService.(visibleTurnReader)
	if !ok {
		return resumeRequest{}, nil
	}
	historyTurn, err := reader.GetVisibleTurnByMessage(ctx, sessionID, messageID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && historyTurn.RequestMessageID != messageID {
		return resumeRequest{}, fmt.Errorf("%w: interrupted request is no longer in history", errResumeUnrecoverable)
	}
	if err != nil {
		return resumeRequest{}, fmt.Errorf("resolve recorded request: %w", err)
	}
	return resumeRequest{messageID: messageID}, nil
}

// streamResumedTurn starts the continuation bound to the request it answers.
// A request that cannot be restored fails the admitted run before any model
// call, rather than running it with rows that belong to no turn.
func (s *Service) streamResumedTurn(ctx context.Context, req ChatRequest, data *resumeContext, request resumeRequest) (<-chan StreamChunk, <-chan error) {
	req.PersistedUserMessageID = request.messageID
	if request.restore {
		id, err := s.restoreInterruptedRequest(ctx, req, data)
		if err != nil {
			chunks, errs := make(chan StreamChunk), make(chan error, 1)
			close(chunks)
			errs <- err
			close(errs)
			return chunks, errs
		}
		req.PersistedUserMessageID = id
	}
	return s.streamTurnChat(ctx, req)
}

// restoreInterruptedRequest writes the request an interrupted run never
// persisted, under the continuation's admitted turn. It holds the original
// task, not the resume instruction the model is given around it.
func (s *Service) restoreInterruptedRequest(ctx context.Context, req ChatRequest, data *resumeContext) (string, error) {
	restored := ChatRequest{
		BotID: req.BotID, ChatID: req.ChatID, ThreadID: req.ThreadID,
		RunID: req.RunID, TurnID: req.TurnID, TurnPosition: req.TurnPosition,
		Query: data.Query, UserVisibleText: turn.UnwrapUserMessageEnvelope(data.Query),
		UserID: req.UserID, SourceChannelIdentityID: req.SourceChannelIdentityID,
		CurrentChannel: req.CurrentChannel, ReplyTarget: req.ReplyTarget, ConversationType: req.ConversationType,
		SessionType: req.SessionType, WorkspaceTargetID: req.WorkspaceTargetID,
	}
	persisted, err := s.persistUserTurn(ctx, restored)
	if err != nil {
		return "", fmt.Errorf("restore interrupted request: %w", err)
	}
	return persisted.ID, nil
}

// resumedHistoryTurn publishes the same response identity and prefix as REST
// history. Loading it before admission leaves the old intent retryable on errors.
func (s *Service) resumedHistoryTurn(ctx context.Context, sessionID, requestID string) (*chatview.UITurn, error) {
	if requestID == "" {
		return nil, nil
	}
	reader, ok := s.messageService.(visibleTurnReader)
	if !ok {
		return nil, nil
	}
	historyTurn, err := reader.GetVisibleTurnByMessage(ctx, sessionID, requestID)
	if err != nil {
		return nil, err
	}
	rows, err := s.messageService.ListVisibleFromBySession(ctx, sessionID, requestID)
	if err != nil {
		return nil, err
	}
	for _, t := range chatview.ConvertMessagesToUITurns(rows) {
		if t.Role == "assistant" && t.TurnID == historyTurn.ID {
			return &t, nil
		}
	}
	return &chatview.UITurn{TurnID: historyTurn.ID, TurnPosition: &historyTurn.Position, Role: "assistant", Timestamp: historyTurn.CreatedAt}, nil
}
