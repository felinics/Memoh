package application

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/errs"
)

// switchContext is the compaction event an armed run asks for when the
// request before a step does not fit: one compaction.Compact over the
// sources the request renders, whose passes it then splices into the run's
// own request before measuring it once. The run's input and steers stay
// where they are, and the latest step with everything after it stays raw
// outside the history budget.
func (s *Service) switchContext(req ChatRequest, committer *agentStepCommitter, turnModelID string) func(context.Context, native.ContextSwitch) (string, error) {
	return func(ctx context.Context, sw native.ContextSwitch) (string, error) {
		start := time.Now()
		botSettings, err := s.settingsService.GetBot(ctx, req.BotID)
		if err != nil {
			return "", fmt.Errorf("load compaction settings: %w", err)
		}
		cfg, err := s.buildCompactionConfig(ctx, req, botSettings, sw.UsageTokens, turnModelID)
		if err != nil {
			return "", err
		}
		cfg.View = committer.currentSources(sw.View)
		cfg.Anchors = append(committer.currentSources(sw.Anchors), committer.inputSources()...)
		for _, source := range sw.Tail {
			source.Current = true
			cfg.ProtectedSources = append(cfg.ProtectedSources, source)
		}
		cfg.HistoryBudgetTokens = sw.HistoryBudgetTokens

		passes, err := s.compactionService.Compact(ctx, cfg)
		tokens := sw.UsageTokens
		for _, pass := range passes {
			tokens = sw.Fold(native.ContextFold{
				Artifact: pass.ArtifactID,
				Summary:  compaction.SummaryMessage(pass.Summary),
				Covered:  pass.Covered,
				Absorbed: pass.Absorbed,
			})
		}
		reason := compaction.ReasonOf(err)
		if err == nil && tokens > sw.AllowanceTokens {
			reason = compaction.ReasonStillOver
			err = fmt.Errorf("%s: request_tokens=%d allowance=%d", reason, tokens, sw.AllowanceTokens)
		}
		detail := fmt.Sprintf("trigger=in_run passes=%d request_tokens=%d->%d allowance=%d", len(passes), sw.UsageTokens, tokens, sw.AllowanceTokens)
		attrs := []slog.Attr{
			slog.String("bot_id", req.BotID), slog.String("session_id", req.ThreadID), slog.String("run_id", req.RunID),
			slog.String("trigger", "in_run"), slog.Int("passes", len(passes)),
			slog.Int("request_tokens_before", sw.UsageTokens), slog.Int("request_tokens_after", tokens),
			slog.Int("allowance_tokens", sw.AllowanceTokens), slog.Duration("duration", time.Since(start)),
		}
		if err == nil {
			s.logger.LogAttrs(ctx, slog.LevelInfo, "context compacted", attrs...)
			return detail, nil
		}
		detail += " reason=" + cmp.Or(string(reason), "error")
		if ctx.Err() != nil {
			return detail, ctx.Err()
		}
		err = errs.Wrap(err, "context compaction failed", append(attrs, slog.String("reason", string(reason)))...)
		if reason == compaction.ReasonProtectedOverflow {
			return detail, fmt.Errorf("%w: %w", contextfrag.ErrProtectedContextOverflow, err)
		}
		return detail, fmt.Errorf("%w: %w", contextfrag.ErrBudgetUnsatisfied, err)
	}
}
