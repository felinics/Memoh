package native

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/errlog"
	"github.com/felinics/memoh/internal/models/modelretry"
)

const maxContextRecoveries = 2

// Some providers finish with length and zero output when input consumes the
// window instead of returning an API error. Measured input above our allowance
// makes that an unfinished step. Ordinary output truncation and any generated
// content remain completed work and must not be replayed.
func exhaustedContextResult(cfg RunConfig, result sdk.ModelResult) error {
	if result.FinishReason != sdk.FinishReasonLength || result.Usage.OutputTokens != 0 || result.Usage.ReasoningTokens != 0 ||
		result.Text != "" || result.Reasoning != "" || len(result.ReasoningParts) != 0 ||
		len(result.ToolCalls) != 0 || len(result.Files) != 0 || len(result.Sources) != 0 {
		return nil
	}
	allowance := providerInputAllowance(cfg)
	if allowance <= 0 || result.Usage.InputTokens <= allowance {
		return nil
	}
	return &emptyContextExhaustion{inputTokens: result.Usage.InputTokens}
}

type emptyContextExhaustion struct{ inputTokens int }

func (*emptyContextExhaustion) Error() string {
	return "model exhausted context without producing output"
}

func (*emptyContextExhaustion) Unwrap() error { return contextfrag.ErrBudgetUnsatisfied }

// contextRecoveryFeedback reports whether a failed attempt was rejected for
// its input and what the provider said about it. An exhausted attempt
// counted its input but names no window, so the run's own allowance stands
// for the limit it went over.
func contextRecoveryFeedback(cfg RunConfig, err error) (modelretry.Overflow, bool) {
	var exhausted *emptyContextExhaustion
	if errors.As(err, &exhausted) {
		return modelretry.Overflow{InputTokens: exhausted.inputTokens, LimitTokens: providerInputAllowance(cfg)}, true
	}
	return modelretry.ContextOverflow(err)
}

// recoverContextOverflow runs at the failed model step, before any output
// from that attempt commits or any of its tools execute. It folds the
// rejected request to an allowance tighter than the one it was admitted at,
// even when the estimate passed admission, and permits a new dispatch only
// when a committed pass brought the request within it.
func (a *Agent) recoverContextOverflow(ctx context.Context, cfg RunConfig, request *sdk.Request, failure error, recoveries *int) (bool, error) {
	feedback, overflow := contextRecoveryFeedback(cfg, failure)
	if !overflow || cfg.contextSwitch == nil {
		return false, failure
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if *recoveries >= maxContextRecoveries {
		return false, fmt.Errorf("%w: provider context recovery exhausted: %w", contextfrag.ErrBudgetUnsatisfied, failure)
	}
	// The provider's count of the rejected request calibrates the run, so the
	// steps after the recovered one are admitted as the provider counts them.
	cfg.contextSwitch.observe(sdk.Usage{InputTokens: feedback.InputTokens})
	before := contextfrag.ProviderEnvelopeTokens(request.System, request.Messages, request.Tools)
	allowance := feedback.InputBudget(before)
	changed, err := cfg.contextSwitch.fold(ctx, cfg, request, allowance)
	if err != nil {
		return false, err
	}
	after := contextfrag.ProviderEnvelopeTokens(request.System, request.Messages, request.Tools)
	if !changed || after > allowance {
		return false, fmt.Errorf("%w: provider context recovery made no progress: %w", contextfrag.ErrBudgetUnsatisfied, failure)
	}
	*recoveries++
	event := errlog.Event(ctx, "agent.context_recovery", failure, errlog.Options{})
	a.logger.LogAttrs(ctx, event.Level, "provider context overflow recovered", append([]slog.Attr{
		slog.String("run_id", cfg.RunID), slog.Int("attempt", *recoveries),
		slog.Int("estimated_input_before", before), slog.Int("estimated_input_after", after),
		slog.Int("input_allowance", allowance), slog.Int("provider_input_tokens", feedback.InputTokens), slog.Int("provider_limit_tokens", feedback.LimitTokens),
	}, event.Attrs()...)...)
	return true, nil
}
