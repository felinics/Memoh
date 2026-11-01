package application

import (
	"context"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/runtimekind"
)

func discussContextPressure(cmd turn.StartTurnCommand) int {
	total := 0
	for _, message := range cmd.DiscussMessages {
		total += discussMessageTokens(message)
	}
	return max(total, cmd.DiscussContextTokens)
}

// maybeSyncCompactDiscuss compacts before the turn when Channel could not fit
// even the newest input beside the summaries and sent no messages to run: the
// batch stays, newest first while it leaves the summary room, and the rest is
// summarized. It reports whether the turn must be recomposed and why the
// compaction failed. Any other batch runs whole; the run folds what does not
// fit before its first step.
func (s *Service) maybeSyncCompactDiscuss(ctx context.Context, cmd turn.StartTurnCommand, resolved ResolveRunConfigResult, runID string) (bool, error) {
	if !cmd.DiscussContextOverflow || cmd.DiscussRecoveryExhausted || ctx.Err() != nil {
		return false, nil
	}
	budget := resolved.ContextBudgetMaxTokens
	if budget <= 0 {
		budget = s.contextAbsoluteMaxTokens()
	}
	if runtimekind.IsExternal(resolved.RuntimeType) {
		budget -= turn.EstimateTokensFromBytes(len(discussAgentPromptPrefix) + len(discussAgentPromptSuffix) + len("[assistant]\n\n\n") + turn.ContextBytesPerToken - 1)
	}
	// A newest input that does not fit by itself fails the turn: summarizing
	// the rest makes no room for it.
	if budget-cmd.DiscussCurrentTokens <= 0 {
		return false, nil
	}
	req := ChatRequest{BotID: cmd.BotID, ChatID: cmd.BotID, ThreadID: cmd.ThreadID, RunID: runID, discussCurrentSources: cmd.DiscussCurrentSources}
	if err := s.compactForBudget(ctx, req, discussContextPressure(cmd), budget, resolved.ModelID); err != nil {
		return false, err
	}
	return true, nil
}
