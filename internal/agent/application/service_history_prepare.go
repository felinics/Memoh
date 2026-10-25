package application

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/chat/timeline"
)

type preparedHistoryContext struct {
	messages       []ModelMessage
	records        []historyfrag.HistoryRecord
	pressureTokens int
}

// prepareHistoryContext loads the history a request renders. Its window
// folds the rows a summary replaced, so the rows the summary does not cover,
// such as an earlier input kept beside it, stay inside the budget. An edit or
// retry renders history before its cutoff, where a summary that reaches past
// the cutoff does not render, so its window does not fold; nor does a window
// in which a folded group's summary turns out not to render.
func (s *Service) prepareHistoryContext(
	ctx context.Context,
	req ChatRequest,
	fallback historyfrag.ScopeFallback,
	contextTokenBudget int,
) (preparedHistoryContext, error) {
	fold := strings.TrimSpace(req.HistoryCutoffBeforeMessageID) == ""
	prepared, err := s.prepareHistoryWindow(ctx, req, fallback, contextTokenBudget, fold)
	if fold && errors.Is(err, errSummaryNotRendered) {
		if s.logger != nil {
			s.logger.WarnContext(ctx, "history window reloaded without folding", slog.String("session_id", req.ThreadID), slog.Any("error", err))
		}
		return s.prepareHistoryWindow(ctx, req, fallback, contextTokenBudget, false)
	}
	return prepared, err
}

func (s *Service) prepareHistoryWindow(
	ctx context.Context,
	req ChatRequest,
	fallback historyfrag.ScopeFallback,
	contextTokenBudget int,
	fold bool,
) (preparedHistoryContext, error) {
	loaded, err := s.loadHistoryRecords(ctx, fallback, req.ThreadID, contextTokenBudget, fold)
	if err != nil {
		return preparedHistoryContext{}, err
	}
	loaded = pruneHistoryForGateway(loaded)
	loaded = dropPreflightFailureTurns(loaded)
	loaded = dropEmptyHistoryFailures(loaded)
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
	messages, records := historyMessagesFromRecords(loaded)
	return preparedHistoryContext{
		messages:       messages,
		records:        records,
		pressureTokens: estimateMessagesTokens(loaded),
	}, nil
}

func historyErrorCode(meta map[string]any) string {
	if meta == nil {
		return ""
	}
	code, _ := meta[messagepkg.HistoryErrorCodeMetadataKey].(string)
	return strings.TrimSpace(code)
}

// dropPreflightFailureTurns removes inputs rejected before the agent accepted
// them. They stay in the UI, but a later ordinary message must not smuggle a
// Hook-rejected input into the model context.
func dropPreflightFailureTurns(records []historyfrag.HistoryRecord) []historyfrag.HistoryRecord {
	if len(records) == 0 {
		return records
	}
	out := make([]historyfrag.HistoryRecord, 0, len(records))
	for _, rec := range records {
		if rec.Metadata != nil && rec.Metadata[messagepkg.HistoryFailureOriginMetadataKey] == messagepkg.HistoryFailureOriginUserMessageHook {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// dropEmptyHistoryFailures removes empty assistant rows that exist only to
// mark a timeout/interrupt. They stay in the UI, but replaying them as
// empty assistant turns is what made the next provider call 400.
func dropEmptyHistoryFailures(records []historyfrag.HistoryRecord) []historyfrag.HistoryRecord {
	if len(records) == 0 {
		return records
	}
	out := make([]historyfrag.HistoryRecord, 0, len(records))
	for _, rec := range records {
		if strings.EqualFold(strings.TrimSpace(rec.ModelMessage.Role), "assistant") &&
			historyErrorCode(rec.Metadata) != "" &&
			isEmptyAssistantMessage(rec.ModelMessage) {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// projectInterruptedHistoryReasoning turns interrupted reasoning into assistant
// text. Durable history keeps the typed reasoning part; every checkpoint is
// projected the same way on every turn, so a superseded one neither rewrites
// the replayed prefix nor reaches the provider as unsigned reasoning.
func projectInterruptedHistoryReasoning(records []historyfrag.HistoryRecord) []historyfrag.HistoryRecord {
	var projected []historyfrag.HistoryRecord
	for i, record := range records {
		if !strings.EqualFold(strings.TrimSpace(record.ModelMessage.Role), "assistant") ||
			record.Metadata[messagepkg.AgentStepInterruptedMetadataKey] != true {
			continue
		}
		if projected == nil {
			projected = append([]historyfrag.HistoryRecord(nil), records...)
		}
		projected[i].ModelMessage = timeline.ProjectInterruptedReasoning(record.ModelMessage)
	}
	if projected == nil {
		return records
	}
	return projected
}
