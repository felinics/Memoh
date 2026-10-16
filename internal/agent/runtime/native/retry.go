package native

import (
	"context"
	"log/slog"

	"github.com/felinics/memoh/internal/errlog"
	"github.com/felinics/memoh/internal/models/modelretry"
)

// retryEvent is the stream event announcing retry: its counters, the wait
// before the next call and the class of the failure.
func retryEvent(retry modelretry.Retry) StreamEvent {
	return StreamEvent{
		Type:         EventRetry,
		Attempt:      retry.Attempt,
		MaxAttempt:   retry.MaxAttempts,
		RetryDelayMs: retry.Delay.Milliseconds(),
		RetryReason:  string(retry.Reason),
	}
}

// logModelRetry records the failed model call that retry makes again, once,
// as a WARN event for agent.model_call. Both step loops record it here.
func (a *Agent) logModelRetry(ctx context.Context, runID string, step int, retry modelretry.Retry, failure error) {
	result := errlog.Event(ctx, "agent.model_call", failure, errlog.Options{})
	a.logger.LogAttrs(ctx, result.Level, "model call failed, retrying", append([]slog.Attr{
		slog.String("run_id", runID),
		slog.Int("attempt", retry.Attempt),
		slog.Int("max_attempts", retry.MaxAttempts),
		slog.Duration("delay", retry.Delay),
		slog.String("retry_reason", string(retry.Reason)),
		slog.Int("step", step),
	}, result.Attrs()...)...)
}
