package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	chatview "github.com/felinics/memoh/internal/agent/view"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// A shutdown continuation is a run of its own, admitted with a new turn. The
// request it answers was applied by the run it resumes, or by that run's own
// predecessor when shutdowns recurred. maxRunLineage bounds the walk back.
const maxRunLineage = 8

var errRunLineageMismatch = errors.New("continued run belongs to another session")

// runRequestResolver finds the requests runs applied in history.
type runRequestResolver interface {
	GetRunsRequestMessageID(ctx context.Context, sessionID string, runIDs, turnIDs []string) (string, error)
	RunsWroteHistory(ctx context.Context, sessionID string, runIDs, turnIDs []string) (bool, error)
}

// visibleTurnReader finds the visible turn a message belongs to.
type visibleTurnReader interface {
	GetVisibleTurnByMessage(ctx context.Context, sessionID, messageID string) (messagepkg.HistoryTurn, error)
}

// runLineage is a run and the runs it continues, newest first.
type runLineage []sqlc.SessionRun

func (l runLineage) ids() ([]string, []string) {
	runIDs, turnIDs := make([]string, 0, len(l)), make([]string, 0, len(l))
	for _, run := range l {
		if run.RunID.Valid {
			runIDs = append(runIDs, run.RunID.String())
		}
		if run.TurnID.Valid {
			turnIDs = append(turnIDs, run.TurnID.String())
		}
	}
	return runIDs, turnIDs
}

// continuedRunID is the run a shutdown continuation resumes, read from its
// admission input; "" for any other run.
func continuedRunID(input []byte) string {
	var admitted struct {
		Kind             string `json:"kind"`
		InterruptedRunID string `json:"interrupted_run_id"`
	}
	if json.Unmarshal(input, &admitted) != nil || admitted.Kind != "resume" {
		return ""
	}
	return strings.TrimSpace(admitted.InterruptedRunID)
}

// lineageOf lists run and the runs it continues. Every run in it belongs to
// the same session and bot; the walk ends at a run that continues none, or
// whose predecessor is gone.
func (s *Service) lineageOf(ctx context.Context, run sqlc.SessionRun) (runLineage, error) {
	lineage := runLineage{run}
	for len(lineage) < maxRunLineage {
		previous := continuedRunID(run.InputJson)
		if previous == "" || s.queries == nil {
			break
		}
		next, err := s.queries.GetSessionRun(ctx, db.ParseUUIDOrEmpty(previous))
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("load continued run: %w", err)
		}
		if next.SessionID != run.SessionID || next.BotID != run.BotID {
			return nil, errRunLineageMismatch
		}
		run = next
		lineage = append(lineage, run)
	}
	return lineage, nil
}

// lineageRequestMessageID is the latest input applied along lineage. ""
// means none of its runs has a visible request.
func lineageRequestMessageID(ctx context.Context, resolver runRequestResolver, sessionID string, lineage runLineage) (string, error) {
	runIDs, turnIDs := lineage.ids()
	id, err := resolver.GetRunsRequestMessageID(ctx, sessionID, runIDs, turnIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return strings.TrimSpace(id), err
}

// continuedRequestMessageID resolves the request of a shutdown continuation
// that has applied no input of its own: it answers the request of the run it
// resumes. It returns "" for any other run.
func (s *Service) continuedRequestMessageID(ctx context.Context, sessionID, runID string) (string, error) {
	resolver, ok := s.messageService.(runRequestResolver)
	if !ok || s.queries == nil {
		return "", nil
	}
	run, err := s.queries.GetSessionRun(ctx, db.ParseUUIDOrEmpty(runID))
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load continuation run: %w", err)
	}
	if continuedRunID(run.InputJson) == "" {
		return "", nil
	}
	lineage, err := s.lineageOf(ctx, run)
	if err != nil {
		return "", err
	}
	return lineageRequestMessageID(ctx, resolver, sessionID, lineage)
}

// runLineageRequestMessageIDs lists every input applied by the run and by the
// runs it continues, oldest run first.
func (s *Service) runLineageRequestMessageIDs(ctx context.Context, sessionID string, handle sessionruntime.RunHandle) ([]string, error) {
	lineage := runLineage{{RunID: db.ParseUUIDOrEmpty(handle.RunID), TurnID: db.ParseUUIDOrEmpty(handle.TurnID)}}
	run, err := s.queries.GetSessionRun(ctx, db.ParseUUIDOrEmpty(handle.RunID))
	switch {
	case err == nil:
		if lineage, err = s.lineageOf(ctx, run); err != nil {
			return nil, err
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("load continuation run: %w", err)
	}
	var ids []string
	for i := len(lineage) - 1; i >= 0; i-- {
		rows, err := s.queries.ListRunHistoryTurnRequestMessageIDs(ctx, sqlc.ListRunHistoryTurnRequestMessageIDsParams{
			SessionID: db.ParseUUIDOrEmpty(sessionID), RunID: lineage[i].RunID, RootTurnID: lineage[i].TurnID,
		})
		if err != nil {
			return nil, err
		}
		for _, id := range rows {
			ids = append(ids, id.String())
		}
	}
	return ids, nil
}

// resumeRequest is the request row a shutdown continuation answers.
type resumeRequest struct {
	// messageID is the request the interrupted run was answering.
	messageID string
	// restore means the interrupted run stopped before it wrote its request:
	// the continuation writes it under its own turn before running.
	restore bool
}

// interruptedRunRequest finds the request an interrupted run was answering, so
// every row its continuation writes, internal feedback included, joins that
// turn. History decides: the latest input the run (or a run it continues)
// applied, then the request it recorded when that was written before its run
// was admitted. Intents saved before the request was recorded resolve from
// history alone.
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
	lineage, err := s.lineageOf(ctx, row)
	if errors.Is(err, errRunLineageMismatch) {
		return resumeRequest{}, fmt.Errorf("%w: %w", errResumeUnrecoverable, err)
	}
	if err != nil {
		return resumeRequest{}, err
	}
	id, err := lineageRequestMessageID(ctx, resolver, sessionID, lineage)
	if err != nil {
		return resumeRequest{}, fmt.Errorf("resolve interrupted request: %w", err)
	}
	if id != "" {
		return resumeRequest{messageID: id}, nil
	}
	if recorded := strings.TrimSpace(data.RequestMessageID); recorded != "" {
		return s.recordedResumeRequest(ctx, sessionID, recorded)
	}
	runIDs, turnIDs := lineage.ids()
	wrote, err := resolver.RunsWroteHistory(ctx, sessionID, runIDs, turnIDs)
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
