package native

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"time"

	sdk "github.com/felinics/twilight/sdk"
)

// RetryConfig controls retry behavior for stream failures.
type RetryConfig struct {
	MaxAttempts  int           // total retry attempts
	FastAttempts int           // first N attempts with no delay
	BaseDelay    time.Duration // backoff base for non-fast attempts
	MaxDelay     time.Duration // backoff cap
}

// DefaultRetryConfig returns the default retry strategy: 5 attempts total.
// Only the first retry fires immediately — it absorbs network blips and
// instant upstream rejections. Later attempts back off 1s→8s with jitter, so
// a sustained overload window (typically tens of seconds) is ridden out
// without hammering the upstream with a burst of immediate retries.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:  5,
		FastAttempts: 1,
		BaseDelay:    1 * time.Second,
		MaxDelay:     8 * time.Second,
	}
}

// retryableProviderFailure reports whether a failed model call is worth
// making again. Only the failure of the provider call itself is asked about:
// a failure of the loop's own work (the provider-attempt handoff, a commit, a
// capability refresh, a tool batch) ends the run, since a retry would redo it.
// The order of the checks is part of the answer.
func retryableProviderFailure(err error) bool {
	if err == nil {
		return false
	}
	// A cancellation or an expired deadline is never retried. It is checked
	// first because context.DeadlineExceeded also satisfies net.Error.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// The provider answered: its Kind says whether waiting helps. Any other
	// Kind, including one a later SDK adds, is final.
	var apiErr *sdk.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Kind {
		case sdk.KindRateLimited, sdk.KindServerError:
			return true
		default:
			return false
		}
	}
	// The response was cut off, cleanly or not, or never arrived. The failed
	// step never committed, so calling again replays nothing.
	if errors.Is(err, sdk.ErrStreamIncomplete) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// retryDelay returns the delay before the next retry attempt.
// For fast attempts (0-indexed < FastAttempts): no delay.
// For backoff attempts: exponential delay with jitter, capped at MaxDelay.
func retryDelay(attempt int, cfg RetryConfig) time.Duration {
	if attempt < cfg.FastAttempts {
		return 0
	}
	// Exponential backoff: base * 2^(attempt - fastAttempts), capped to prevent overflow
	backoffIdx := attempt - cfg.FastAttempts
	if backoffIdx > 20 {
		backoffIdx = 20
	}
	delay := cfg.BaseDelay * time.Duration(1<<backoffIdx)
	delay = min(delay, cfg.MaxDelay)
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
