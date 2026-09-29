package native

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"syscall"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/errs"
)

// TestRetryableProviderFailure pins which failed model calls are made again.
// The provider's Kind decides when it answered, the transport decides when it
// did not, and a cancellation or deadline is never retried even where it also
// reads as a network error. Error text never decides.
func TestRetryableProviderFailure(t *testing.T) {
	t.Parallel()

	apiErr := func(status int, kind sdk.ErrorKind) error {
		return &sdk.APIError{Provider: "openai-completions", StatusCode: status, Kind: kind, Message: "upstream said no"}
	}
	reset := &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}
	refused := &url.Error{Op: "Post", URL: "http://127.0.0.1:1/v1/chat/completions", Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "cancellation", err: context.Canceled, want: false},
		{name: "deadline, which is also a net.Error", err: context.DeadlineExceeded, want: false},
		{name: "transport error wrapping a deadline", err: &url.Error{Op: "Post", URL: "http://provider", Err: context.DeadlineExceeded}, want: false},
		{name: "cancellation under a dependency wrap", err: errs.WrapDependency(context.Canceled, "model stream"), want: false},
		{name: "rate limited", err: apiErr(429, sdk.KindRateLimited), want: true},
		{name: "server error", err: apiErr(503, sdk.KindServerError), want: true},
		{name: "server error in a stream event", err: apiErr(0, sdk.KindServerError), want: true},
		{name: "server error under a dependency wrap", err: errs.WrapDependency(apiErr(529, sdk.KindServerError), "model stream"), want: true},
		{name: "quota exhausted answered with 429", err: apiErr(429, sdk.KindQuotaExhausted), want: false},
		{name: "authentication", err: apiErr(401, sdk.KindAuthentication), want: false},
		{name: "permission denied", err: apiErr(403, sdk.KindPermissionDenied), want: false},
		{name: "unknown 501", err: apiErr(501, sdk.KindUnknown), want: false},
		{name: "unknown 400", err: apiErr(400, sdk.KindUnknown), want: false},
		{name: "a kind a later SDK adds", err: apiErr(400, sdk.ErrorKind("content_filtered")), want: false},
		{name: "no kind", err: apiErr(503, ""), want: false},
		{name: "provider answer decides over a transport error", err: errors.Join(apiErr(400, sdk.KindUnknown), reset), want: false},
		{name: "stream ended before its terminal event", err: fmt.Errorf("anthropic-messages: %w", sdk.ErrStreamIncomplete), want: true},
		{name: "unexpected EOF", err: fmt.Errorf("read body: %w", io.ErrUnexpectedEOF), want: true},
		{name: "connection reset", err: reset, want: true},
		{name: "connection refused", err: refused, want: true},
		{name: "plain EOF", err: io.EOF, want: false},
		{name: "status text without an APIError", err: errors.New("api error 503: service unavailable"), want: false},
		{name: "stream ended before finish-step", err: errs.NewDependency("model stream ended before finish-step"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := retryableProviderFailure(tt.err); got != tt.want {
				t.Fatalf("retryableProviderFailure(%v) = %v, want %v", tt.err, got, tt.want)
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
