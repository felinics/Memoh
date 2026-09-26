package application

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	"github.com/felinics/memoh/internal/agent/turn"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/models"
)

func discussContextPressure(cmd turn.StartTurnCommand) int {
	total := 0
	for _, message := range cmd.DiscussMessages {
		total += discussMessageTokens(message)
	}
	return max(total, cmd.DiscussContextTokens)
}

func (s *Service) scheduleDiscussCompaction(ctx context.Context, cmd turn.StartTurnCommand, modelID string) {
	if pressure := discussContextPressure(cmd); pressure > 0 && s.compactionService != nil && s.settingsService != nil {
		go s.maybeCompactDiscuss(context.WithoutCancel(ctx), cmd.BotID, cmd.ThreadID, modelID, pressure)
	}
}

func (s *Service) maybeSyncCompactDiscuss(ctx context.Context, cmd turn.StartTurnCommand, resolved ResolveRunConfigResult, runID string) bool {
	mode := s.effectiveSyncCompactionMode()
	if ctx.Err() != nil || mode == syncCompactionModeOff || s.compactionService == nil || s.settingsService == nil {
		return false
	}
	pressure := discussContextPressure(cmd)
	budget := resolved.ContextBudgetMaxTokens
	if budget <= 0 {
		budget = s.contextAbsoluteMaxTokens()
	}
	_, admission := admitDiscussMessages(cmd.DiscussMessages, budget)
	if resolved.RuntimeType == sessionpkg.RuntimeACPAgent || sessionpkg.IsDirectRuntimeType(resolved.RuntimeType) {
		_, admission = admitDiscussAgentMessages(cmd.DiscussMessages, budget)
	}
	rejected := cmd.DiscussContextOverflow || admission.ProtectedOverflow
	if !rejected && !syncCompactionShouldRun(pressure, budget) {
		return false
	}
	threshold := hardCompactionThreshold(budget)
	if mode == syncCompactionModeShadow && !rejected {
		s.logger.InfoContext(ctx, "sync_compaction_backstop",
			slog.String("path", "discuss"),
			slog.String("mode", "shadow"),
			slog.Bool("would_fire", true),
			slog.String("bot_id", cmd.BotID),
			slog.String("session_id", cmd.ThreadID),
			slog.Int("pressure_tokens", pressure),
			slog.Int("threshold_tokens", threshold))
		return false
	}
	if rejected {
		if cmd.DiscussContextOverflow {
			budget = max(0, budget-cmd.DiscussCurrentTokens)
			if resolved.RuntimeType == sessionpkg.RuntimeACPAgent || sessionpkg.IsDirectRuntimeType(resolved.RuntimeType) {
				budget -= turn.EstimateTokensFromBytes(len(discussAgentPromptPrefix) + len(discussAgentPromptSuffix) + len("[assistant]\n\n\n") + turn.ContextBytesPerToken - 1)
			}
		} else {
			budget = admission.RecoveryBudgetTokens
		}
		if budget <= 0 {
			return false
		}
	}
	start := time.Now()
	req := ChatRequest{BotID: cmd.BotID, ChatID: cmd.BotID, ThreadID: cmd.ThreadID, RunID: runID, discussCurrentSources: cmd.DiscussCurrentSources, discussMessages: cmd.DiscussMessages}
	var res compaction.Result
	if rejected {
		res = s.runBudgetCompactionSync(ctx, req, pressure, budget, resolved.ModelID)
	} else {
		res = s.runCompactionSync(ctx, req, pressure, budget, resolved.ModelID)
	}

	s.logger.InfoContext(ctx, "sync_compaction_backstop",
		slog.String("path", "discuss"),
		slog.String("mode", "active"),
		slog.String("status", res.Status),
		slog.Int64("duration_ms", time.Since(start).Milliseconds()),
		slog.String("bot_id", cmd.BotID),
		slog.String("session_id", cmd.ThreadID),
		slog.Int("pressure_tokens", pressure),
		slog.Int("threshold_tokens", threshold))
	return res.Status == compaction.StatusOK || (rejected && res.Status == compaction.StatusProgress)
}

// maybeCompactDiscuss re-evaluates compaction pressure after a native discuss
// turn with the same trigger policy as the chat path.
func (s *Service) maybeCompactDiscuss(ctx context.Context, botID, threadID, modelID string, compactable int) {
	// The absolute cap keeps compaction triggers alive even when the model
	// has no configured context window; a zero budget would disable them.
	budget := s.contextAbsoluteMaxTokens()
	var turnModel models.GetResponse
	if s.modelsService != nil && strings.TrimSpace(modelID) != "" {
		if model, err := s.modelsService.GetByID(ctx, modelID); err == nil {
			turnModel = model
			budget = s.effectiveContextTokenBudget(model)
		}
	}
	s.maybeCompact(ctx, ChatRequest{BotID: botID, ThreadID: threadID}, resolvedContext{
		model:                  turnModel,
		compactableTokens:      compactable,
		compactableTokensKnown: true,
		contextTokenBudget:     budget,
	}, compactable)
}

// discussCompactableTokens estimates the raw history share of a discuss
// context, excluding artifact summaries, in the shared estimator's unit.
func discussCompactableTokens(messages []turn.DiscussMessage) int {
	total := 0
	for _, message := range messages {
		if message.CompactionArtifactID != "" {
			continue
		}
		total += discussMessageTokens(message)
	}
	return total
}
