package modelretry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/errs"
)

// TestRetryable pins which failed model calls are made again and the class
// each one is reported as. The provider's Kind decides when it answered, the
// transport decides when it did not, and a cancellation or deadline is never
// retried even where it also reads as a network error. Error text never
// decides.
func TestRetryable(t *testing.T) {
	t.Parallel()

	apiErr := func(status int, kind sdk.ErrorKind) error {
		return &sdk.APIError{Provider: "openai-completions", StatusCode: status, Kind: kind, Message: "upstream said no"}
	}
	reset := &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}
	refused := &url.Error{Op: "Post", URL: "http://127.0.0.1:1/v1/chat/completions", Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}}

	tests := []struct {
		name string
		err  error
		want Reason
	}{
		{name: "nil", err: nil},
		{name: "cancellation", err: context.Canceled},
		{name: "deadline, which is also a net.Error", err: context.DeadlineExceeded},
		{name: "transport error wrapping a deadline", err: &url.Error{Op: "Post", URL: "http://provider", Err: context.DeadlineExceeded}},
		{name: "cancellation under a dependency wrap", err: errs.WrapDependency(context.Canceled, "model stream")},
		{name: "rate limited", err: apiErr(429, sdk.KindRateLimited), want: ReasonRateLimited},
		{name: "server error", err: apiErr(503, sdk.KindServerError), want: ReasonServerError},
		{name: "server error in a stream event", err: apiErr(0, sdk.KindServerError), want: ReasonServerError},
		{name: "server error under a dependency wrap", err: errs.WrapDependency(apiErr(529, sdk.KindServerError), "model stream"), want: ReasonServerError},
		{name: "quota exhausted answered with 429", err: apiErr(429, sdk.KindQuotaExhausted)},
		{name: "authentication", err: apiErr(401, sdk.KindAuthentication)},
		{name: "permission denied", err: apiErr(403, sdk.KindPermissionDenied)},
		{name: "unknown 501", err: apiErr(501, sdk.KindUnknown)},
		{name: "unknown 400", err: apiErr(400, sdk.KindUnknown)},
		{name: "a kind a later SDK adds", err: apiErr(400, sdk.ErrorKind("content_filtered"))},
		{name: "no kind", err: apiErr(503, "")},
		{name: "provider answer decides over a transport error", err: errors.Join(apiErr(400, sdk.KindUnknown), reset)},
		{name: "stream ended before its terminal event", err: fmt.Errorf("anthropic-messages: %w", sdk.ErrStreamIncomplete), want: ReasonStreamIncomplete},
		{name: "stream ended before finish-step", err: errs.WrapDependency(sdk.ErrStreamIncomplete, "model stream ended before finish-step"), want: ReasonStreamIncomplete},
		{name: "unexpected EOF", err: fmt.Errorf("read body: %w", io.ErrUnexpectedEOF), want: ReasonStreamIncomplete},
		{name: "connection reset", err: reset, want: ReasonNetwork},
		{name: "connection refused", err: refused, want: ReasonNetwork},
		{name: "plain EOF", err: io.EOF},
		{name: "status text without an APIError", err: errors.New("api error 503: service unavailable")},
		{name: "finish-step text without the sentinel", err: errs.NewDependency("model stream ended before finish-step")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := Retryable(tt.err)
			if ok != (tt.want != "") || got != tt.want {
				t.Fatalf("Retryable(%v) = %q, %v, want %q", tt.err, got, ok, tt.want)
			}
		})
	}
}

// TestRateLimited pins the classifier of a call with side effects: only a
// rate-limit answer is retried.
func TestRateLimited(t *testing.T) {
	t.Parallel()

	if reason, ok := RateLimited(&sdk.APIError{StatusCode: 429, Kind: sdk.KindRateLimited}); !ok || reason != ReasonRateLimited {
		t.Fatalf("RateLimited(rate_limited) = %q, %v, want rate_limited", reason, ok)
	}
	for _, err := range []error{
		&sdk.APIError{StatusCode: 500, Kind: sdk.KindServerError},
		sdk.ErrStreamIncomplete,
		&net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET},
	} {
		if _, ok := RateLimited(err); ok {
			t.Fatalf("RateLimited(%v) = true, want false", err)
		}
	}
}

// TestDelayToleratesUnsetDelayFields pins the misconfiguration guard: callers
// may set only MaxAttempts (leaving the delay fields at their zero values),
// and the delay must fire immediately instead of panicking inside
// rand.Int64N on a non-positive argument.
func TestDelayToleratesUnsetDelayFields(t *testing.T) {
	t.Parallel()

	if got := (Config{MaxAttempts: 3}).delay(2, nil); got != 0 {
		t.Fatalf("delay(2, delays unset) = %v, want 0", got)
	}
	nano := Config{MaxAttempts: 3, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond}
	if got := nano.delay(2, nil); got != 0 {
		t.Fatalf("delay(2, 1ns delays) = %v, want 0 (delay/2 == 0 must not reach Int64N)", got)
	}
}

func retryAfterErr(header, value string) error {
	h := http.Header{}
	h.Set(header, value)
	return &sdk.APIError{StatusCode: 429, Kind: sdk.KindRateLimited, Header: h}
}

// TestNextTakesRetryAfter pins that the provider's requested wait replaces
// the backoff, even for a fast attempt, while one above MaxRetryAfter falls
// back to it.
func TestNextTakesRetryAfter(t *testing.T) {
	t.Parallel()

	cfg := Config{MaxAttempts: 3, FastAttempts: 1, BaseDelay: time.Second, MaxDelay: 2 * time.Second}

	retry, ok := cfg.Next(0, retryAfterErr("Retry-After", "7"), nil)
	if !ok || retry.Delay != 7*time.Second || retry.Reason != ReasonRateLimited || retry.Attempt != 1 || retry.MaxAttempts != 3 {
		t.Fatalf("Next(Retry-After: 7) = %+v, %v, want attempt 1/3 after 7s for rate_limited", retry, ok)
	}
	retry, _ = cfg.Next(1, retryAfterErr("retry-after-ms", "1500"), nil)
	if retry.Delay != 1500*time.Millisecond {
		t.Fatalf("Next(retry-after-ms: 1500).Delay = %v, want 1.5s", retry.Delay)
	}
	retry, _ = cfg.Next(1, retryAfterErr("Retry-After", "3600"), nil)
	if retry.Delay < 500*time.Millisecond || retry.Delay >= time.Second {
		t.Fatalf("Next(Retry-After: 3600).Delay = %v, want the backoff in [0.5s, 1s)", retry.Delay)
	}
	retry, _ = cfg.Next(0, &sdk.APIError{StatusCode: 503, Kind: sdk.KindServerError}, nil)
	if retry.Delay != 0 {
		t.Fatalf("Next(first retry, no Retry-After).Delay = %v, want 0", retry.Delay)
	}
}

// TestNextStopsAtBudgetAndOnFinalFailure pins the two refusals: the budget is
// spent, or the failure is not one the classifier accepts. A zero Config is
// DefaultConfig.
func TestNextStopsAtBudgetAndOnFinalFailure(t *testing.T) {
	t.Parallel()

	overloaded := &sdk.APIError{StatusCode: 503, Kind: sdk.KindServerError}
	if _, ok := (Config{MaxAttempts: 2}).Next(2, overloaded, nil); ok {
		t.Fatal("Next after the last retry = true, want false")
	}
	if _, ok := (Config{MaxAttempts: 2}).Next(0, &sdk.APIError{StatusCode: 401, Kind: sdk.KindAuthentication}, nil); ok {
		t.Fatal("Next(authentication) = true, want false")
	}
	if _, ok := (Config{MaxAttempts: 2}).Next(0, overloaded, RateLimited); ok {
		t.Fatal("Next(server_error, RateLimited) = true, want false")
	}
	retry, ok := (Config{}).Next(4, overloaded, nil)
	if !ok || retry.MaxAttempts != DefaultConfig().MaxAttempts {
		t.Fatalf("zero Config Next(4) = %+v, %v, want the default budget", retry, ok)
	}
}

var fastConfig = Config{MaxAttempts: 2, FastAttempts: 2}

// TestDoRetriesUntilSuccess pins that a retryable failure is made again and
// the later result returned.
func TestDoRetriesUntilSuccess(t *testing.T) {
	t.Parallel()

	calls := 0
	got, err := Do(context.Background(), nil, "test", fastConfig, nil, func(context.Context) (string, error) {
		calls++
		if calls == 1 {
			return "", &sdk.APIError{StatusCode: 503, Kind: sdk.KindServerError}
		}
		return "ok", nil
	})
	if err != nil || got != "ok" || calls != 2 {
		t.Fatalf("Do = %q, %v after %d calls, want ok after 2", got, err, calls)
	}
}

// TestDoStopsOnFinalFailureAndBudget pins that a final failure is returned at
// once and a retryable one after the budget is spent.
func TestDoStopsOnFinalFailureAndBudget(t *testing.T) {
	t.Parallel()

	auth := &sdk.APIError{StatusCode: 401, Kind: sdk.KindAuthentication}
	calls := 0
	_, err := Do(context.Background(), nil, "test", fastConfig, nil, func(context.Context) (int, error) {
		calls++
		return 0, auth
	})
	if !errors.Is(err, auth) || calls != 1 {
		t.Fatalf("Do(authentication) = %v after %d calls, want it after 1", err, calls)
	}

	overloaded := &sdk.APIError{StatusCode: 503, Kind: sdk.KindServerError}
	calls = 0
	_, err = Do(context.Background(), nil, "test", fastConfig, nil, func(context.Context) (int, error) {
		calls++
		return 0, overloaded
	})
	if !errors.Is(err, overloaded) || calls != fastConfig.MaxAttempts+1 {
		t.Fatalf("Do(server_error) = %v after %d calls, want it after %d", err, calls, fastConfig.MaxAttempts+1)
	}
}

// TestDoWaitEndsWithContext pins that a cancelled context ends the wait for
// the next call.
func TestDoWaitEndsWithContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err := Do(ctx, nil, "test", Config{MaxAttempts: 3}, nil, func(context.Context) (int, error) {
		calls++
		time.AfterFunc(10*time.Millisecond, cancel)
		return 0, retryAfterErr("Retry-After", "30")
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("Do = %v after %d calls, want the cancellation after 1", err, calls)
	}
}
