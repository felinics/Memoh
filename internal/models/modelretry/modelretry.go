// Package modelretry decides whether a failed model call is made again, and
// when. The agent loops and the single model calls (context compaction,
// memory, image generation, titles) share it, so a provider failure is retried
// by the same rule wherever the call is made.
package modelretry

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/errlog"
)

// Config controls how often and how soon a failed model call is retried. A
// zero MaxAttempts means DefaultConfig.
type Config struct {
	MaxAttempts  int           // total retry attempts
	FastAttempts int           // first N attempts with no delay
	BaseDelay    time.Duration // backoff base for non-fast attempts
	MaxDelay     time.Duration // backoff cap
}

// DefaultConfig returns the default retry strategy: 5 attempts total.
// Only the first retry fires immediately — it absorbs network blips and
// instant upstream rejections. Later attempts back off 1s→8s with jitter, so
// a sustained overload window (typically tens of seconds) is ridden out
// without hammering the upstream with a burst of immediate retries.
func DefaultConfig() Config {
	return Config{
		MaxAttempts:  5,
		FastAttempts: 1,
		BaseDelay:    1 * time.Second,
		MaxDelay:     8 * time.Second,
	}
}

// MaxRetryAfter is the longest wait a provider's Retry-After is honored for.
// A longer one falls back to the backoff: the run does not stall for minutes
// on one call.
const MaxRetryAfter = time.Minute

// Reason is the class of a failure that is retried. Its values are stable and
// reach clients on the retry event; the provider's own text never does.
type Reason string

const (
	// ReasonRateLimited: the provider answered rate_limited.
	ReasonRateLimited Reason = "rate_limited"
	// ReasonServerError: the provider answered server_error (failed or
	// overloaded).
	ReasonServerError Reason = "server_error"
	// ReasonStreamIncomplete: the response was cut off before it completed.
	ReasonStreamIncomplete Reason = "stream_incomplete"
	// ReasonNetwork: the request or the response failed in transport.
	ReasonNetwork Reason = "network"
)

// Classifier reports whether a failed model call is worth making again, and
// the class of the failure when it is.
type Classifier func(error) (Reason, bool)

// Retryable is the classifier of a model call that has no side effect beyond
// its answer. The order of the checks is part of the answer.
func Retryable(err error) (Reason, bool) {
	if err == nil {
		return "", false
	}
	// A cancellation or an expired deadline is never retried. It is checked
	// first because context.DeadlineExceeded also satisfies net.Error.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "", false
	}
	// The provider answered: its Kind says whether waiting helps. Any other
	// Kind, including one a later SDK adds, is final.
	var apiErr *sdk.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Kind {
		case sdk.KindRateLimited:
			return ReasonRateLimited, true
		case sdk.KindServerError:
			return ReasonServerError, true
		default:
			return "", false
		}
	}
	// The response was cut off, cleanly or not. The failed call produced no
	// complete answer, so calling again replays nothing.
	if errors.Is(err, sdk.ErrStreamIncomplete) || errors.Is(err, io.ErrUnexpectedEOF) {
		return ReasonStreamIncomplete, true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return ReasonNetwork, true
	}
	return "", false
}

// RateLimited is the classifier of a model call whose result may cost or
// persist something even when the call fails. Only a rate-limit answer proves
// the provider did no work; a server error, a cut-off response or a transport
// failure may follow a result the provider already produced.
func RateLimited(err error) (Reason, bool) {
	reason, ok := Retryable(err)
	if !ok || reason != ReasonRateLimited {
		return "", false
	}
	return reason, true
}

// Retry is the decision to make a failed call again.
type Retry struct {
	// Attempt is the one-based number of this retry, out of MaxAttempts.
	Attempt     int
	MaxAttempts int
	// Delay is the wait before the call is made again.
	Delay  time.Duration
	Reason Reason
}

// Next decides whether a call that failed with err, after retries earlier
// retries, is made again. err is classified by retryable; a nil retryable
// means Retryable.
func (c Config) Next(retries int, err error, retryable Classifier) (Retry, bool) {
	c = c.orDefault()
	if retryable == nil {
		retryable = Retryable
	}
	reason, ok := retryable(err)
	if !ok || retries >= c.MaxAttempts {
		return Retry{}, false
	}
	return Retry{
		Attempt:     retries + 1,
		MaxAttempts: c.MaxAttempts,
		Delay:       c.delay(retries, err),
		Reason:      reason,
	}, true
}

func (c Config) orDefault() Config {
	if c.MaxAttempts <= 0 {
		return DefaultConfig()
	}
	return c
}

// delay returns the wait before retry attempt (zero-based) after err. A
// provider's Retry-After up to MaxRetryAfter is taken as is, even for a fast
// attempt; otherwise fast attempts fire immediately and later ones back off
// exponentially with jitter, capped at MaxDelay.
func (c Config) delay(attempt int, err error) time.Duration {
	var apiErr *sdk.APIError
	if errors.As(err, &apiErr) {
		if after, ok := apiErr.RetryAfter(); ok && after <= MaxRetryAfter {
			return after
		}
	}
	if attempt < c.FastAttempts {
		return 0
	}
	// Exponential backoff: base * 2^(attempt - fastAttempts), capped to prevent overflow
	backoffIdx := attempt - c.FastAttempts
	if backoffIdx > 20 {
		backoffIdx = 20
	}
	delay := c.BaseDelay * time.Duration(1<<backoffIdx)
	delay = min(delay, c.MaxDelay)
	// Int64N panics on a non-positive argument, so a config that leaves the
	// delay fields at (near-)zero values must never reach it. "No delay
	// configured" means the same as a fast attempt: fire immediately.
	half := delay / 2
	if half <= 0 {
		return 0
	}
	// Add jitter: random value in [0, half), so final delay is in [half, delay).
	// math/rand is intentional here — cryptographic randomness is not needed for backoff jitter.
	jitter := time.Duration(rand.Int64N(int64(half))) //nolint:gosec // G404: jitter does not need crypto/rand
	return half + jitter
}

// Do makes call, and makes it again after each failure that cfg.Next accepts.
// Every retried failure is recorded on logger as a WARN event for operation;
// the failure Do returns is left to the caller to record. A cancelled ctx
// ends the wait and returns its error.
func Do[T any](
	ctx context.Context,
	logger *slog.Logger,
	operation string,
	cfg Config,
	retryable Classifier,
	call func(context.Context) (T, error),
) (T, error) {
	for retries := 0; ; retries++ {
		result, err := call(ctx)
		if err == nil {
			return result, nil
		}
		retry, ok := cfg.Next(retries, err, retryable)
		if !ok || ctx.Err() != nil {
			return result, err
		}
		if logger != nil {
			event := errlog.Event(ctx, operation, err, errlog.Options{})
			logger.LogAttrs(ctx, event.Level, "model call failed, retrying", append([]slog.Attr{
				slog.Int("attempt", retry.Attempt),
				slog.Int("max_attempts", retry.MaxAttempts),
				slog.Duration("delay", retry.Delay),
				slog.String("retry_reason", string(retry.Reason)),
			}, event.Attrs()...)...)
		}
		if err := Sleep(ctx, retry.Delay); err != nil {
			var zero T
			return zero, err
		}
	}
}

// Sleep waits for d, or returns ctx's error when it ends first.
func Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
