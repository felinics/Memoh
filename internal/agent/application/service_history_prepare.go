package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/apperror"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/chat/timeline"
	"github.com/felinics/memoh/internal/errs"
)

type preparedHistoryContext struct {
	messages       []ModelMessage
	records        []historyfrag.HistoryRecord
	pressureTokens int
	// truncated reports that the load's byte budget left rows after the
	// checkpoint out.
	truncated bool
}

// loadChatHistory loads the history a chat request renders, composed from
// the pipeline or read from the session's rows. A load the byte budget
// truncated would render a history that starts later than its checkpoint's
// position, so the rows it left out are folded from rows first
// (compactForBudget) and the history loads again; one still truncated after
// that fails the request.
func (s *Service) loadChatHistory(ctx context.Context, req ChatRequest, usePipeline bool, contextTokenBudget int, modelID string) (preparedHistoryContext, error) {
	load := func() (preparedHistoryContext, error) {
		if usePipeline {
			messages, pressure, truncated, err := s.buildMessagesFromPipeline(ctx, req, contextTokenBudget)
			return preparedHistoryContext{messages: messages, pressureTokens: pressure, truncated: truncated}, err
		}
		return s.prepareHistoryContext(ctx, req, historyScopeFallbackFromChatRequest(req), contextTokenBudget)
	}
	history, err := load()
	if err != nil || !history.truncated {
		return history, err
	}
	if err := s.compactForBudget(ctx, req, history.pressureTokens, contextTokenBudget, modelID); err != nil {
		return preparedHistoryContext{}, truncatedLoadFailure(err)
	}
	if history, err = load(); err == nil && history.truncated {
		err = truncatedLoadFailure(errors.New("compacted history still exceeds the load budget"))
	}
	return history, err
}

// truncatedLoadFailure is the failure of a request whose truncated history
// could not be folded before it runs: the budget's, unless the compaction
// failed on the provider's account (budgetFailureCode).
func truncatedLoadFailure(cause error) error {
	return apperror.Wrap(budgetFailureCode(cause), fmt.Errorf("%w: %w", contextfrag.ErrBudgetUnsatisfied, cause), nil)
}

// prepareHistoryContext loads the history a request renders: the history
// under the session's checkpoint, with what it must keep raw and without
// what never replays. An edit or retry renders the history as it stood at
// its cutoff, under the checkpoint its replacement keeps.
func (s *Service) prepareHistoryContext(
	ctx context.Context,
	req ChatRequest,
	fallback historyfrag.ScopeFallback,
	contextTokenBudget int,
) (preparedHistoryContext, error) {
	cut, err := s.loadHistoryCut(ctx, req)
	if err != nil {
		return preparedHistoryContext{}, err
	}
	scope := compactionSummaryScope(req.BotID, req.ChatID, req.ThreadID, req.ConversationType, req.ConversationName, req.ReplyTarget)
	loaded, truncated, err := s.loadHistoryRecords(ctx, fallback, scope, contextTokenBudget, cut)
	if err != nil {
		return preparedHistoryContext{}, err
	}
	loaded = pruneHistoryForGateway(loaded)
	loaded = dropPreflightFailureTurns(loaded)
	loaded = dropEmptyHistoryFailures(loaded)
	loaded = dedupePersistedCurrentUserMessage(loaded, req)
	loaded, err = s.ensureRequiredHistoryMessage(ctx, loaded, req)
	if err != nil {
		return preparedHistoryContext{}, err
	}
	loaded = projectInterruptedHistoryReasoning(loaded)
	messages, records := historyMessagesFromRecords(loaded)
	return preparedHistoryContext{
		messages:       messages,
		records:        records,
		pressureTokens: estimateMessagesTokens(loaded),
		truncated:      truncated,
	}, nil
}

// interruptedPreparation reports a request whose context ended while it was
// being prepared, naming the step it was in. Preparation between database
// calls is CPU-bound and never sees ctx otherwise, so a turn past its budget
// would keep assembling a request nobody sends.
func interruptedPreparation(ctx context.Context, step string) error {
	if ctx.Err() == nil {
		return nil
	}
	return errs.WrapWithDepth(1, context.Cause(ctx), step)
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
