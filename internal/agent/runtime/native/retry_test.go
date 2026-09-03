package native

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetryableStreamErrorSeparatesProviderStatusFromApplicationTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "provider 504", err: errors.New("api error 504: gateway timeout"), want: true},
		{name: "provider 524", err: errors.New("api error 524: origin timeout"), want: true},
		{name: "timeout wording alone", err: errors.New("request timeout label only"), want: false},
		{name: "application deadline", err: context.DeadlineExceeded, want: false},
		{name: "explicit cancellation", err: context.Canceled, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isRetryableStreamError(tt.err); got != tt.want {
				t.Fatalf("isRetryableStreamError(%q) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestRetryDelayToleratesUnsetDelayFields pins the misconfiguration guard:
// callers may set only MaxAttempts (leaving the delay fields at their zero
// values), and retryDelay must fire immediately instead of panicking inside
// rand.Int64N on a non-positive argument.
func TestRetryDelayToleratesUnsetDelayFields(t *testing.T) {
	t.Parallel()

	if got := retryDelay(2, RetryConfig{MaxAttempts: 3}); got != 0 {
		t.Fatalf("retryDelay(2, delays unset) = %v, want 0", got)
	}
	nano := RetryConfig{MaxAttempts: 3, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond}
	if got := retryDelay(2, nano); got != 0 {
		t.Fatalf("retryDelay(2, 1ns delays) = %v, want 0 (delay/2 == 0 must not reach Int64N)", got)
	}
}
