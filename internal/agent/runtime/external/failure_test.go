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
		{"cancellation", Unavailable(context.Canceled), context.Canceled},
		{"sentinel", Fail(FailureAuthRequired, fmt.Errorf("read auth: %w", ErrAuthRequired)), ErrAuthRequired},
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
