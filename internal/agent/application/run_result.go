package application

import (
	"context"
	"log/slog"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errlog"
	"github.com/felinics/memoh/internal/errs"
)

// runResultOperation names an agent run in its result record.
const runResultOperation = "agent.run"

type runOutcomeKey struct{}

type runOutcomeValue struct {
	runID   string
	outcome RunOutcome
	// async is whether nobody is waiting for the run's terminal frame. The
	// side that admitted the run decides it from the run's mode.
	async bool
}

// WithRunOutcome carries the owner's outcome of run runID to the run's
// terminal observer. The owner passes the returned context to its terminal
// write, so the run's result record can report the cause the owner held; the
// terminal state itself only carries a code. Detaching the context from its
// cancellation keeps the value, so a durable finish retry still reports it.
//
// The value names its run because contexts outlive runs: a follow-up run
// started from a terminal observation inherits the finished run's context.
//
// The run is recorded as one a user is waiting for. A run admitted for
// nobody in particular carries that through withRunOutcome instead.
func WithRunOutcome(ctx context.Context, runID string, outcome RunOutcome) context.Context {
	return withRunOutcome(ctx, runID, outcome, sessionmode.Chat)
}

// withRunOutcome is WithRunOutcome for a run admitted in mode. A mode no user
// is in the loop of (schedule, discuss, subagent) records the run as
// asynchronous.
func withRunOutcome(ctx context.Context, runID string, outcome RunOutcome, mode string) context.Context {
	return context.WithValue(ctx, runOutcomeKey{}, runOutcomeValue{runID: runID, outcome: outcome, async: !sessionmode.IsInteractive(mode)})
}

// runOutcomeFrom is the value ctx carries for run runID, or zero.
func runOutcomeFrom(ctx context.Context, runID string) runOutcomeValue {
	value, ok := ctx.Value(runOutcomeKey{}).(runOutcomeValue)
	if !ok || value.runID != runID {
		return runOutcomeValue{}
	}
	return value
}

// logRunResult writes the one result record of an agent run.
//
// It runs on the terminal observer, the one place every way a run ends
// passes through: its owner's terminal write, a durable retry of that write,
// the reaper, the recovery of an in-memory finish handoff, and shutdown. The
// observer fires more than once for some runs (a replay after a crash, a
// retry that finds the run already terminal, a stale owner told a newer
// fence ended it), and only the observation whose own write applied the
// transition is recorded. A process that crashes between its write and the
// observation leaves the run without a record; a replay cannot tell that
// case from one that was recorded, and a duplicate would misstate how many
// runs failed.
//
// The level follows the fault: a server or dependency failure is an error, a
// client failure and a stop are not. A run started by a user who sees its
// terminal frame is attributed like a request; a run no user is waiting for
// (schedule, discuss, subagent) is asynchronous, and a client fault in it is
// this process's fault.
func (s *Service) logRunResult(ctx context.Context, terminal sessionruntime.TerminalRun) {
	if s.logger == nil || !terminal.Applied {
		return
	}
	owner := runOutcomeFrom(ctx, terminal.RunID)
	cause := owner.outcome.Cause
	if cause == nil {
		cause = terminal.Cause
	}
	failure := runResultError(terminal, cause)
	result := errlog.Finish(ctx, runResultOperation, failure, errlog.Options{Async: owner.async})
	attrs := append([]slog.Attr{
		slog.String("operation", runResultOperation),
		slog.String("run_id", terminal.RunID),
		slog.String("bot_id", terminal.BotID),
		slog.String("session_id", terminal.SessionID),
		slog.String("state", terminal.State),
		slog.String("error_code", terminal.ErrorCode),
	}, result.Attrs()...)
	s.logger.LogAttrs(ctx, result.Level, "agent run", attrs...)
}

// runResultError is the failure a run's result record reports: none for a run
// that completed or was stopped, and otherwise the cause under the code the
// run recorded. The cause is the owner's, or the session runtime's when it
// ended the run itself. A run that ended without either, such as one the
// reaper found, has no cause, and its failure is the recorded code alone.
func runResultError(terminal sessionruntime.TerminalRun, cause error) error {
	switch ledger.State(terminal.State) {
	case ledger.StateCompleted, ledger.StateAborted:
		return nil
	}
	code := apperror.Code(terminal.ErrorCode)
	if cause != nil {
		if code == "" || publicFailureCode(cause) == code {
			return cause
		}
		return apperror.Wrap(code, cause, nil)
	}
	if code == "" {
		return errs.NewWithDepth(1, "agent run ended without a recorded cause")
	}
	return errs.WrapWithDepth(1, apperror.New(code, nil), "agent run ended without its owner's cause")
}
