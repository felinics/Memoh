package application

import (
	"context"
	"strings"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
)

type continuationTurnRequestMessageResolver interface {
	GetRunTurnRequestMessageID(context.Context, string, string, string) (string, error)
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
) (native.RunConfig, error) {
	loaded, err := s.loadHistoryRecords(ctx, fallback, summaryScope.SessionID, 0)
	if err != nil {
		return native.RunConfig{}, err
	}
	loaded = pruneHistoryForGateway(loaded)
	loaded, err = s.replaceCompactedMessages(ctx, summaryScope.SessionID, summaryScope, loaded, compactionArtifactBoundary{})
	if err != nil {
		return native.RunConfig{}, err
	}
	loaded = projectInterruptedHistoryReasoning(loaded)
	messages, retained, _ := trimMessagesAndRecordsByTokens(s.logger, loaded, 0)
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
	// deferred tool call.
	base.Messages = modelMessagesToSDKMessages(repairToolCallClosures(nonNilModelMessages(messages), syntheticToolClosureError))
	base.ContextCurrentUserMessageIndex = nil
	base.ContextMemoryMessageIndex = nil
	base.Query = ""
	base.LiveToolStream = eventCh != nil
	base.CanRequestUserInput = s.canDeliverUserInputWS(eventCh)
	// Continuations (ask_user answers, tool approvals) rebuild the run config
	// inside the same epoch: the pinned history budget and prefix-hash
	// accounting apply here exactly as at turn start (spec rule 4 — a resumed
	// turn must not recompute the budget from its own payload).
	s.pinHistoryBudget(ctx, summaryScope.SessionID, &base)
	return s.prepareRunConfig(ctx, base), nil
}
