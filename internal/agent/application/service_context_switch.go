package application

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/turn"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/errs"
)

// switchContext is the compaction event an armed run asks for when the
// request before a step, the first one included, does not fit: one
// compaction.Fold over that request, whose passes it splices into the run's
// own request as they commit, before the run measures it once. The run's
// input and steers are anchors; the event keeps them and the newest call
// groups raw as compaction.Fold decides. The summary comes from the run's own
// model, forked from its request. An edit or retry that folds before its
// replacement has replaced the turn first moves the session to the checkpoint
// the replacement keeps.
func (s *Service) switchContext(req ChatRequest, committer *agentStepCommitter, turnModelID string) func(context.Context, native.ContextSwitch) (string, error) {
	return func(ctx context.Context, sw native.ContextSwitch) (string, error) {
		start := time.Now()
		inputs := committer.inputSources()
		tokens := sw.UsageTokens
		passes, err := s.compactionService.Fold(ctx, compaction.Event{
			BotID:           req.BotID,
			SessionID:       req.ThreadID,
			ModelRecordID:   turnModelID,
			Request:         sw.Request,
			Sources:         foldSources(committer, sw.Provenance, inputs),
			AnchorPolicy:    anchorPolicy(req),
			Window:          sw.WindowTokens,
			MaxOutputTokens: sw.OutputTokens,
			FixedOutput:     true,
			Allowance:       sw.AllowanceTokens,
			Pressure:        sw.UsageTokens,
			ReplacedTurnID:  committer.replacedTurn(),
			Summarizer:      sw.Summarize,
			Apply: func(pass compaction.Pass) (sdk.Request, []compaction.Source) {
				splice := sw.Fold(native.ContextFold{
					SummaryID: pass.SummaryID,
					Summary:   compaction.SummaryMessage(pass.Summary),
					Order:     pass.Order,
				})
				tokens = splice.Tokens
				return splice.Request, foldSources(committer, splice.Provenance, inputs)
			},
		})
		reason := compaction.ReasonOf(err)
		if err == nil && tokens > sw.AllowanceTokens {
			// An event that folds nothing found only what the run keeps.
			reason = compaction.ReasonStillOver
			if len(passes) == 0 {
				reason = compaction.ReasonProtectedOverflow
			}
			err = fmt.Errorf("%s: request_tokens=%d allowance=%d", reason, tokens, sw.AllowanceTokens)
		}
		detail := fmt.Sprintf("trigger=in_run passes=%d request_tokens=%d->%d allowance=%d", len(passes), sw.UsageTokens, tokens, sw.AllowanceTokens)
		attrs := []slog.Attr{
			slog.String("bot_id", req.BotID), slog.String("session_id", req.ThreadID), slog.String("run_id", req.RunID),
			slog.String("trigger", "in_run"), slog.Int("passes", len(passes)),
			slog.Int("request_tokens_before", sw.UsageTokens), slog.Int("request_tokens_after", tokens),
			slog.Int("allowance_tokens", sw.AllowanceTokens), slog.Duration("duration", time.Since(start)),
		}
		// The token counts are the estimate's; the provider counts density
		// tokens for each.
		if sw.Density > 1 {
			detail += fmt.Sprintf(" density=%.2f", sw.Density)
			attrs = append(attrs, slog.Float64("density", sw.Density))
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

// anchorPolicy is how much of its anchors a run's fold keeps: a Discuss run
// keeps the batch it answers while it leaves the summary room, any other run
// keeps its input and steers within a cap.
func anchorPolicy(req ChatRequest) compaction.AnchorPolicy {
	if req.SessionType == sessionpkg.TypeDiscuss {
		return compaction.AnchorsFirst
	}
	return compaction.AnchorsCapped
}

// foldSources is what each message of the run's request stands for in a
// fold: nothing for runtime context, otherwise its sources named by the rows
// that hold them now, an anchor when it renders the run's input or a steer.
func foldSources(committer *agentStepCommitter, provenance []*native.MessageProvenance, inputs []turn.ContextMessageSource) []compaction.Source {
	sources := make([]compaction.Source, len(provenance))
	for i, p := range provenance {
		if p == nil || len(p.Sources) == 0 {
			continue
		}
		current := committer.currentSources(p.Sources)
		sources[i] = compaction.Source{
			Sources: current,
			Anchor: p.Anchor || slices.ContainsFunc(current, func(source turn.ContextMessageSource) bool {
				return slices.ContainsFunc(inputs, func(input turn.ContextMessageSource) bool {
					return input.Kind == source.Kind && input.ID == source.ID
				})
			}),
		}
	}
	return sources
}
