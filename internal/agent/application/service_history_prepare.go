package application

import (
	"context"
	"strings"

	sdk "github.com/memohai/twilight-ai/sdk"

	historyfrag "github.com/memohai/memoh/internal/agent/context/history"
	messagepkg "github.com/memohai/memoh/internal/chat/message"
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

// projectInterruptedHistoryReasoning turns only explicitly interrupted
// reasoning into assistant text for the next model call. Durable history keeps
// the typed reasoning part; this projection covers the non-DCP fallback and
// providers that ignore reasoning_content in prior messages.
func projectInterruptedHistoryReasoning(records []historyfrag.HistoryRecord) []historyfrag.HistoryRecord {
	var projected []historyfrag.HistoryRecord
	for i, record := range records {
		if !strings.EqualFold(strings.TrimSpace(record.ModelMessage.Role), "assistant") ||
			record.Metadata[messagepkg.AgentStepInterruptedMetadataKey] != true {
			continue
		}
		message := modelMessageToSDKMessage(record.ModelMessage)
		var reasoning strings.Builder
		parts := make([]sdk.MessagePart, 0, len(message.Content))
		for _, part := range message.Content {
			switch p := part.(type) {
			case sdk.ReasoningPart:
				reasoning.WriteString(p.Text)
			case *sdk.ReasoningPart:
				reasoning.WriteString(p.Text)
			default:
				parts = append(parts, part)
			}
		}
		if strings.TrimSpace(reasoning.String()) == "" {
			continue
		}
		checkpoint := messagepkg.AgentStepInterruptedReasoningPrefix + reasoning.String()
		merged := false
		for j, part := range parts {
			switch p := part.(type) {
			case sdk.TextPart:
				p.Text = checkpoint + "\n\n" + p.Text
				parts[j], merged = p, true
			case *sdk.TextPart:
				clone := *p
				clone.Text = checkpoint + "\n\n" + clone.Text
				parts[j], merged = clone, true
			}
			if merged {
				break
			}
		}
		if !merged {
			parts = append([]sdk.MessagePart{sdk.TextPart{Text: checkpoint}}, parts...)
		}
		message.Content = parts
		converted := sdkMessagesToModelMessages([]sdk.Message{message})[0]
		converted.Usage = record.ModelMessage.Usage
		converted.ToolCalls = record.ModelMessage.ToolCalls
		converted.ToolCallID = record.ModelMessage.ToolCallID
		converted.Name = record.ModelMessage.Name
		if projected == nil {
			projected = append([]historyfrag.HistoryRecord(nil), records...)
		}
		projected[i].ModelMessage = converted
	}
	if projected != nil {
		return projected
	}
	return records
}
