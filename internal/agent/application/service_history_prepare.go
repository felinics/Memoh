package application

import (
	"context"
	"strings"

	historyfrag "github.com/memohai/memoh/internal/agent/context/history"
	messagepkg "github.com/memohai/memoh/internal/chat/message"
	"github.com/memohai/memoh/internal/chat/timeline"
)

type preparedHistoryContext struct {
	messages          []ModelMessage
	records           []historyfrag.HistoryRecord
	estimatedTokens   int
	compactableTokens int
}

func (s *Service) prepareHistoryContext(
	ctx context.Context,
	req ChatRequest,
	fallback historyfrag.ScopeFallback,
	contextTokenBudget int,
) (preparedHistoryContext, error) {
	loaded, err := s.loadHistoryRecords(ctx, fallback, req.ThreadID, defaultMaxContextMinutes, contextTokenBudget)
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
	loaded, err = s.replaceCompactedMessages(
		ctx,
		req.ThreadID,
		compactionSummaryScope(req.BotID, req.ChatID, req.ThreadID, req.ConversationType, req.ConversationName, req.ReplyTarget),
		loaded,
		boundary,
	)
	if err != nil {
		return preparedHistoryContext{}, err
	}
	loaded = projectInterruptedHistoryReasoning(loaded)
	compactableTokens := totalCompactableHistoryTokens(loaded)
	messages, records, estimatedTokens := trimMessagesAndRecordsByTokens(s.logger, loaded, contextTokenBudget)
	return preparedHistoryContext{
		messages:          messages,
		records:           records,
		estimatedTokens:   estimatedTokens,
		compactableTokens: compactableTokens,
	}, nil
}

// projectInterruptedHistoryReasoning turns still-live interrupted reasoning
// into assistant text for the next model call. Durable history keeps the typed
// reasoning part; this projection covers the non-DCP fallback and providers that
// ignore reasoning_content in prior messages. Checkpoints a completed answer
// already superseded stay hidden, same as on the DCP path.
func projectInterruptedHistoryReasoning(records []historyfrag.HistoryRecord) []historyfrag.HistoryRecord {
	checkpoint := messagepkg.LatestInterruptedCheckpoint(len(records), func(i int) (bool, bool) {
		return strings.EqualFold(strings.TrimSpace(records[i].ModelMessage.Role), "assistant"),
			records[i].Metadata[messagepkg.AgentStepInterruptedMetadataKey] == true
	})
	if checkpoint < 0 {
		return records
	}
	projected := append([]historyfrag.HistoryRecord(nil), records...)
	projected[checkpoint].ModelMessage = timeline.ProjectInterruptedReasoning(records[checkpoint].ModelMessage)
	return projected
}
