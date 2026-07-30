package sessionruntime

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/memohai/memoh/internal/agent/runtime/session/ledger"
)

// finalizeLedgerRun records the run's terminal state durably, fenced by the
// token its owner holds.
//
// It runs before the live release, and that order is the same one the reaper
// uses for the same reason: the live lease is the only pointer a reaper has to
// an unfinished run, so releasing it before the durable write would strand a row
// that says `running` with nothing left to notice. Failing this write therefore
// means the caller must leave the lease alone and let it expire — the reaper
// then transitions the run to `lost`, which is wrong about how the turn ended but
// safe about the session's active slot.
//
// A zero fencing token means the run was started through a pre-ledger entry
// point and has no durable row to transition, not that fencing was skipped.
func (m *Manager) finalizeLedgerRun(ctx context.Context, handle RunHandle, status, message string) error {
	if m.runs == nil || handle.FencingToken <= 0 {
		return nil
	}
	state := terminalLedgerState(status, message)
	errorCode := ""
	if state == ledger.StateFailed {
		errorCode = "runtime_run_failed"
	}
	_, applied, err := m.runs.Finalize(ctx, ledger.FinalizeParams{
		RunID:        handle.RunID,
		FencingToken: handle.FencingToken,
		State:        state,
		ErrorCode:    errorCode,
		ErrorMessage: message,
	})
	if err != nil {
		return fmt.Errorf("finalize runtime run: %w", err)
	}
	if !applied {
		// Already terminal, or superseded by a newer owner. Both mean this
		// token has nothing left to write, which is an ordinary outcome for a
		// retried finish rather than a failure to report.
		m.logger.Debug("runtime run terminal write did not apply",
			slog.String("run_id", handle.RunID),
			slog.String("state", string(state)))
	}
	return nil
}

// terminalLedgerState maps a live run status to its durable terminal state. The
// live vocabulary is larger than the durable one on purpose — `admitting` and
// `aborting` are transitions an owner passes through, not ways a run can end —
// so this collapses rather than translates.
func terminalLedgerState(status, message string) ledger.State {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case RunStatusAborted, RunStatusAborting:
		return ledger.StateAborted
	case RunStatusErrored:
		return ledger.StateFailed
	case RunStatusCompleted:
		return ledger.StateCompleted
	}
	// An empty status means the caller left the outcome to be derived. A finish
	// message is only set when something went wrong, so it is the signal.
	if strings.TrimSpace(message) != "" {
		return ledger.StateFailed
	}
	return ledger.StateCompleted
}
