package application

import (
	"context"

	historyfrag "github.com/memohai/memoh/internal/agent/context/history"
)

type preparedHistoryContext struct {
	messages                 []ModelMessage
	records                  []historyfrag.HistoryRecord
	estimatedTokens          int
	compactableTokens        int
	compactionInputTokens    int
	compactionArtifactIDs    []string
	compactionArtifactsKnown bool
}

func (s *Service) prepareHistoryContext(
	ctx context.Context,
	req ChatRequest,
	fallback historyfrag.ScopeFallback,
	contextTokenBudget int,
) (preparedHistoryContext, error) {
	loaded, err := s.loadHistoryRecords(ctx, fallback, req.ThreadID, defaultMaxContextMinutes)
	if err != nil {
		return preparedHistoryContext{}, err
	}
	loaded = pruneHistoryForGateway(loaded)
	boundary := s.loadCompactionArtifactBoundary(ctx, loaded, req.ThreadID, req.HistoryCutoffBeforeMessageID)
	loaded = filterMessagesBeforeID(loaded, req.HistoryCutoffBeforeMessageID)
	loaded = dedupePersistedCurrentUserMessage(loaded, req)
	loaded, err = s.ensureRequiredHistoryMessage(ctx, loaded, req)
	if err != nil {
		return preparedHistoryContext{}, err
	}
	var compactionArtifactIDs []string
	var compactionArtifactsKnown bool
	loaded, compactionArtifactIDs, compactionArtifactsKnown, err = s.replaceCompactedMessagesWithSnapshot(
		ctx,
		req.ThreadID,
		compactionSummaryScope(req.BotID, req.ChatID, req.ThreadID, req.ConversationType, req.ConversationName, req.ReplyTarget),
		loaded,
		boundary,
	)
	if err != nil {
		return preparedHistoryContext{}, err
	}
	compactableTokens := totalCompactableHistoryTokens(loaded)
	compactionInputTokens := estimateMessagesTokens(loaded)
	loaded = injectWorkspaceTransitionRecords(loaded)
	messages, records, estimatedTokens := trimMessagesAndRecordsByTokens(s.logger, loaded, contextTokenBudget)
	return preparedHistoryContext{
		messages:                 messages,
		records:                  records,
		estimatedTokens:          estimatedTokens,
		compactableTokens:        compactableTokens,
		compactionInputTokens:    compactionInputTokens,
		compactionArtifactIDs:    compactionArtifactIDs,
		compactionArtifactsKnown: compactionArtifactsKnown,
	}, nil
}
