package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
)

var errInvalidOwnerTerminalState = errors.New("session runtime: invalid owner terminal state")

// runErrorRunFailed is the session_runs.error_code of a failed run whose owner
// reported no code. Like the reaper's codes it is persisted, so it keeps its
// value; it is registered in the apperror catalog under the same value.
const runErrorRunFailed = "runtime_run_failed"

// ledgerFailureCode is the error code a terminal write persists. A failed run
// always carries one: when its owner named none, it is runErrorRunFailed. Both
// durable entry points apply it, because a proposal is kept as written and the
// finalize write cannot fill in a code the proposal left empty.
func ledgerFailureCode(state ledger.State, errorCode string) string {
	errorCode = strings.TrimSpace(errorCode)
	if state == ledger.StateFailed && errorCode == "" {
		return runErrorRunFailed
	}
	return errorCode
}

// prepareLedgerFinish makes the proposed owner outcome durable while the run
// remains active. The reaper may later pass StateLost to Finalize, but the
// ledger resolves a prepared run to this proposal instead. That is the crash
// recovery boundary between a genuine vanished owner and a run whose terminal
// output was already accepted.
func (m *Manager) prepareLedgerFinish(
	ctx context.Context,
	handle RunHandle,
	status, errorCode string,
	allowWaitingDecision bool,
) (ledger.Run, error) {
	state := terminalLedgerState(status, errorCode)
	// Lost is a reaper-only conclusion: an owner cannot authoritatively claim
	// that it disappeared. Reject it at the persistence boundary so a future
	// caller cannot turn a deterministic misuse into an endless durable retry.
	if state == ledger.StateLost || !state.Terminal() {
		return ledger.Run{}, fmt.Errorf("%w: %q", errInvalidOwnerTerminalState, state)
	}
	errorCode = ledgerFailureCode(state, errorCode)
	if m.runs == nil || handle.FencingToken <= 0 {
		return ledger.Run{
			State:             ledger.StateFinishing,
			ProposedState:     state,
			ProposedErrorCode: errorCode,
		}, nil
	}
	run, applied, err := m.runs.PrepareFinish(ctx, ledger.PrepareFinishParams{
		RunID:                handle.RunID,
		FencingToken:         handle.FencingToken,
		State:                state,
		ErrorCode:            errorCode,
		AllowWaitingDecision: allowWaitingDecision,
	})
	if err != nil {
		return ledger.Run{}, fmt.Errorf("prepare runtime run finish: %w", err)
	}
	if applied {
		return run, nil
	}
	run, err = m.runs.Get(ctx, handle.RunID)
	if err != nil {
		return ledger.Run{}, fmt.Errorf("load runtime run after unapplied finish proposal: %w", err)
	}
	if run.FencingToken != handle.FencingToken {
		return run, ErrRunOwnershipLost
	}
	if run.State == ledger.StateWaitingDecision && !allowWaitingDecision {
		return run, nil
	}
	if run.State == ledger.StateFinishing || run.State.Terminal() {
		return run, nil
	}
	return run, ErrRunOwnershipLost
}

// finalizeLedgerRun records the run's terminal state durably, fenced by the
// token its owner holds.
//
// It runs before the live release, and that order is the same one the reaper
// uses for the same reason: the live lease is the only pointer a reaper has to
// an unfinished run, so releasing it before the durable write would strand a row
// that says `running` with nothing left to notice. Failing this write therefore
// means the caller must leave the lease alone and let it expire — the reaper
// then resolves a prepared proposal to its intended terminal outcome. A run
// that never crossed the durable proposal boundary still becomes `lost`.
//
// Backend-only reservation tests use zero fencing tokens and have no durable
// row to transition. Production admission always supplies a positive token.
func (m *Manager) finalizeLedgerRun(ctx context.Context, handle RunHandle, status, errorCode string) (TerminalRun, error) {
	if m.runs == nil || handle.FencingToken <= 0 {
		return TerminalRun{}, nil
	}
	state := terminalLedgerState(status, errorCode)
	errorCode = ledgerFailureCode(state, errorCode)
	run, applied, err := m.runs.Finalize(ctx, ledger.FinalizeParams{
		RunID:        handle.RunID,
		FencingToken: handle.FencingToken,
		State:        state,
		ErrorCode:    errorCode,
	})
	if err != nil {
		return TerminalRun{}, fmt.Errorf("finalize runtime run: %w", err)
	}
	if !applied {
		// Already terminal, or superseded by a newer owner. Both mean this
		// token has nothing left to write, which is an ordinary outcome for a
		// retried finish rather than a failure to report.
		m.logger.DebugContext(ctx, "runtime run terminal write did not apply",
			slog.String("run_id", handle.RunID),
			slog.String("state", string(state)))
		run, err = m.runs.Get(ctx, handle.RunID)
		if err != nil {
			return TerminalRun{}, fmt.Errorf("load authoritative runtime terminal: %w", err)
		}
		if !run.State.Terminal() {
			return TerminalRun{}, ErrRunOwnershipLost
		}
	}
	terminal := terminalRunFromLedger(run)
	terminal.Applied = applied
	if run.RunID != handle.RunID || run.BotID != handle.BotID || run.SessionID != handle.SessionID {
		return TerminalRun{}, ErrRunOwnershipLost
	}
	if run.FencingToken != handle.FencingToken {
		return terminal, ErrRunOwnershipLost
	}
	return terminal, nil
}

func terminalRunFromLedger(run ledger.Run) TerminalRun {
	return TerminalRun{
		RunID:        run.RunID,
		BotID:        run.BotID,
		SessionID:    run.SessionID,
		FencingToken: run.FencingToken,
		State:        string(run.State),
		ErrorCode:    run.ErrorCode,
	}
}

// terminalLedgerState maps a live run status to its durable terminal state. The
// live vocabulary is larger than the durable one on purpose — `admitting` and
// `aborting` are transitions an owner passes through, not ways a run can end —
// so this collapses rather than translates.
func terminalLedgerState(status, errorCode string) ledger.State {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case RunStatusAborted, RunStatusAborting:
		return ledger.StateAborted
	case RunStatusErrored:
		return ledger.StateFailed
	case RunStatusCompleted:
		return ledger.StateCompleted
	case RunStatusLost:
		return ledger.StateLost
	}
	// An empty status means the caller left the outcome to be derived. A code
	// is only set when something went wrong, so it is the signal.
	if strings.TrimSpace(errorCode) != "" {
		return ledger.StateFailed
	}
	return ledger.StateCompleted
}
