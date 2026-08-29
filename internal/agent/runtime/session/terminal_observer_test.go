package sessionruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
)

func TestFinishRunObservesAuthoritativeLedgerTerminal(t *testing.T) {
	t.Parallel()
	fixture := newAdmitFixture(t)
	admission, err := fixture.manager.Admit(context.Background(), fixture.input("inv-terminal-observer", `{"text":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	var observed []TerminalRun
	observerSawActiveControl := false
	fixture.manager.SetTerminalObserver(func(_ context.Context, run TerminalRun) {
		observerSawActiveControl = fixture.manager.localControlForHandle(admission.Handle) != nil
		observed = append(observed, run)
	})

	if err := fixture.manager.FinishRun(context.Background(), admission.Handle, RunStatusErrored, "provider.unavailable"); err != nil {
		t.Fatal(err)
	}
	if len(observed) != 1 {
		t.Fatalf("terminal observations = %d, want 1", len(observed))
	}
	if observerSawActiveControl {
		t.Fatal("terminal observer ran before live owner cleanup")
	}
	want := TerminalRun{
		RunID: admission.RunID, BotID: testBotID, SessionID: testSessionID,
		FencingToken: admission.Handle.FencingToken, State: string(ledger.StateFailed),
		ErrorCode: "runtime_run_failed",
	}
	if observed[0] != want {
		t.Fatalf("terminal observation = %+v, want %+v", observed[0], want)
	}
}

func TestFinishRunWithErrorCodePersistsStableCodeWithoutDiagnostic(t *testing.T) {
	t.Parallel()
	fixture := newAdmitFixture(t)
	admission, err := fixture.manager.Admit(context.Background(), fixture.input("inv-coded-terminal", `{"text":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}

	if err := fixture.manager.FinishRunWithErrorCode(
		context.Background(), admission.Handle, RunStatusErrored, "agent.response_timeout",
	); err != nil {
		t.Fatal(err)
	}

	writes := fixture.runs.terminalWrites()
	if len(writes) != 1 {
		t.Fatalf("terminal writes = %d, want 1", len(writes))
	}
	if writes[0].ErrorCode != "agent.response_timeout" || writes[0].ErrorMessage != "" {
		t.Fatalf("terminal error = code:%q message:%q", writes[0].ErrorCode, writes[0].ErrorMessage)
	}
}

func TestUnnamedFinishCarriesProjectedStableErrorCodeToLedger(t *testing.T) {
	t.Parallel()
	fixture := newAdmitFixture(t)
	admission, err := fixture.manager.Admit(context.Background(), fixture.input("inv-projected-code", `{"text":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.HandleAgentEvent(context.Background(), admission.Handle, native.StreamEvent{
		Type: native.EventError, Code: "agent.response_interrupted", Error: "public fallback",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.HandleAgentEvent(context.Background(), admission.Handle, native.StreamEvent{Type: native.EventAgentAbort}); err != nil {
		t.Fatal(err)
	}

	if err := fixture.manager.FinishRun(context.Background(), admission.Handle, "", ""); err != nil {
		t.Fatal(err)
	}
	writes := fixture.runs.terminalWrites()
	if len(writes) != 1 || writes[0].ErrorCode != "agent.response_interrupted" || writes[0].ErrorMessage != "" {
		t.Fatalf("terminal writes = %#v", writes)
	}
}

func TestFinishRunReplaysAlreadyTerminalLedgerOutcome(t *testing.T) {
	t.Parallel()
	fixture := newAdmitFixture(t)
	admission, err := fixture.manager.Admit(context.Background(), fixture.input("inv-terminal-replay", `{"text":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, applied, err := fixture.runs.Finalize(context.Background(), ledger.FinalizeParams{
		RunID: admission.RunID, FencingToken: admission.Handle.FencingToken, State: ledger.StateCompleted,
	}); err != nil || !applied {
		t.Fatalf("seed terminal = applied:%v err:%v", applied, err)
	}
	var observed []TerminalRun
	fixture.manager.SetTerminalObserver(func(_ context.Context, run TerminalRun) {
		observed = append(observed, run)
	})

	if err := fixture.manager.FinishRun(context.Background(), admission.Handle, RunStatusCompleted, ""); err != nil {
		t.Fatal(err)
	}
	if len(observed) != 1 || observed[0].State != string(ledger.StateCompleted) {
		t.Fatalf("terminal observations = %+v, want completed replay", observed)
	}
}

func TestFinishRunObservesTerminalNewerFenceButRejectsStaleOwner(t *testing.T) {
	t.Parallel()
	fixture := newAdmitFixture(t)
	admission, err := fixture.manager.Admit(context.Background(), fixture.input("inv-terminal-newer-fence", `{"text":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	ctrl := fixture.manager.localControlForHandle(admission.Handle)
	if ctrl == nil {
		t.Fatal("admitted owner control is missing before stale finish")
	}
	leaseCtx, stopLease := context.WithCancel(context.Background())
	leaseDone := make(chan struct{})
	go func() {
		<-leaseCtx.Done()
		close(leaseDone)
	}()
	t.Cleanup(stopLease)
	ctrl.leaseLifecycleMu.Lock()
	ctrl.leaseStop = stopLease
	ctrl.leaseDone = leaseDone
	ctrl.leaseLifecycleMu.Unlock()
	fixture.runs.mu.Lock()
	run := fixture.runs.runs[admission.RunID]
	run.FencingToken++
	run.State = ledger.StateAborted
	newToken := run.FencingToken
	fixture.runs.mu.Unlock()
	var observed []TerminalRun
	fixture.manager.SetTerminalObserver(func(_ context.Context, run TerminalRun) {
		observed = append(observed, run)
	})

	err = fixture.manager.FinishRun(context.Background(), admission.Handle, RunStatusCompleted, "")
	if !errors.Is(err, ErrRunOwnershipLost) {
		t.Fatalf("FinishRun() error = %v, want ErrRunOwnershipLost", err)
	}
	if len(observed) != 1 || observed[0].State != string(ledger.StateAborted) || observed[0].FencingToken != newToken {
		t.Fatalf("terminal observations = %+v, want authoritative newer aborted row", observed)
	}
	if fixture.manager.localControlForHandle(admission.Handle) != nil {
		t.Fatal("stale owner control remains after authoritative terminal observation")
	}
	select {
	case <-leaseDone:
	default:
		t.Fatal("stale owner lease renewal remains active after authoritative terminal observation")
	}
}

func TestFinishRunDoesNotObserveWaitingDecision(t *testing.T) {
	t.Parallel()
	fixture := newAdmitFixture(t)
	admission, err := fixture.manager.Admit(context.Background(), fixture.input("inv-terminal-waiting", `{"text":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.HandleAgentEvent(context.Background(), admission.Handle, native.StreamEvent{
		Type: native.EventToolApprovalRequest, ToolName: "exec", ToolCallID: "call-waiting",
		ApprovalID: "approval-waiting", Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	var observed []TerminalRun
	fixture.manager.SetTerminalObserver(func(_ context.Context, run TerminalRun) {
		observed = append(observed, run)
	})

	if err := fixture.manager.FinishRun(context.Background(), admission.Handle, "", ""); err != nil {
		t.Fatal(err)
	}
	if len(observed) != 0 {
		t.Fatalf("waiting decision emitted terminal observations: %+v", observed)
	}
	if got := fixture.runs.state(admission.RunID); got != ledger.StateWaitingDecision {
		t.Fatalf("ledger state = %q, want waiting_decision", got)
	}
}
