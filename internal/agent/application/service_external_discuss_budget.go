package application

import (
	"context"
	"log/slog"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/apperror"
)

func (s *Service) prepareExternalDiscussContext(ctx context.Context, req ChatRequest, markdown string, imageCount int) (ChatRequest, error) {
	if req.discussMessages == nil {
		return req, nil
	}
	if err := ctx.Err(); err != nil {
		return req, err
	}
	admitted, admission := admitDiscussAgentContext(req.discussMessages, s.contextAbsoluteMaxTokens(), len(markdown), imageCount)
	if admission.ProtectedOverflow {
		if admission.RecoveryBudgetTokens > 0 && s.effectiveSyncCompactionMode() != syncCompactionModeOff && s.compactionService != nil && s.settingsService != nil {
			result := s.runBudgetCompactionSync(ctx, req, max(req.discussContextTokens, admission.EstimatedTokens), admission.RecoveryBudgetTokens, "")
			if err := ctx.Err(); err != nil {
				return req, err
			}
			if result.Status == compaction.StatusOK || result.Status == compaction.StatusProgress {
				return req, native.ErrContextRecompose
			}
		}
		return req, apperror.New(apperror.CodeContextProtectedOverflow, nil)
	}
	if admission.DroppedMessages > 0 && s.logger != nil {
		s.logger.InfoContext(ctx, "context_admission",
			slog.String("path", "discuss_external_context"),
			slog.Int("selected_tokens", admission.SelectedTokens),
			slog.Int("budget_tokens", admission.BudgetTokens),
			slog.Int("dropped_messages", admission.DroppedMessages))
	}
	req.Query = discussAgentFullContextPrompt(admitted)
	req.RawQuery = req.Query
	req.discussMessages = admitted
	return req, nil
}
