package application

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/apperror"
)

func TestClassifyRunFailure(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		cause error
		want  apperror.Code
	}{
		{name: "catalogued code", cause: apperror.New(apperror.CodeAgentProviderOverloaded, nil), want: apperror.CodeAgentProviderOverloaded},
		{name: "catalogued code under a wrap", cause: fmt.Errorf("stream: %w", apperror.New(apperror.CodeAgentResponseTimeout, nil)), want: apperror.CodeAgentResponseTimeout},
		{name: "code outside the catalog", cause: apperror.New("not.catalogued", nil), want: apperror.CodeAgentResponseInterrupted},
		{name: "plain error", cause: errors.New("connection reset"), want: apperror.CodeAgentResponseInterrupted},
		{name: "cancellation", cause: context.Canceled, want: apperror.CodeAgentResponseInterrupted},
		{name: "nil", cause: nil, want: apperror.CodeAgentResponseInterrupted},
		{name: "external agent code", cause: fmt.Errorf("prompt: %w", apperror.New(apperror.CodeACPRuntimeBusy, nil)), want: apperror.CodeACPRuntimeBusy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyRunFailure(tc.cause); got != tc.want {
				t.Fatalf("classifyRunFailure() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClassifyRuntimeFailure(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		cause error
		want  apperror.Code
	}{
		{name: "catalogued code", cause: apperror.New(apperror.CodeAgentResponseTimeout, nil), want: apperror.CodeAgentResponseTimeout},
		{name: "external agent code", cause: apperror.New(apperror.CodeACPAgentAuthInvalid, nil), want: apperror.CodeACPAgentAuthInvalid},
		{name: "plain error", cause: errors.New("driver exited"), want: apperror.CodeRuntimePromptFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyRuntimeFailure(tc.cause); got != tc.want {
				t.Fatalf("classifyRuntimeFailure() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The history marker reads its code from the same classification, but a cause
// that names no code leaves no marker unless the idle watchdog fired.
func TestSnapshotFailureCodeMarksOnlyNamedFailures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		idleFired bool
		cause     error
		want      apperror.Code
	}{
		{name: "listed code", cause: apperror.New(apperror.CodeAgentProviderOverloaded, nil), want: apperror.CodeAgentProviderOverloaded},
		{name: "unlisted code", cause: apperror.New(apperror.CodeContextProtectedOverflow, nil), want: ""},
		{name: "user stop", cause: context.Canceled, want: ""},
		{name: "unnamed failure", cause: errors.New("agent stream ended without a terminal event"), want: ""},
		{name: "idle without a code", idleFired: true, cause: context.Canceled, want: apperror.CodeAgentResponseTimeout},
		{name: "idle with a code", idleFired: true, cause: apperror.New(apperror.CodeAgentToolTimeout, nil), want: apperror.CodeAgentToolTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := snapshotFailureCode(tc.idleFired, tc.cause); got != tc.want {
				t.Fatalf("snapshotFailureCode() = %q, want %q", got, tc.want)
			}
		})
	}
}

func errorEvent(code apperror.Code) native.StreamEvent {
	return native.StreamEvent{Type: native.EventError, Code: string(code), Error: string(code)}
}

func firedIdle(t *testing.T) (context.Context, *idleCancel) {
	t.Helper()
	idleCtx, cancel := context.WithCancelCause(context.Background())
	cancel(apperror.New(apperror.CodeAgentResponseTimeout, nil))
	return idleCtx, &idleCancel{fired: true}
}

func quietIdle() (context.Context, *idleCancel) {
	return context.Background(), &idleCancel{}
}

func newTestRecorder(runCtx context.Context, idleCtx context.Context, idle *idleCancel) *outcomeRecorder {
	r := newOutcomeRecorder(runCtx)
	r.watchIdle(idleCtx, idle)
	return r
}

func TestOutcomeRecorderFirstStreamErrorWins(t *testing.T) {
	t.Parallel()
	idleCtx, idle := quietIdle()
	r := newTestRecorder(context.Background(), idleCtx, idle)

	if err := r.observe(errorEvent(apperror.CodeAgentProviderRateLimited)); apperror.CodeOf(err) != apperror.CodeAgentProviderRateLimited {
		t.Fatalf("observe() = %v, want the event's failure", err)
	}
	_ = r.observe(native.StreamEvent{Type: native.EventRetry})
	_ = r.observe(errorEvent(apperror.CodeAgentProviderOverloaded))

	if got := apperror.CodeOf(r.cause); got != apperror.CodeAgentProviderRateLimited {
		t.Fatalf("cause code = %q, want the first error's", got)
	}
	if got := apperror.CodeOf(r.reported); got != apperror.CodeAgentProviderRateLimited {
		t.Fatalf("reported code = %q, want the first error's", got)
	}
}

func TestOutcomeRecorderAgentEndClearsRecoveredErrors(t *testing.T) {
	t.Parallel()
	idleCtx, idle := quietIdle()
	r := newTestRecorder(context.Background(), idleCtx, idle)

	_ = r.observe(errorEvent(apperror.CodeAgentProviderOverloaded))
	_ = r.observe(native.StreamEvent{Type: native.EventAgentEnd})

	if r.cause != nil || r.reported != nil {
		t.Fatalf("cause = %v, reported = %v; a clean end must clear both", r.cause, r.reported)
	}
	if !r.terminalSeen || r.deferred {
		t.Fatalf("terminalSeen = %v, deferred = %v", r.terminalSeen, r.deferred)
	}
}

func TestOutcomeRecorderDeferredEndKeepsErrors(t *testing.T) {
	t.Parallel()
	idleCtx, idle := quietIdle()
	r := newTestRecorder(context.Background(), idleCtx, idle)

	_ = r.observe(errorEvent(apperror.CodeAgentProviderOverloaded))
	_ = r.observe(native.StreamEvent{Type: native.EventAgentEnd, ApprovalID: "approval-1"})

	if !r.deferred {
		t.Fatal("a terminal event with an approval must defer the lifecycle")
	}
	if r.cause == nil || r.reported == nil {
		t.Fatal("a deferred end must not clear the recorded errors")
	}
	var calls int
	r.finishLifecycle(func(error) { calls++ })
	if calls != 0 {
		t.Fatal("finishLifecycle wrote a terminal for a parked run")
	}
	r.setCause(errors.New("post-persist failed"))
	r.finishLifecycle(func(error) { calls++ })
	if calls != 1 {
		t.Fatal("setCause must end the deferral")
	}
}

func TestOutcomeRecorderAbortCause(t *testing.T) {
	t.Parallel()

	t.Run("idle timeout", func(t *testing.T) {
		t.Parallel()
		idleCtx, idle := firedIdle(t)
		r := newTestRecorder(context.Background(), idleCtx, idle)
		_ = r.observe(errorEvent(apperror.CodeAgentProviderOverloaded))
		_ = r.observe(native.StreamEvent{Type: native.EventAgentAbort})
		if got := apperror.CodeOf(r.cause); got != apperror.CodeAgentResponseTimeout {
			t.Fatalf("cause code = %q, want the idle timeout", got)
		}
	})
	t.Run("earlier stream error", func(t *testing.T) {
		t.Parallel()
		idleCtx, idle := quietIdle()
		r := newTestRecorder(context.Background(), idleCtx, idle)
		_ = r.observe(errorEvent(apperror.CodeAgentProviderOverloaded))
		_ = r.observe(native.StreamEvent{Type: native.EventAgentAbort})
		if got := apperror.CodeOf(r.cause); got != apperror.CodeAgentProviderOverloaded {
			t.Fatalf("cause code = %q, want the stream error", got)
		}
	})
	t.Run("run stopped", func(t *testing.T) {
		t.Parallel()
		runCtx, stop := context.WithCancelCause(context.Background())
		stopCause := errors.New("stopped by user")
		stop(stopCause)
		idleCtx, idle := quietIdle()
		r := newTestRecorder(runCtx, idleCtx, idle)
		_ = r.observe(errorEvent(apperror.CodeAgentProviderOverloaded))
		_ = r.observe(native.StreamEvent{Type: native.EventAgentAbort})
		if !errors.Is(r.cause, stopCause) {
			t.Fatalf("cause = %v, want the stop cause", r.cause)
		}
	})
	t.Run("no cause", func(t *testing.T) {
		t.Parallel()
		idleCtx, idle := quietIdle()
		r := newTestRecorder(context.Background(), idleCtx, idle)
		_ = r.observe(native.StreamEvent{Type: native.EventAgentAbort})
		if r.cause == nil || r.cause.Error() != "agent run aborted" {
			t.Fatalf("cause = %v, want the generic abort", r.cause)
		}
	})
}

func TestOutcomeRecorderEndStream(t *testing.T) {
	t.Parallel()

	t.Run("no terminal event", func(t *testing.T) {
		t.Parallel()
		idleCtx, idle := quietIdle()
		r := newTestRecorder(context.Background(), idleCtx, idle)
		r.endStream()
		if r.cause == nil || r.cause.Error() != "agent stream ended without a terminal event" {
			t.Fatalf("cause = %v", r.cause)
		}
	})
	t.Run("idle timeout", func(t *testing.T) {
		t.Parallel()
		idleCtx, idle := firedIdle(t)
		r := newTestRecorder(context.Background(), idleCtx, idle)
		r.endStream()
		if got := apperror.CodeOf(r.cause); got != apperror.CodeAgentResponseTimeout {
			t.Fatalf("cause code = %q, want the idle timeout", got)
		}
	})
	t.Run("recorded cause stays", func(t *testing.T) {
		t.Parallel()
		idleCtx, idle := firedIdle(t)
		r := newTestRecorder(context.Background(), idleCtx, idle)
		_ = r.observe(errorEvent(apperror.CodeAgentProviderOverloaded))
		r.endStream()
		if got := apperror.CodeOf(r.cause); got != apperror.CodeAgentProviderOverloaded {
			t.Fatalf("cause code = %q, want the recorded error", got)
		}
	})
	t.Run("clean end", func(t *testing.T) {
		t.Parallel()
		idleCtx, idle := quietIdle()
		r := newTestRecorder(context.Background(), idleCtx, idle)
		_ = r.observe(native.StreamEvent{Type: native.EventAgentEnd})
		r.endStream()
		if r.cause != nil {
			t.Fatalf("cause = %v, want none after a clean end", r.cause)
		}
	})
}

func TestOutcomeRecorderReportedErrors(t *testing.T) {
	t.Parallel()
	idleCtx, idle := firedIdle(t)
	r := newTestRecorder(context.Background(), idleCtx, idle)

	persistErr := errors.New("persist failed")
	r.recordReported(persistErr)
	r.recordReported(errors.New("later"))
	if !errors.Is(r.reported, persistErr) {
		t.Fatalf("reported = %v, want the first", r.reported)
	}
	r.reportIdleTimeout()
	if got := apperror.CodeOf(r.reported); got != apperror.CodeAgentResponseTimeout {
		t.Fatalf("reported code = %q, the idle timeout replaces what was reported", got)
	}
}

// A failed terminal event carries the recorder's code; a clean end, a parked
// decision and a stop do not.
func TestOutcomeRecorderStampsOnlyFailedTerminals(t *testing.T) {
	t.Parallel()
	abort := native.StreamEvent{Type: native.EventAgentAbort}

	idleCtx, idle := quietIdle()
	failed := newTestRecorder(context.Background(), idleCtx, idle)
	_ = failed.observe(errorEvent(apperror.CodeAgentProviderRateLimited))
	_ = failed.observe(errorEvent(apperror.CodeAgentProviderOverloaded))
	_ = failed.observe(abort)
	if got := failed.stampTerminal(abort).Code; got != string(apperror.CodeAgentProviderRateLimited) {
		t.Fatalf("stamped code = %q, want the first error's", got)
	}
	if got := failed.ownerOutcome(); got.Status != "errored" || got.ErrorCode() != string(apperror.CodeAgentProviderRateLimited) {
		t.Fatalf("owner outcome = %q / %q, want errored with the first error's code", got.Status, got.ErrorCode())
	}

	// A coded cancellation of the run context, such as the schedule budget, is a
	// stop the session runtime names from the run, not a stamped failure.
	stopCtx, stop := context.WithCancelCause(context.Background())
	stop(apperror.New(apperror.CodeScheduleExecutionTimeout, nil))
	stopped := newTestRecorder(stopCtx, idleCtx, idle)
	_ = stopped.observe(abort)
	if got := stopped.stampTerminal(abort).Code; got != "" {
		t.Fatalf("stopped run stamped code = %q, want none", got)
	}
	if got := stopped.ownerOutcome(); got != (RunOutcome{}) {
		t.Fatalf("stopped run owner outcome = %+v, want unnamed", got)
	}

	parked := newTestRecorder(context.Background(), idleCtx, idle)
	_ = parked.observe(errorEvent(apperror.CodeAgentProviderOverloaded))
	parkedAbort := native.StreamEvent{Type: native.EventAgentAbort, ApprovalID: "approval-1"}
	_ = parked.observe(parkedAbort)
	if got := parked.stampTerminal(parkedAbort).Code; got != "" {
		t.Fatalf("parked run stamped code = %q, want none", got)
	}

	end := native.StreamEvent{Type: native.EventAgentEnd}
	recovered := newTestRecorder(context.Background(), idleCtx, idle)
	_ = recovered.observe(errorEvent(apperror.CodeAgentProviderOverloaded))
	_ = recovered.observe(end)
	if got := recovered.stampTerminal(end).Code; got != "" {
		t.Fatalf("recovered run stamped code = %q, want none", got)
	}
}

// A loop whose cause comes from a driver's return value stamps only a failure
// it delivered in the stream, with its default code when the cause has none.
func TestOutcomeRecorderDeliveredOnly(t *testing.T) {
	t.Parallel()
	abort := native.StreamEvent{Type: native.EventAgentAbort}
	r := newOutcomeRecorder(context.Background())
	r.defaultCode = apperror.CodeRuntimePromptFailed
	r.deliveredOnly = true
	r.setCause(errors.New("persist round failed"))
	if got := r.stampTerminal(abort).Code; got != "" {
		t.Fatalf("undelivered failure stamped code = %q, want none", got)
	}
	if got := r.ownerOutcome(); got != (RunOutcome{}) {
		t.Fatalf("undelivered failure owner outcome = %+v, want unnamed", got)
	}
	r.markDelivered()
	if got := r.stampTerminal(abort).Code; got != string(apperror.CodeRuntimePromptFailed) {
		t.Fatalf("delivered failure stamped code = %q, want the default", got)
	}
}

// A failure whose cause has no catalogued code is named with the default code
// once, and the stamped terminal and the owner outcome carry the same code. A
// stop is still left unnamed.
func TestOutcomeRecorderDefaultCodeNamesUncodedFailure(t *testing.T) {
	t.Parallel()
	abort := native.StreamEvent{Type: native.EventAgentAbort}
	code := apperror.CodeAgentResponseInterrupted
	idleCtx, idle := quietIdle()

	aborted := newTestRecorder(context.Background(), idleCtx, idle)
	aborted.defaultCode = code
	_ = aborted.observe(abort)
	aborted.observeSnapshot(terminalSnapshot{aborted: true})
	if got := aborted.stampTerminal(abort).Code; got != string(code) {
		t.Fatalf("uncoded abort stamped code = %q, want %q", got, code)
	}
	if got := aborted.ownerOutcome(); got.Status != "errored" || got.ErrorCode() != string(code) {
		t.Fatalf("uncoded abort owner outcome = %q / %q, want errored with %q", got.Status, got.ErrorCode(), code)
	}

	unterminated := newTestRecorder(context.Background(), idleCtx, idle)
	unterminated.defaultCode = code
	unterminated.endStream()
	if got := unterminated.ownerOutcome(); got.Status != "errored" || got.ErrorCode() != string(code) {
		t.Fatalf("stream without a terminal owner outcome = %q / %q, want errored with %q", got.Status, got.ErrorCode(), code)
	}

	stopCtx, stop := context.WithCancel(context.Background())
	stop()
	stopped := newTestRecorder(stopCtx, idleCtx, idle)
	stopped.defaultCode = code
	_ = stopped.observe(abort)
	stopped.observeSnapshot(terminalSnapshot{aborted: true})
	if got := stopped.stampTerminal(abort).Code; got != "" {
		t.Fatalf("stopped run stamped code = %q, want none", got)
	}
	if got := stopped.ownerOutcome(); got != (RunOutcome{}) {
		t.Fatalf("stopped run owner outcome = %+v, want unnamed", got)
	}
}
