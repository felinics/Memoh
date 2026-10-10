package schedule

import (
	"context"
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/errs"
)

func TestWithExecutionTimeoutKeepsRecordedMarker(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(ErrExecutionTimeout)

	got := withExecutionTimeout(ctx, errs.Recorded(errors.New("trigger failed")))
	if !errors.Is(got, ErrExecutionTimeout) {
		t.Fatalf("err = %v, want ErrExecutionTimeout in chain", got)
	}
	if !errs.Analyze(context.Background(), got).Recorded {
		t.Fatalf("err = %v, want Recorded marker preserved", got)
	}
	if err := withExecutionTimeout(ctx, nil); !errors.Is(err, ErrExecutionTimeout) {
		t.Fatalf("nil trigger err = %v, want ErrExecutionTimeout", err)
	}
}

func TestWithExecutionTimeoutLeavesOtherFailures(t *testing.T) {
	cause := errors.New("trigger failed")
	if got := withExecutionTimeout(context.Background(), cause); got != cause { //nolint:errorlint // identity is the point
		t.Fatalf("err = %v, want unchanged", got)
	}
}
