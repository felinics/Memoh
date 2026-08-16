package native

import (
	"context"
	"errors"
	"net"
	"testing"
)

// fakeNetError implements net.Error so tests can drive the network branch of
// isRetryableStreamError with a controllable Timeout() flag.
type fakeNetError struct {
	timeout bool
}

func (e fakeNetError) Error() string   { return "fake network error" }
func (e fakeNetError) Timeout() bool   { return e.timeout }
func (e fakeNetError) Temporary() bool { return !e.timeout }

// TestIsRetryableStreamError verifies the retryability decision for each error
// class, in particular that pure timeouts are NOT retryable (issue #1010
// family: retrying a timeout multiplies the already-long silent wait) while
// connection-level and HTTP status failures remain retryable.
func TestIsRetryableStreamError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context deadline exceeded", context.DeadlineExceeded, false},
		{"context canceled", context.Canceled, false},
		// Timeout-class net.Error must NOT retry.
		{"timeout net.Error", fakeNetError{timeout: true}, false},
		{"http client timeout exceeded text", errors.New(`Get "https://x": net/http: request canceled (Client.Timeout exceeded while awaiting headers)`), false},
		// Non-timeout network errors stay retryable.
		{"connection net.Error", fakeNetError{timeout: false}, true},
		{"connection refused", errors.New("dial tcp 1.2.3.4:443: connect: connection refused"), true},
		{"connection reset", errors.New("read tcp 1.2.3.4:443: connection reset by peer"), true},
		{"EOF", errors.New("unexpected EOF"), true},
		// HTTP status errors remain retryable (unchanged behavior).
		{"429", errors.New("api error 429: rate limited"), true},
		{"500", errors.New("api error 500: internal"), true},
		{"rate limit", errors.New("rate limit exceeded"), true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isRetryableStreamError(tc.err); got != tc.want {
				t.Fatalf("isRetryableStreamError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

var _ net.Error = fakeNetError{}