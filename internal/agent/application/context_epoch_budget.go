package application

import (
	"context"
	"log/slog"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/native"
)

// pinHistoryBudget wires the Context Epoch's pinned history budget (spec rule
// 4) into the run config: a longer current request may never shrink the
// history budget within an epoch. The first computed budget of the epoch is
// recorded through the guarded pin write; later turns reuse the pinned value
// and the selector's per-turn feedback no longer applies to history.
func (s *Service) pinHistoryBudget(ctx context.Context, sessionID string, cfg *native.RunConfig) {
	if s == nil || s.contextEpochs == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	state, err := s.contextEpochs.Load(ctx, sessionID)
	if err != nil {
		// Degrade to the computed budget: a transient metadata read failure
		// must not fail the turn, and the pin write retries next turn.
		s.logger.WarnContext(ctx, "context_epoch_load_failed",
			slog.String("path", "budget_pin"),
			slog.String("session_id", sessionID),
			slog.Any("error", err))
		return
	}
	if state.BudgetTokens > 0 {
		cfg.PinnedHistoryBudgetTokens = int(state.BudgetTokens)
	}
	cfg.RecordHistoryBudgetPin = func(pinCtx context.Context, tokens int) {
		if _, err := s.contextEpochs.PinBudget(pinCtx, sessionID, int64(tokens)); err != nil {
			s.logger.WarnContext(pinCtx, "context_epoch_budget_pin_failed",
				slog.String("session_id", sessionID),
				slog.Int("budget_tokens", tokens),
				slog.Any("error", err))
		}
	}
}
