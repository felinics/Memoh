package native

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"regexp"
	"strings"
	"time"
)

// RetryConfig controls retry behavior for stream failures.
type RetryConfig struct {
	MaxAttempts  int           // total retry attempts
	FastAttempts int           // first N attempts with no delay
	BaseDelay    time.Duration // backoff base for non-fast attempts
	MaxDelay     time.Duration // backoff cap
}

// err429Pattern matches HTTP 429 status codes in error strings.
// Requires a non-digit boundary to avoid matching "429" inside larger numbers.
var err429Pattern = regexp.MustCompile(`(^|[^0-9])429($|[^0-9])`)

// errEOFPattern matches EOF or connection-level resets.
var errEOFPattern = regexp.MustCompile(`(?i)connection (reset|refused)|EOF$`)

// serverErrPattern matches "api error 5XX" where XX is any two digits.
var serverErrPattern = regexp.MustCompile(`api error 5\d{2}`)

// DefaultRetryConfig returns the default retry strategy: 10 attempts total,
// first 5 fast (no delay), last 5 with exponential backoff.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:  10,
		FastAttempts: 5,
		BaseDelay:    1 * time.Second,
		MaxDelay:     30 * time.Second,
	}
}

// isRetryableStreamError returns true for errors worth retrying.
func isRetryableStreamError(err error) bool {
	if err == nil {
		return false
	}
	// Context cancelled/expired — do NOT retry (check first since
	// context.DeadlineExceeded also satisfies net.Error)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// Network-level errors (connection refused, timeout, DNS)
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	// HTTP status errors: retry on 429 and 5xx
	errStr := err.Error()
	if err429Pattern.MatchString(errStr) {
		return true
	}
	if strings.Contains(errStr, "rate limit") || strings.Contains(errStr, "rate_limit") {
		return true
	}
	if serverErrPattern.MatchString(errStr) {
		return true
	}
	// Connection reset / EOF
	if errEOFPattern.MatchString(errStr) {
		return true
	}
	return false
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
	// Add jitter: random value in [0, delay/2), so final delay is in [delay/2, delay).
	// math/rand is intentional here — cryptographic randomness is not needed for backoff jitter.
	jitter := time.Duration(rand.Int64N(int64(delay / 2))) //nolint:gosec // G404: jitter does not need crypto/rand
	return delay/2 + jitter
}
