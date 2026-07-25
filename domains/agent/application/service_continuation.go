package application

import (
	"context"

	"github.com/memohai/memoh/domains/agent/chat/context/fragment"
	"github.com/memohai/memoh/domains/agent/chat/context/history"
	"github.com/memohai/memoh/domains/agent/engine"
)

func (s *Service) prepareContinuationRunConfig(
	ctx context.Context,
	base engine.RunConfig,
	fallback history.ScopeFallback,
	summaryScope fragment.Scope,
	eventCh chan<- WSStreamEvent,
) (engine.RunConfig, error) {
	loaded, err := s.loadHistoryRecords(ctx, fallback, summaryScope.SessionID, defaultMaxContextMinutes)
	if err != nil {
		return engine.RunConfig{}, err
	}
	loaded = pruneHistoryForGateway(loaded)
	loaded, err = s.replaceCompactedMessages(ctx, summaryScope.SessionID, summaryScope, loaded, compactionArtifactBoundary{})
	if err != nil {
		return engine.RunConfig{}, err
	}
	messages, retained, _ := trimMessagesAndRecordsByTokens(s.logger, loaded, 0)
	messages = sanitizeMessages(messages)

	base.ContextFrags = historyContextFragsForMessages(messages, retained)
	// Close any tool call left open by an interrupted turn before the transcript
	// reaches providers that enforce strict assistant-tool adjacency. A process
	// restart can orphan a deferred ask_user / tool-approval call while a later
	// request still completes normally; repairing here (not in ContextFrags)
	// keeps the fragments faithful to history while the outgoing messages stay
	// provider-valid. Applies to every continuation path that resumes after a
	// deferred tool call.
	base.Messages = modelMessagesToSDKMessages(repairToolCallClosures(nonNilModelMessages(messages), syntheticToolClosureError))
	base.Query = ""
	base.LiveToolStream = eventCh != nil
	base.CanRequestUserInput = s.canDeliverUserInputWS(eventCh)
	return s.prepareRunConfig(ctx, base), nil
}
