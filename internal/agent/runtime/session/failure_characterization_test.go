package sessionruntime

// Characterization tests for the durable and live outcome of a failed run.
//
// Every assertion here pins the CURRENT behavior of the session runtime, not a
// desired one. They exist so that the upcoming failure-classification refactor
// (one terminal outcome per run) can prove which externally visible values it
// changes. Where the current value looks wrong, the test name or a comment says
// "current behavior" and the expected literal is the value the code writes today.
//
// Codes are spelled as string literals on purpose: renaming a constant must make
// these tests fail, because the literal is what reaches session_runs and clients.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
)

// terminalColumns is the triple a failed run leaves in session_runs.
type terminalColumns struct {
	State        ledger.State
	ErrorCode    string
	ErrorMessage string
}

func ledgerColumns(t *testing.T, runs *fakeLedger, runID string) terminalColumns {
	t.Helper()
	run, err := runs.Get(context.Background(), runID)
	if err != nil {
		t.Fatalf("load ledger run %q: %v", runID, err)
	}
	return terminalColumns{State: run.State, ErrorCode: run.ErrorCode, ErrorMessage: run.ErrorMessage}
}

func assertColumns(t *testing.T, got, want terminalColumns) {
	t.Helper()
	if got != want {
		t.Fatalf("session_runs terminal columns = %+v, want %+v", got, want)
	}
}

// liveRunView is the error part of the run view that snapshots and runtime
// deltas deliver to HTTP and WebSocket subscribers.
type liveRunView struct {
	Status    string
	ErrorCode string
	Error     string
}

func currentRunView(t *testing.T, manager *Manager) liveRunView {
	t.Helper()
	snapshot, err := manager.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.CurrentRunView == nil {
		t.Fatal("snapshot has no current run view")
	}
	run := snapshot.CurrentRunView
	return liveRunView{Status: run.Status, ErrorCode: run.ErrorCode, Error: run.Error}
}

func admitRunning(t *testing.T, f admitFixture, invocationID string) Admission {
	t.Helper()
	admission, err := f.manager.Admit(context.Background(), f.input(invocationID, `{"text":"hi"}`))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if !admission.Started {
		t.Fatalf("admission did not start: %+v", admission)
	}
	return admission
}

func handleEvent(t *testing.T, manager *Manager, handle RunHandle, event native.StreamEvent) {
	t.Helper()
	if _, err := manager.HandleAgentEvent(context.Background(), handle, event); err != nil {
		t.Fatalf("handle %s event: %v", event.Type, err)
	}
}

// Scenario 1 (failure before the stream starts): the admission builder fails
// after the live reservation exists.
//
// Current behavior: the manager finishes the run itself with FinishRun(errored,
// err.Error()), so the row carries the generic runtime_run_failed code and the
// raw cause text. The runtime_reservation_failed write that abandonClaim issues
// afterwards does not apply because the row is already terminal.
func TestCharacterizeAdmissionBuilderFailureColumns_CurrentBehavior(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)
	in := f.input("inv-builder-fail", `{"text":"a"}`)
	in.Execution.Admission = func(context.Context, RunHandle) (RunAdmissionView, error) {
		return RunAdmissionView{}, errors.New("persist user turn failed")
	}

	_, err := f.manager.Admit(context.Background(), in)
	if err == nil || err.Error() != "persist user turn failed" {
		t.Fatalf("admit error = %v, want the builder cause unchanged", err)
	}
	run, getErr := f.runs.GetByInvocation(context.Background(), testSessionID, "inv-builder-fail")
	if getErr != nil {
		t.Fatalf("load run: %v", getErr)
	}
	assertColumns(t, ledgerColumns(t, f.runs, run.RunID), terminalColumns{
		State:        ledger.StateFailed,
		ErrorCode:    "runtime_run_failed",
		ErrorMessage: "persist user turn failed",
	})
	if view := currentRunView(t, f.manager); view != (liveRunView{Status: "errored", ErrorCode: "runtime_run_failed", Error: "persist user turn failed"}) {
		t.Fatalf("run view = %+v", view)
	}
}

// Scenario 5 (reservation failure): the live backend already holds another
// active run for the session, so the reservation returns an error.
func TestCharacterizeReservationFailedColumns(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)
	ctx := context.Background()
	key := Key{BotID: testBotID, SessionID: testSessionID}
	if _, _, err := f.manager.backend.Update(ctx, key, func(snapshot Snapshot, _ bool) (Snapshot, bool, error) {
		snapshot.BotID = testBotID
		snapshot.SessionID = testSessionID
		snapshot.CurrentRunView = &CurrentRunView{RunID: "run-foreign", Status: RunStatusRunning, StartedAt: time.Now(), UpdatedAt: time.Now()}
		return snapshot, true, nil
	}); err != nil {
		t.Fatalf("seed live run: %v", err)
	}

	_, err := f.manager.Admit(ctx, f.input("inv-reserve-fail", `{"text":"a"}`))
	const cause = `session "session-runtime" already has an active runtime run`
	if err == nil || err.Error() != cause {
		t.Fatalf("admit error = %v, want %q", err, cause)
	}
	run, getErr := f.runs.GetByInvocation(ctx, testSessionID, "inv-reserve-fail")
	if getErr != nil {
		t.Fatalf("load run: %v", getErr)
	}
	// Current behavior: error_message stores the raw reservation cause.
	assertColumns(t, ledgerColumns(t, f.runs, run.RunID), terminalColumns{
		State:        ledger.StateFailed,
		ErrorCode:    "runtime_reservation_failed",
		ErrorMessage: cause,
	})
}

// decliningBackend is a memory backend whose reservation declines without an
// error, which is how a distributed backend answers when another owner holds
// the session's live slot.
type decliningBackend struct {
	*MemoryBackend
}

func (decliningBackend) StartRunIfNoHistoryReset(context.Context, Key, SnapshotUpdate) (Snapshot, bool, error) {
	return Snapshot{}, false, nil
}

// Scenario 5 (reservation declined): the live backend declines without an error.
//
// Current behavior: error_message is the text of ErrRunOwnershipLost and the
// caller receives ErrRunOwnershipLost itself.
func TestCharacterizeReservationDeclinedColumns_CurrentBehavior(t *testing.T) {
	t.Parallel()
	runs := newFakeLedger()
	manager := NewManager(decliningBackend{MemoryBackend: NewMemoryBackend()}, Options{
		OwnerID:       "owner-declined",
		StateTTL:      time.Minute,
		OwnerLeaseTTL: time.Second,
		Ledger:        runs,
		Fence:         &fakeFence{},
	})
	t.Cleanup(func() { _ = manager.Close() })
	f := admitFixture{manager: manager, runs: runs, executions: new(int64)}

	_, err := manager.Admit(context.Background(), f.input("inv-declined", `{"text":"a"}`))
	if !errors.Is(err, ErrRunOwnershipLost) {
		t.Fatalf("admit error = %v, want ErrRunOwnershipLost", err)
	}
	run, getErr := runs.GetByInvocation(context.Background(), testSessionID, "inv-declined")
	if getErr != nil {
		t.Fatalf("load run: %v", getErr)
	}
	assertColumns(t, ledgerColumns(t, runs, run.RunID), terminalColumns{
		State:        ledger.StateFailed,
		ErrorCode:    "runtime_reservation_declined",
		ErrorMessage: "runtime run ownership was lost",
	})
}

// Scenario 5 (fence activation): the persistence fence cannot be activated.
//
// Current behavior: error_message is the wrapped fence error text.
func TestCharacterizeFenceActivationFailedColumns_CurrentBehavior(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)
	f.fence.setErr(errors.New("fence is stale"))

	_, err := f.manager.Admit(context.Background(), f.input("inv-fence-fail", `{"text":"a"}`))
	const cause = "activate runtime persistence fence: fence is stale"
	if err == nil || err.Error() != cause {
		t.Fatalf("admit error = %v, want %q", err, cause)
	}
	run, getErr := f.runs.GetByInvocation(context.Background(), testSessionID, "inv-fence-fail")
	if getErr != nil {
		t.Fatalf("load run: %v", getErr)
	}
	assertColumns(t, ledgerColumns(t, f.runs, run.RunID), terminalColumns{
		State:        ledger.StateFailed,
		ErrorCode:    "runtime_fence_activation_failed",
		ErrorMessage: cause,
	})
}

// Scenario 10 / WS path: finishWSRun passes the public code as the finish
// MESSAGE (FinishRun(errored, code)), never as the error code.
//
// Current behavior: with no error recorded on the live run, error_code falls
// back to runtime_run_failed and error_message holds the public code string.
func TestCharacterizeFinishRunWithCodeAsMessage_CurrentBehavior(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)
	admission := admitRunning(t, f, "inv-ws-code-as-message")

	if _, err := f.manager.FinishRun(context.Background(), admission.Handle, RunStatusErrored, "agent.response_interrupted"); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	assertColumns(t, ledgerColumns(t, f.runs, admission.RunID), terminalColumns{
		State:        ledger.StateFailed,
		ErrorCode:    "runtime_run_failed",
		ErrorMessage: "agent.response_interrupted",
	})
	if view := currentRunView(t, f.manager); view != (liveRunView{Status: "errored", ErrorCode: "runtime_run_failed", Error: "agent.response_interrupted"}) {
		t.Fatalf("run view = %+v", view)
	}
}

// Scenario 10 / WS path with a non-apperror cause: apperror.CodeOf returns "",
// so finishWSRun calls FinishRun(errored, "").
func TestCharacterizeFinishRunErroredWithoutCodeOrMessage(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)
	admission := admitRunning(t, f, "inv-ws-plain")

	if _, err := f.manager.FinishRun(context.Background(), admission.Handle, RunStatusErrored, ""); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	assertColumns(t, ledgerColumns(t, f.runs, admission.RunID), terminalColumns{
		State:     ledger.StateFailed,
		ErrorCode: "runtime_run_failed",
	})
	if view := currentRunView(t, f.manager); view != (liveRunView{Status: "errored", ErrorCode: "runtime_run_failed"}) {
		t.Fatalf("run view = %+v", view)
	}
}

// Scenario 2/3: the stream published EventError with a catalog code before the
// owner finished, and no terminal event proposed the outcome. The finish call
// names the code: finishing errored without one records runtime_run_failed,
// not the code the live run last saw.
func TestCharacterizeFinishCodeComesFromCaller(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		finish func(*Manager, RunHandle) error
		want   terminalColumns
		view   liveRunView
	}{
		{
			// turnRunFinisher shape: FinishRunWithErrorCode(errored, CodeOf(cause)="").
			name: "finish with empty error code",
			finish: func(m *Manager, h RunHandle) error {
				_, err := m.FinishRunWithErrorCode(context.Background(), h, RunStatusErrored, "")
				return err
			},
			want: terminalColumns{State: ledger.StateFailed, ErrorCode: "runtime_run_failed"},
			// Current behavior: the live view keeps the event's public detail as
			// its error text while the ledger row has no message.
			view: liveRunView{Status: "errored", ErrorCode: "runtime_run_failed", Error: "The model provider is overloaded."},
		},
		{
			// finishWSRun shape: FinishRun(errored, CodeOf(cause)).
			name: "finish with code as message",
			finish: func(m *Manager, h RunHandle) error {
				_, err := m.FinishRun(context.Background(), h, RunStatusErrored, "agent.provider_overloaded")
				return err
			},
			want: terminalColumns{State: ledger.StateFailed, ErrorCode: "runtime_run_failed", ErrorMessage: "agent.provider_overloaded"},
			view: liveRunView{Status: "errored", ErrorCode: "runtime_run_failed", Error: "agent.provider_overloaded"},
		},
		{
			// A different explicit code wins over the live one.
			name: "finish with explicit code",
			finish: func(m *Manager, h RunHandle) error {
				_, err := m.FinishRunWithErrorCode(context.Background(), h, RunStatusErrored, "agent.response_interrupted")
				return err
			},
			want: terminalColumns{State: ledger.StateFailed, ErrorCode: "agent.response_interrupted"},
			view: liveRunView{Status: "errored", ErrorCode: "agent.response_interrupted", Error: "The model provider is overloaded."},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newAdmitFixture(t)
			admission := admitRunning(t, f, "inv-live-code")
			handleEvent(t, f.manager, admission.Handle, native.StreamEvent{
				Type:  native.EventError,
				Code:  "agent.provider_overloaded",
				Error: "The model provider is overloaded.",
			})
			if err := tc.finish(f.manager, admission.Handle); err != nil {
				t.Fatalf("finish: %v", err)
			}
			assertColumns(t, ledgerColumns(t, f.runs, admission.RunID), tc.want)
			if view := currentRunView(t, f.manager); view != tc.view {
				t.Fatalf("run view = %+v, want %+v", view, tc.view)
			}
		})
	}
}

// Scenario 3: an EventError followed by EventAgentEnd writes the durable
// proposal from the live run (prepareAgentTerminalEvent). An EventError without
// a code falls back to runtime_run_failed there as well.
func TestCharacterizeAgentEndAfterEventErrorColumns(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		event native.StreamEvent
		want  terminalColumns
		view  liveRunView
	}{
		{
			name:  "coded event error",
			event: native.StreamEvent{Type: native.EventError, Code: "agent.provider_rate_limited", Error: "rate limited"},
			want:  terminalColumns{State: ledger.StateFailed, ErrorCode: "agent.provider_rate_limited"},
			view:  liveRunView{Status: "errored", ErrorCode: "agent.provider_rate_limited", Error: "rate limited"},
		},
		{
			// Current behavior: an uncoded native error text (for example a raw
			// provider message) stays in the live run view.
			name:  "uncoded event error",
			event: native.StreamEvent{Type: native.EventError, Error: "api error 503: upstream"},
			want:  terminalColumns{State: ledger.StateFailed, ErrorCode: "runtime_run_failed"},
			view:  liveRunView{Status: "errored", ErrorCode: "runtime_run_failed", Error: "api error 503: upstream"},
		},
		{
			name:  "empty event error",
			event: native.StreamEvent{Type: native.EventError},
			want:  terminalColumns{State: ledger.StateFailed, ErrorCode: "runtime_run_failed"},
			view:  liveRunView{Status: "errored", ErrorCode: "runtime_run_failed", Error: "stream error"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newAdmitFixture(t)
			admission := admitRunning(t, f, "inv-agent-end")
			handleEvent(t, f.manager, admission.Handle, tc.event)
			handleEvent(t, f.manager, admission.Handle, native.StreamEvent{Type: native.EventAgentEnd})

			run, err := f.runs.Get(context.Background(), admission.RunID)
			if err != nil {
				t.Fatalf("load run: %v", err)
			}
			if run.State != ledger.StateFinishing || run.ProposedState != tc.want.State ||
				run.ProposedErrorCode != tc.want.ErrorCode || run.ProposedErrorMessage != tc.want.ErrorMessage {
				t.Fatalf("proposal = state:%q proposed:%q code:%q message:%q, want %+v",
					run.State, run.ProposedState, run.ProposedErrorCode, run.ProposedErrorMessage, tc.want)
			}
			// The owner's clean return (FinishRun with nothing) finalizes the proposal.
			if _, err := f.manager.FinishRun(context.Background(), admission.Handle, "", ""); err != nil {
				t.Fatalf("finish run: %v", err)
			}
			assertColumns(t, ledgerColumns(t, f.runs, admission.RunID), tc.want)
			if view := currentRunView(t, f.manager); view != tc.view {
				t.Fatalf("run view = %+v, want %+v", view, tc.view)
			}
		})
	}
}

// Scenario 3: a retry clears the failed attempt's error from the live run, so a
// recovered stream completes cleanly.
func TestCharacterizeRetryClearsLiveErrorBeforeCompletion(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)
	admission := admitRunning(t, f, "inv-retry")
	handleEvent(t, f.manager, admission.Handle, native.StreamEvent{Type: native.EventError, Error: "api error 503"})
	handleEvent(t, f.manager, admission.Handle, native.StreamEvent{Type: native.EventRetry})
	if view := currentRunView(t, f.manager); view.ErrorCode != "" || view.Error != "" {
		t.Fatalf("run view after retry = %+v, want no error", view)
	}
	handleEvent(t, f.manager, admission.Handle, native.StreamEvent{Type: native.EventAgentEnd})
	if _, err := f.manager.FinishRun(context.Background(), admission.Handle, "", ""); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	assertColumns(t, ledgerColumns(t, f.runs, admission.RunID), terminalColumns{State: ledger.StateCompleted})
}

// Scenario 3: a retry that runs out of attempts publishes its own final
// EventError. Without a terminal event the durable row records only what the
// finish call names, so an errored finish without a code is runtime_run_failed.
func TestCharacterizeRetryThenTerminalEventErrorColumns(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)
	admission := admitRunning(t, f, "inv-retry-exhausted")
	handleEvent(t, f.manager, admission.Handle, native.StreamEvent{Type: native.EventError, Error: "api error 503"})
	handleEvent(t, f.manager, admission.Handle, native.StreamEvent{Type: native.EventRetry})
	handleEvent(t, f.manager, admission.Handle, native.StreamEvent{
		Type: native.EventError, Code: "agent.provider_overloaded", Error: "The model provider is overloaded.",
	})
	if _, err := f.manager.FinishRunWithErrorCode(context.Background(), admission.Handle, RunStatusErrored, ""); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	assertColumns(t, ledgerColumns(t, f.runs, admission.RunID), terminalColumns{
		State: ledger.StateFailed, ErrorCode: "runtime_run_failed",
	})
}

// Scenario 3: when the terminal event proposed the outcome, the proposal wins
// over a finish call that names no code or a different one.
func TestCharacterizeTerminalProposalWinsOverFinishCode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		code string
	}{
		{name: "finish without code"},
		{name: "finish with another code", code: "agent.response_interrupted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newAdmitFixture(t)
			admission := admitRunning(t, f, "inv-proposal-wins")
			handleEvent(t, f.manager, admission.Handle, native.StreamEvent{
				Type: native.EventError, Code: "agent.provider_overloaded", Error: "The model provider is overloaded.",
			})
			handleEvent(t, f.manager, admission.Handle, native.StreamEvent{Type: native.EventAgentAbort})
			if _, err := f.manager.FinishRunWithErrorCode(context.Background(), admission.Handle, RunStatusErrored, tc.code); err != nil {
				t.Fatalf("finish run: %v", err)
			}
			assertColumns(t, ledgerColumns(t, f.runs, admission.RunID), terminalColumns{
				State: ledger.StateFailed, ErrorCode: "agent.provider_overloaded",
			})
		})
	}
}

// Scenario 3: when the terminal event's proposal write fails, the outcome is
// deferred to finish, and the finish call's code is what the row records.
func TestCharacterizeDeferredProposalTakesFinishCode(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)
	admission := admitRunning(t, f, "inv-deferred-proposal")
	handleEvent(t, f.manager, admission.Handle, native.StreamEvent{
		Type: native.EventError, Code: "agent.provider_overloaded", Error: "The model provider is overloaded.",
	})
	f.runs.SetPrepareErr(errors.New("proposal write temporarily unavailable"))
	handleEvent(t, f.manager, admission.Handle, native.StreamEvent{Type: native.EventAgentAbort})
	if got := f.runs.State(admission.RunID); got != ledger.StateRunning {
		t.Fatalf("ledger after deferred proposal = %q, want running", got)
	}
	f.runs.SetPrepareErr(nil)
	if _, err := f.manager.FinishRunWithErrorCode(context.Background(), admission.Handle, RunStatusErrored, "agent.provider_overloaded"); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	assertColumns(t, ledgerColumns(t, f.runs, admission.RunID), terminalColumns{
		State: ledger.StateFailed, ErrorCode: "agent.provider_overloaded",
	})
}

// Scenario 8 (abort intent): an abort recorded in the ledger before the owner
// finishes resolves the reaper's lost transition to aborted with no code.
func TestCharacterizeAbortIntentBeforeReaperColumns(t *testing.T) {
	t.Parallel()
	runs := newFakeLedger()
	runs.InsertClaimed("run-abort-intent", "session-abort-intent", 5, "generation-1")
	if _, applied, err := runs.RequestAbort(context.Background(), "run-abort-intent"); err != nil || !applied {
		t.Fatalf("request abort = applied:%v err:%v", applied, err)
	}
	live := newFakeLiveness("generation-1")
	live.setCandidates(LeaseCandidate{
		Key:   Key{BotID: testBotID, SessionID: "session-abort-intent"},
		RunID: "run-abort-intent", FencingToken: 5, ExpiresAt: time.Now().Add(-time.Minute),
	})
	newTestReaper(t, runs, live).tick(context.Background())

	assertColumns(t, ledgerColumns(t, runs, "run-abort-intent"), terminalColumns{State: ledger.StateAborted})
}

// Scenario 8 (abort through the live run): the run is aborting when the owner
// returns cleanly, so the outcome is aborted and no code is written.
func TestCharacterizeAbortingRunFinishesAborted(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)
	admission := admitRunning(t, f, "inv-aborting")
	handleEvent(t, f.manager, admission.Handle, native.StreamEvent{Type: native.EventAgentAbort})
	if _, err := f.manager.FinishRun(context.Background(), admission.Handle, "", ""); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	assertColumns(t, ledgerColumns(t, f.runs, admission.RunID), terminalColumns{State: ledger.StateAborted})
	if view := currentRunView(t, f.manager); view != (liveRunView{Status: "aborted"}) {
		t.Fatalf("run view = %+v", view)
	}
}

// Scenario 6 (owner lost, no proposal): the reaper writes its own codes with no
// message.
func TestCharacterizeReaperLostColumns(t *testing.T) {
	t.Parallel()
	t.Run("owner lease expired", func(t *testing.T) {
		t.Parallel()
		runs := newFakeLedger()
		runs.InsertClaimed("run-lease", "session-lease", 5, "generation-1")
		live := newFakeLiveness("generation-1")
		live.setCandidates(LeaseCandidate{
			Key:   Key{BotID: testBotID, SessionID: "session-lease"},
			RunID: "run-lease", FencingToken: 5, ExpiresAt: time.Now().Add(-time.Minute),
		})
		newTestReaper(t, runs, live).tick(context.Background())
		assertColumns(t, ledgerColumns(t, runs, "run-lease"), terminalColumns{
			State: ledger.StateLost, ErrorCode: "runtime_owner_lease_expired",
		})
	})
	t.Run("live backend lost", func(t *testing.T) {
		t.Parallel()
		runs := newFakeLedger()
		runs.InsertClaimed("run-generation", "session-generation", 5, "generation-old")
		newTestReaper(t, runs, newFakeLiveness("generation-new")).tick(context.Background())
		assertColumns(t, ledgerColumns(t, runs, "run-generation"), terminalColumns{
			State: ledger.StateLost, ErrorCode: "runtime_live_backend_lost",
		})
	})
	t.Run("admission orphaned", func(t *testing.T) {
		t.Parallel()
		runs := newFakeLedger()
		runs.InsertOrphan("run-orphaned", "session-orphaned", "inv-orphaned", "fingerprint")
		newTestReaper(t, runs, newFakeLiveness("generation-1")).tick(context.Background())
		assertColumns(t, ledgerColumns(t, runs, "run-orphaned"), terminalColumns{
			State: ledger.StateLost, ErrorCode: "runtime_admission_orphaned",
		})
	})
}

// Scenario 7 (owner lost after a proposal): the proposal wins over the reaper's
// code, message included.
func TestCharacterizeReaperKeepsFailedProposalColumns(t *testing.T) {
	t.Parallel()
	runs := newFakeLedger()
	runs.InsertClaimed("run-proposed", "session-proposed", 5, "generation-1")
	if _, applied, err := runs.PrepareFinish(context.Background(), ledger.PrepareFinishParams{
		RunID: "run-proposed", FencingToken: 5, State: ledger.StateFailed,
		ErrorCode: "agent.provider_overloaded", ErrorMessage: "agent.provider_overloaded",
	}); err != nil || !applied {
		t.Fatalf("prepare finish = applied:%v err:%v", applied, err)
	}
	live := newFakeLiveness("generation-1")
	live.setCandidates(LeaseCandidate{
		Key:   Key{BotID: testBotID, SessionID: "session-proposed"},
		RunID: "run-proposed", FencingToken: 5, ExpiresAt: time.Now().Add(-time.Minute),
	})
	newTestReaper(t, runs, live).tick(context.Background())
	assertColumns(t, ledgerColumns(t, runs, "run-proposed"), terminalColumns{
		State: ledger.StateFailed, ErrorCode: "agent.provider_overloaded", ErrorMessage: "agent.provider_overloaded",
	})
}

// Scenario 6/7 from the owner's side: once ownership was revoked mid-run, the
// owner's terminal write is refused and nothing is written, so the reaper names
// the outcome.
func TestCharacterizeOwnerFinishAfterOwnershipLossWritesNothing(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)
	admission := admitRunning(t, f, "inv-owner-lost")
	ctrl := f.manager.localControlForHandle(admission.Handle)
	if ctrl == nil {
		t.Fatal("no local control for the admitted run")
	}
	ctrl.revokeOwnership(ErrRunOwnershipLost)

	_, err := f.manager.FinishRunWithErrorCode(context.Background(), admission.Handle, RunStatusErrored, "agent.response_interrupted")
	if !errors.Is(err, ErrRunOwnershipLost) {
		t.Fatalf("finish error = %v, want ErrRunOwnershipLost", err)
	}
	assertColumns(t, ledgerColumns(t, f.runs, admission.RunID), terminalColumns{State: ledger.StateRunning})
}

// Run view hydrated from the ledger (no live snapshot) exposes error_message as
// the run view's error text. Current behavior: whatever the row holds, including
// the raw admission cause, is delivered to subscribers.
func TestCharacterizeLedgerHydratedRunViewExposesErrorMessage_CurrentBehavior(t *testing.T) {
	t.Parallel()
	runs := newFakeLedger()
	manager := NewManager(NewMemoryBackend(), Options{
		OwnerID: "owner-hydrate", StateTTL: time.Minute, OwnerLeaseTTL: time.Second,
		Ledger: runs, Fence: &fakeFence{},
	})
	t.Cleanup(func() { _ = manager.Close() })
	f := admitFixture{manager: manager, runs: runs, fence: &fakeFence{}, executions: new(int64)}
	f.fence.setErr(errors.New("fence is stale"))
	manager.fence = f.fence

	if _, err := manager.Admit(context.Background(), f.input("inv-hydrate", `{"text":"a"}`)); err == nil {
		t.Fatal("admit should fail")
	}
	view := currentRunView(t, manager)
	want := liveRunView{
		Status:    "errored",
		ErrorCode: "runtime_fence_activation_failed",
		Error:     "activate runtime persistence fence: fence is stale",
	}
	if view != want {
		t.Fatalf("hydrated run view = %+v, want %+v", view, want)
	}
}
