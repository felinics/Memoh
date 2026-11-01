package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

type continuationTurnRequestMessageResolver interface {
	GetRunTurnRequestMessageID(context.Context, string, string, string) (string, error)
}

// runRequesterUserID is the account the run was admitted for, read from the
// execution intent every fenced run records with its admission (the same
// record a restart resumes from). A decision continuation rebuilds the run's
// config for this account rather than for whoever decided, so user-scoped
// inputs such as model credentials stay those of the run's first request and
// every request of the run keeps the same prefix. A run admitted for no
// account, or one that recorded no intent, yields "".
func (s *Service) runRequesterUserID(ctx context.Context, runID string) (string, error) {
	if s == nil || s.queries == nil || strings.TrimSpace(runID) == "" {
		return "", nil
	}
	row, err := s.queries.GetSessionRun(ctx, db.ParseUUIDOrEmpty(runID))
	if err != nil {
		return "", fmt.Errorf("load run admission: %w", err)
	}
	admitted, err := decodeRunAdmission(row.InputJson)
	if err != nil {
		return "", fmt.Errorf("decode run admission: %w", err)
	}
	if admitted.Resume == nil {
		return "", nil
	}
	return strings.TrimSpace(admitted.Resume.UserID), nil
}

func (s *Service) continuationTurnRequestMessageID(ctx context.Context, sessionID string, runHandle sessionruntime.RunHandle) (string, error) {
	turnID := strings.TrimSpace(runHandle.TurnID)
	if turnID == "" || s == nil || s.messageService == nil {
		return "", nil
	}
	resolver, ok := s.messageService.(continuationTurnRequestMessageResolver)
	if !ok {
		return "", nil
	}
	// An applied steer opens a later turn within this run. Its next tool
	// result and resumed model output belong there, even though admission's
	// run handle still names the original request turn.
	runID := strings.TrimSpace(runHandle.RunID)
	id, err := resolver.GetRunTurnRequestMessageID(ctx, sessionID, runID, turnID)
	if !errors.Is(err, pgx.ErrNoRows) {
		return id, err
	}
	// A continuation that has applied no new input carries its request in its
	// own intent. Validate that the request is still visible in this session.
	if s.queries == nil {
		return "", err
	}
	row, loadErr := s.queries.GetSessionRun(ctx, db.ParseUUIDOrEmpty(runID))
	if loadErr != nil {
		return "", loadErr
	}
	admitted, decodeErr := decodeRunAdmission(row.InputJson)
	if decodeErr != nil {
		return "", decodeErr
	}
	if admitted.Resume == nil || strings.TrimSpace(admitted.Resume.RequestMessageID) == "" {
		return "", err
	}
	request, requestErr := s.recordedResumeRequest(ctx, sessionID, admitted.Resume.RequestMessageID)
	return request.messageID, requestErr
}

func (s *Service) prepareContinuationRunConfig(
	ctx context.Context,
	base native.RunConfig,
	fallback historyfrag.ScopeFallback,
	summaryScope contextfrag.Scope,
	eventCh chan<- WSStreamEvent,
	req ChatRequest,
) (native.RunConfig, error) {
	read := func() ([]ModelMessage, []historyfrag.HistoryRecord, bool, error) {
		loaded, truncated, err := s.loadHistoryRecords(ctx, fallback, summaryScope, 0, historyCut{})
		if err != nil {
			return nil, nil, false, err
		}
		messages, retained := historyMessagesFromRecords(projectInterruptedHistoryReasoning(pruneHistoryForGateway(loaded)))
		return messages, retained, truncated, nil
	}
	messages, retained, truncated, err := read()
	if err != nil {
		return native.RunConfig{}, err
	}
	req.inputInHistory = true
	if req.continuationToolCallID != "" && req.RunHandle.RunID != "" {
		ids, err := s.queries.ListRunHistoryTurnRequestMessageIDs(ctx, sqlc.ListRunHistoryTurnRequestMessageIDsParams{
			SessionID: db.ParseUUIDOrEmpty(req.ThreadID), RunID: db.ParseUUIDOrEmpty(req.RunHandle.RunID), RootTurnID: db.ParseUUIDOrEmpty(req.RunHandle.TurnID),
		})
		if err != nil {
			return native.RunConfig{}, err
		}
		for _, id := range ids {
			req.continuationAnchors = append(req.continuationAnchors, turn.ContextMessageSource{Kind: "history", ID: id.String()})
		}
	}
	if truncated {
		// A load the byte budget truncated would start later than its
		// checkpoint's position: the rows it left out fold from rows first,
		// and the history loads again.
		pressure := 0
		for _, message := range messages {
			pressure += estimateMessageTokens(message)
		}
		if err := s.compactForBudget(ctx, req, pressure, base.ContextBudgetMaxTokens, base.CurrentModelUUID); err != nil {
			return native.RunConfig{}, truncatedLoadFailure(err)
		}
		if messages, retained, truncated, err = read(); err == nil && truncated {
			err = truncatedLoadFailure(errors.New("compacted history still exceeds the load budget"))
		}
		if err != nil {
			return native.RunConfig{}, err
		}
	}
	messages = sanitizeMessages(messages)
	historyEstimates := make([]int, len(messages))
	for i := range messages {
		historyEstimates[i] = estimateMessageTokens(messages[i])
	}
	base.ContextHistoryTokenEstimates = historyEstimates
	base.ContextTrimmableMessages = len(messages)

	base.ContextFrags = historyContextFragsForMessages(messages, retained)
	// Close any tool call left open by an interrupted turn before the transcript
	// reaches providers that enforce strict assistant-tool adjacency. A process
	// restart can orphan a deferred ask_user / tool-approval call while a later
	// request still completes normally; repairing here (not in ContextFrags)
	// keeps the fragments faithful to history while the outgoing messages stay
	// provider-valid. Applies to every continuation path that resumes after a
	// deferred tool call. Provenance names the outgoing messages, closures
	// included, so a fold removes each closure with its call.
	outgoing := repairToolCallClosures(nonNilModelMessages(messages), syntheticToolClosureError)
	base.ContextProvenance = contextProvenance(outgoing, retained, append(compactionInputs(req), req.continuationAnchors...))
	base.Messages = modelMessagesToSDKMessages(outgoing)
	base.ContextCurrentUserMessageIndex = nil
	base.ContextMemoryMessageIndex = nil
	base.Query = ""
	base.LiveToolStream = eventCh != nil
	base.CanRequestUserInput = s.canDeliverUserInputWS(eventCh)
	return s.prepareRunConfig(ctx, base), nil
}
