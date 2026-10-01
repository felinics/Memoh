package sessionruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
)

// A terminal event that carries a code proposes the run's outcome with that
// code, ahead of the last stream error the live view saw.
func TestTerminalEventCodeWinsOverLiveError(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)
	admission := admitRunning(t, f, "inv-event-code")
	handleEvent(t, f.manager, admission.Handle, native.StreamEvent{Type: native.EventError, Code: "agent.provider_overloaded", Error: "overloaded"})
	handleEvent(t, f.manager, admission.Handle, native.StreamEvent{Type: native.EventAgentAbort, Code: "agent.provider_rate_limited"})
	if _, err := f.manager.FinishRun(context.Background(), admission.Handle, "", ""); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	assertColumns(t, ledgerColumns(t, f.runs, admission.RunID), terminalColumns{
		State: ledger.StateFailed, ErrorCode: "agent.provider_rate_limited",
	})
}

// A terminal event may be replayed after the ledger row is final. The replay is
// accepted only when it names the same outcome, and the code is part of that:
// a coded abort names a failure, an uncoded one on a run without a live error
// names an abort. The owner stamps every copy of the event from the same
// recorder, so its replays agree.
func TestReplayedTerminalEventMustCarryTheSameCode(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)
	admission := admitRunning(t, f, "inv-replay")
	stamped := native.StreamEvent{Type: native.EventAgentAbort, Code: "agent.response_timeout"}
	handleEvent(t, f.manager, admission.Handle, stamped)
	if _, _, err := f.runs.Finalize(context.Background(), ledger.FinalizeParams{
		RunID: admission.RunID, FencingToken: admission.Handle.FencingToken, State: ledger.StateFailed,
	}); err != nil {
		t.Fatalf("finalize ledger: %v", err)
	}
	assertColumns(t, ledgerColumns(t, f.runs, admission.RunID), terminalColumns{
		State: ledger.StateFailed, ErrorCode: "agent.response_timeout",
	})

	if _, err := f.manager.HandleAgentEvent(context.Background(), admission.Handle, stamped); err != nil {
		t.Fatalf("replay of the same terminal event = %v, want accepted", err)
	}
	unstamped := native.StreamEvent{Type: native.EventAgentAbort}
	if _, err := f.manager.HandleAgentEvent(context.Background(), admission.Handle, unstamped); !errors.Is(err, ErrRunOwnershipLost) {
		t.Fatalf("replay without the code = %v, want ErrRunOwnershipLost", err)
	}
}
