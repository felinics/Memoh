package application

import (
	"context"
	"errors"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/apperror"
)

// classifyRunFailure names the public code a run failure is reported with: the
// catalogued code the cause carries, or agent.response_interrupted when it
// carries none the catalog knows.
func classifyRunFailure(cause error) apperror.Code {
	if code := publicFailureCode(cause); code != "" {
		return code
	}
	return apperror.CodeAgentResponseInterrupted
}

// classifyRuntimeFailure is classifyRunFailure for a turn an External Agent
// runtime ran: a cause without a catalogued code is reported as
// runtime_prompt_failed.
func classifyRuntimeFailure(cause error) apperror.Code {
	if code := publicFailureCode(cause); code != "" {
		return code
	}
	return apperror.CodeRuntimePromptFailed
}

// hasCatalogCode reports whether err carries an apperror whose code is in the
// catalog.
func hasCatalogCode(err error) bool {
	_, ok := apperror.Lookup(apperror.CodeOf(err))
	return ok
}

// publicFailureCode is the catalogued code a cause carries, or the code
// ExternalAgentError gives it. It is empty when the cause has neither.
func publicFailureCode(cause error) apperror.Code {
	public := ExternalAgentError(cause)
	if !hasCatalogCode(public) {
		return ""
	}
	return apperror.CodeOf(public)
}

// outcomeRecorder holds what a stream loop has learned about how its run ended.
//
// cause is the run's lifecycle cause: the first stream error wins, a clean
// AgentEnd clears it, and an AgentAbort names the idle timeout or the stop that
// ended the run. reported is the error the turn port reports to its caller; it
// follows the same first-error rule and is cleared by the same AgentEnd, but
// persistence failures and the idle timeout set it separately. deferred means
// no lifecycle terminal is written for the run yet: the terminal event parked it
// on a decision, or it handed its context back for recomposition.
type outcomeRecorder struct {
	runCtx  context.Context
	idleCtx context.Context
	idle    *idleCancel

	cause    error
	reported error
	// failure is the first failure the stream reported in an error event. A
	// clean AgentEnd clears it, as it clears cause; a failure found later, such
	// as a persistence error, never replaces it.
	failure      error
	deferred     bool
	terminalSeen bool

	// defaultCode names a failure whose cause carries no catalogued code. It is
	// empty when such a failure is left for the session runtime to name.
	defaultCode apperror.Code
	// deliveredOnly limits the failure code to a failure the stream delivered
	// as an error event, for a loop whose cause comes from a driver's return
	// value rather than from the events it saw. delivered records that it did.
	deliveredOnly bool
	delivered     bool
}

// newOutcomeRecorder starts a recorder for a run whose stop and abort causes
// are read from runCtx.
func newOutcomeRecorder(runCtx context.Context) *outcomeRecorder {
	return &outcomeRecorder{runCtx: runCtx}
}

// watchIdle attaches the idle watchdog whose firing names an abort.
func (r *outcomeRecorder) watchIdle(idleCtx context.Context, idle *idleCancel) {
	r.idleCtx = idleCtx
	r.idle = idle
}

// observe records one agent event and returns the stream failure it reports,
// if any.
func (r *outcomeRecorder) observe(event native.StreamEvent) error {
	eventErr := agentStreamFailure(event)
	if eventErr != nil {
		r.recordCause(eventErr)
		if r.reported == nil {
			r.reported = eventErr
		}
		if r.failure == nil {
			r.failure = eventErr
		}
	}
	if !event.IsTerminal() {
		return eventErr
	}
	r.terminalSeen = true
	r.deferred = strings.TrimSpace(event.ApprovalID) != ""
	if r.deferred {
		return eventErr
	}
	switch event.Type {
	case native.EventAgentEnd:
		// A terminal success means an earlier retryable stream error recovered.
		r.cause = nil
		r.reported = nil
		r.failure = nil
	case native.EventAgentAbort:
		if r.idle.DidFire() {
			r.cause = context.Cause(r.idleCtx)
		} else if context.Cause(r.runCtx) != nil || r.cause == nil {
			r.cause = agentAbortCause(r.runCtx)
		}
	}
	return eventErr
}

// handOff ends a run that returned its context to the driver for
// recomposition before any model call; the resubmitted turn owns the outcome.
func (r *outcomeRecorder) handOff() {
	r.deferred = true
}

// observeSnapshot records the terminal snapshot decoded from a terminal event.
func (r *outcomeRecorder) observeSnapshot(snap terminalSnapshot) {
	r.deferred = r.deferred || snap.deferredToolID != ""
	if snap.aborted && !r.deferred && r.cause == nil {
		r.cause = agentAbortCause(r.runCtx)
	}
}

// endStream names the cause of a stream that closed without one.
func (r *outcomeRecorder) endStream() {
	if r.cause != nil || r.deferred {
		return
	}
	switch {
	case r.idle.DidFire():
		r.cause = context.Cause(r.idleCtx)
	case r.runCtx.Err() != nil:
		r.cause = context.Cause(r.runCtx)
	case !r.terminalSeen:
		r.cause = errors.New("agent stream ended without a terminal event")
	}
}

// recordCause keeps err as the lifecycle cause unless one is already recorded.
func (r *outcomeRecorder) recordCause(err error) {
	if r.cause == nil {
		r.cause = err
	}
}

// setCause replaces the lifecycle cause. A failure found after the terminal
// event ends the run even when that event had parked it on a decision.
func (r *outcomeRecorder) setCause(err error) {
	r.cause = err
	r.deferred = false
}

// recordReported keeps err as the reported error unless one is already
// recorded.
func (r *outcomeRecorder) recordReported(err error) {
	if r.reported == nil {
		r.reported = err
	}
}

// reportIdleTimeout makes the idle timeout the reported error.
func (r *outcomeRecorder) reportIdleTimeout() {
	r.reported = context.Cause(r.idleCtx)
}

// failureCode is the code of the failure that ended the run, or empty when the
// run did not fail with a catalogued code: it completed, parked on a decision,
// or was stopped. A stop cancels the run context, and the session runtime names
// it from the abort intent recorded against the run, not from this recorder.
func (r *outcomeRecorder) failureCode() apperror.Code {
	if r.cause == nil || r.deferred || r.runCtx.Err() != nil || (r.deliveredOnly && !r.delivered) {
		return ""
	}
	if code := publicFailureCode(r.cause); code != "" {
		return code
	}
	return r.defaultCode
}

// stampTerminal writes the run's failure code into a failed terminal event
// before it is published. The session runtime proposes the run's durable
// outcome from the terminal event, so the code the recorder classified is the
// one the run records, not whichever stream error the live view saw last.
func (r *outcomeRecorder) stampTerminal(event native.StreamEvent) native.StreamEvent {
	if event.Type != native.EventAgentAbort {
		return event
	}
	if code := r.failureCode(); code != "" {
		event.Code = string(code)
	}
	return event
}

// ownerOutcome is the outcome a stream that returned without an error reports
// to its terminal write: the failure it already delivered in the stream, or an
// unnamed outcome for the session runtime to resolve.
//
// The outcome's cause carries the failure code the terminal event was stamped
// with, so a cause the recorder named with defaultCode reaches the terminal
// write under that same code.
func (r *outcomeRecorder) ownerOutcome() RunOutcome {
	if r == nil {
		return RunOutcome{}
	}
	code := r.failureCode()
	if code == "" {
		return RunOutcome{}
	}
	cause := r.cause
	if publicFailureCode(cause) != code {
		cause = apperror.Wrap(code, cause, nil)
	}
	return RunOutcome{Status: sessionruntime.RunStatusErrored, Cause: cause}
}

// deliveredOutcome is the outcome of the failure the stream already delivered
// as an error event, or zero when it delivered none. A run that returns an
// error after that, for example because persisting its turn failed, still
// records the delivered failure's code: the run's code is named once, when the
// failure is first declared, and the later error is only a diagnostic.
func (r *outcomeRecorder) deliveredOutcome() RunOutcome {
	if r == nil || r.failure == nil || r.deferred || r.runCtx.Err() != nil || publicFailureCode(r.failure) == "" {
		return RunOutcome{}
	}
	return RunOutcome{Status: sessionruntime.RunStatusErrored, Cause: r.failure}
}

// withDeliveredOutcome attaches the outcome of a failure the stream already
// delivered to err, the error a run reports after it. The error reads and
// unwraps as err; only the run's terminal write reads the outcome.
func withDeliveredOutcome(err error, delivered RunOutcome) error {
	if err == nil || delivered.Status == "" {
		return err
	}
	return &deliveredFailureError{err: err, outcome: delivered}
}

type deliveredFailureError struct {
	err     error
	outcome RunOutcome
}

func (e *deliveredFailureError) Error() string { return e.err.Error() }
func (e *deliveredFailureError) Unwrap() error { return e.err }

// failedRunOutcome is the terminal outcome of a run that reported err: the
// failure the stream delivered before err, if any, and otherwise err itself.
func failedRunOutcome(err error) RunOutcome {
	var delivered *deliveredFailureError
	if errors.As(err, &delivered) {
		return delivered.outcome
	}
	return RunOutcome{Status: sessionruntime.RunStatusErrored, Cause: err}
}

// markDelivered records that the stream delivered the run's failure as an
// error event.
func (r *outcomeRecorder) markDelivered() {
	r.delivered = true
}

// finishLifecycle writes the lifecycle terminal unless the run is parked.
func (r *outcomeRecorder) finishLifecycle(terminal func(error)) {
	if !r.deferred {
		terminal(r.cause)
	}
}

// RunOutcome is how the owner of a run reports its end to the terminal write.
// Status is a session runtime run status, or empty to leave the outcome to the
// session runtime, which derives it from what the run recorded. Cause is the
// failure the owner holds, if any.
type RunOutcome struct {
	Status string
	Cause  error
}

// ErrorCode is the code the terminal write records for the run: the catalogued
// code the cause carries, or empty to let the session runtime name it. The code
// travels in its own column; it is never written as the run's error message.
func (o RunOutcome) ErrorCode() string {
	return string(publicFailureCode(o.Cause))
}
