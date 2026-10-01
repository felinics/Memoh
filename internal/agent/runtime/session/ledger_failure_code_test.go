package sessionruntime

import (
	"context"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
)

// A failed proposal written without a code still carries runtime_run_failed,
// and the reaper's later lost transition keeps it. The finalize write cannot
// fill in a code for a proposal, so the proposal itself must hold one.
func TestFailedProposalWithoutCodeCarriesRunFailed(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)
	admission := admitRunning(t, f, "inv-null-code-proposal")

	prepared, err := f.manager.prepareLedgerFinish(context.Background(), admission.Handle, RunStatusErrored, "", "", true)
	if err != nil {
		t.Fatalf("prepare finish: %v", err)
	}
	if prepared.State != ledger.StateFinishing || prepared.ProposedState != ledger.StateFailed || prepared.ProposedErrorCode != "runtime_run_failed" {
		t.Fatalf("proposal = state:%q proposed:%q code:%q, want finishing failed runtime_run_failed",
			prepared.State, prepared.ProposedState, prepared.ProposedErrorCode)
	}

	// The reaper's transition for a vanished owner.
	if _, _, err := f.runs.Finalize(context.Background(), ledger.FinalizeParams{
		RunID: admission.RunID, FencingToken: admission.Handle.FencingToken,
		State: ledger.StateLost, ErrorCode: "runtime_owner_lease_expired",
	}); err != nil {
		t.Fatalf("reaper finalize: %v", err)
	}
	assertColumns(t, ledgerColumns(t, f.runs, admission.RunID), terminalColumns{
		State: ledger.StateFailed, ErrorCode: "runtime_run_failed",
	})
}

func TestLedgerFailureCode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		state ledger.State
		code  string
		want  string
	}{
		{state: ledger.StateFailed, code: "", want: "runtime_run_failed"},
		{state: ledger.StateFailed, code: "  ", want: "runtime_run_failed"},
		{state: ledger.StateFailed, code: "agent.response_timeout", want: "agent.response_timeout"},
		{state: ledger.StateCompleted, code: "", want: ""},
		{state: ledger.StateAborted, code: "", want: ""},
		{state: ledger.StateLost, code: "", want: ""},
		{state: ledger.StateAborted, code: "history_reset", want: "history_reset"},
	} {
		if got := ledgerFailureCode(tc.state, tc.code); got != tc.want {
			t.Errorf("ledgerFailureCode(%q, %q) = %q, want %q", tc.state, tc.code, got, tc.want)
		}
	}
}
