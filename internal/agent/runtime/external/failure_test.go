package external

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// A Failure is judged by its Kind. A cancellation or sentinel it carries must
// not make callers treat it as that error; Cause still reaches it.
func TestFailureHidesItsErrorFromIsAndAs(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		err   error
		inner error
	}{
		{"cancellation", Fail(FailureReported, context.Canceled), context.Canceled},
		{"sentinel", Fail(FailureAuthRequired, fmt.Errorf("read auth: %w", ErrThreadUnavailable)), ErrThreadUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if errors.Is(tc.err, tc.inner) {
				t.Fatalf("errors.Is(%v, %v) = true, want the Failure to hide it", tc.err, tc.inner)
			}
			var failure *Failure
			if !errors.As(tc.err, &failure) {
				t.Fatal("errors.As did not find the Failure")
			}
			if !errors.Is(failure.Cause(), tc.inner) {
				t.Fatalf("Cause() = %v, want it to reach %v", failure.Cause(), tc.inner)
			}
		})
	}
}

// What the agent or its process said reaches the user in its words; a
// Failure, a missing dependency and a cancellation keep what they are.
func TestUpstreamShowsWhatTheProcessSaid(t *testing.T) {
	t.Parallel()

	var failure *Failure
	if !errors.As(Upstream(errors.New("agent process exited with code 1: https://u:p@example.test/x?key=1")), &failure) ||
		failure.Kind != FailureReported || failure.Message != "agent process exited with code 1: https://example.test/x" {
		t.Fatalf("Upstream() = %#v, want a reported failure", failure)
	}
	for _, err := range []error{Fail(FailureAuthRequired, nil), &DependencyMissingError{DependencyID: "codex"}, context.Canceled} {
		if got := Upstream(err); !errors.Is(got, err) || (IsFailure(got) && got != err) { //nolint:errorlint // identity: the error must come back as it is.
			t.Fatalf("Upstream(%v) = %v, want it unchanged", err, got)
		}
	}
}
