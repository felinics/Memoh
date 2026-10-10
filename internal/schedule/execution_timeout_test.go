package schedule

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
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

func TestFailureCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want apperror.Code
	}{
		{"budget expired", fmt.Errorf("%w: trigger failed", ErrExecutionTimeout), apperror.CodeScheduleExecutionTimeout},
		{"target session gone", ErrTargetSessionGone, apperror.CodeSessionNotFound},
		{"canceled", fmt.Errorf("trigger: %w", context.Canceled), apperror.CodeCanceled},
		{"public error", apperror.New(apperror.CodeScheduleModelRequired, nil), apperror.CodeScheduleModelRequired},
		{"anything else", errors.New("upstream said: secret detail"), apperror.CodeInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := failureCode(tt.err); got != tt.want {
				t.Fatalf("failureCode = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToScheduleLogKeepsEarlierMessage(t *testing.T) {
	row := sqlc.ListScheduleLogsByBotRow{Status: "error", ErrorMessage: "old sentence"}
	if got := toScheduleLog(row); got.ErrorMessage != "old sentence" || got.ErrorCode != "" {
		t.Fatalf("log = %+v, want the message and no code", got)
	}
	row = sqlc.ListScheduleLogsByBotRow{Status: "error", ErrorCode: "schedule.execution_timeout"}
	if got := toScheduleLog(row); got.ErrorCode != "schedule.execution_timeout" || got.ErrorMessage != "" {
		t.Fatalf("log = %+v, want the code and no message", got)
	}
}
