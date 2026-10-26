package application

import (
	"context"
	"fmt"
	"slices"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

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
	return resolver.GetRunTurnRequestMessageID(ctx, sessionID, strings.TrimSpace(runHandle.RunID), turnID)
}

func (s *Service) prepareContinuationRunConfig(
	ctx context.Context,
	base native.RunConfig,
	fallback historyfrag.ScopeFallback,
	summaryScope contextfrag.Scope,
	eventCh chan<- WSStreamEvent,
	req ChatRequest,
) (native.RunConfig, error) {
	loaded, err := s.loadHistoryRecords(ctx, fallback, summaryScope.SessionID, 0, false)
	if err != nil {
		return native.RunConfig{}, err
	}
	loaded = pruneHistoryForGateway(loaded)
	req.inputInHistory = true
	loaded, err = s.replaceCompactedMessages(ctx, summaryScope.SessionID, summaryScope, loaded, compactionArtifactBoundary{})
	if err != nil {
		return native.RunConfig{}, err
	}
	loaded = projectInterruptedHistoryReasoning(loaded)
	messages, retained := historyMessagesFromRecords(loaded)
	if req.continuationToolCallID != "" && req.RunHandle.RunID != "" {
		start := slices.IndexFunc(retained, func(record historyfrag.HistoryRecord) bool {
			return slices.ContainsFunc(extractAssistantToolCallParts(record.ModelMessage), func(call sdk.ToolCallPart) bool {
				return call.ToolCallID == req.continuationToolCallID
			})
		})
		if start < 0 {
			return native.RunConfig{}, fmt.Errorf("%w: deferred call missing from continuation", contextfrag.ErrBudgetUnsatisfied)
		}
		for _, record := range retained[start:] {
			if record.DBMessageID != "" {
				req.continuationTail = append(req.continuationTail, turn.ContextMessageSource{Kind: "history", ID: record.DBMessageID})
			}
		}
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
	messages = sanitizeMessages(messages)
	historyEstimates := make([]int, len(messages))
	pressure := 0
	for i := range messages {
		historyEstimates[i] = estimateMessageTokens(messages[i])
		pressure += historyEstimates[i]
	}
	base.ContextHistoryTokenEstimates = historyEstimates
	base.ContextTrimmableMessages = len(messages)

	base.ContextFrags = historyContextFragsForMessages(messages, retained)
	base.ContextProvenance = contextProvenance(messages, retained, append(compactionProtectedSources(req), req.continuationAnchors...))
	// Close any tool call left open by an interrupted turn before the transcript
	// reaches providers that enforce strict assistant-tool adjacency. A process
	// restart can orphan a deferred ask_user / tool-approval call while a later
	// request still completes normally; repairing here (not in ContextFrags)
	// keeps the fragments faithful to history while the outgoing messages stay
	// provider-valid. Applies to every continuation path that resumes after a
	// deferred tool call.
	base.Messages = modelMessagesToSDKMessages(repairToolCallClosures(nonNilModelMessages(messages), syntheticToolClosureError))
	base.ContextCurrentUserMessageIndex = nil
	base.ContextMemoryMessageIndex = nil
	base.Query = ""
	base.LiveToolStream = eventCh != nil
	base.CanRequestUserInput = s.canDeliverUserInputWS(eventCh)
	base.RecoverContextBudget = s.chatBudgetRecovery(req, chatHistoryLayout{historyCount: len(messages), pressureTokens: pressure})
	return s.prepareRunConfig(ctx, base), nil
}
