package sessionruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
)

// resetRacingLedger is the run ledger with a hook that runs right after an
// owner's terminal write has applied: the point at which a history reset's
// drain sees the run terminal and drops it from the live projection, before
// the owner releases that projection itself.
type resetRacingLedger struct {
	*fakeLedger
	afterFinalize func()
}

func (l *resetRacingLedger) Finalize(ctx context.Context, params ledger.FinalizeParams) (ledger.Run, bool, error) {
	run, applied, err := l.fakeLedger.Finalize(ctx, params)
	if applied && l.afterFinalize != nil {
		hook := l.afterFinalize
		l.afterFinalize = nil
		hook()
	}
	return run, applied, err
}

func newResetRacingFixture(t *testing.T) (*Manager, *resetRacingLedger) {
	t.Helper()
	runs := &resetRacingLedger{fakeLedger: newFakeLedger()}
	manager := NewManager(NewMemoryBackend(), Options{
		OwnerID: "owner-reset-race", StateTTL: time.Minute, OwnerLeaseTTL: time.Second,
		Ledger: runs, Fence: &fakeFence{},
	})
	t.Cleanup(func() { _ = manager.Close() })
	return manager, runs
}

func admitResetRaceRun(t *testing.T, manager *Manager) Admission {
	t.Helper()
	ctx := context.Background()
	admission, err := manager.Admit(ctx, AdmitInput{
		BotID: testBotID, SessionID: testSessionID, InvocationID: "inv-reset-race", Payload: []byte(`{"text":"hi"}`),
		Execution: Execution{Admission: func(context.Context, RunHandle) (RunAdmissionView, error) { return RunAdmissionView{}, nil }},
	})
	if err != nil || !admission.Started {
		t.Fatalf("admit = (%+v, %v)", admission, err)
	}
	if _, err := manager.HandleAgentEvent(ctx, admission.Handle, native.StreamEvent{Type: native.EventTextDelta, Delta: "partial"}); err != nil {
		t.Fatalf("stream text: %v", err)
	}
	return admission
}

// The owner's own terminal write landed, then the reset dropped the
// projection: the finish is the run's outcome, not a lost ownership.
func TestFinishRunAfterResetDroppedTheProjection(t *testing.T) {
	t.Parallel()
	manager, runs := newResetRacingFixture(t)
	ctx := context.Background()
	admission := admitResetRaceRun(t, manager)
	key := Key{BotID: testBotID, SessionID: testSessionID}
	runs.afterFinalize = func() {
		if err := manager.invalidateHistoryResetSnapshots(ctx, []Key{key}, true); err != nil {
			t.Errorf("drop projection: %v", err)
		}
	}

	terminal, err := manager.FinishRun(ctx, admission.Handle, RunStatusAborted)
	if err != nil {
		t.Fatalf("FinishRun() after the reset dropped the projection = %v, want the run's outcome", err)
	}
	if terminal.RunID != admission.RunID || !terminal.Applied {
		t.Fatalf("terminal = %+v, want this owner's applied write for %q", terminal, admission.RunID)
	}
	if manager.localControl(admission.RunID) != nil {
		t.Fatal("finished run kept its local control")
	}
}

// A terminal some other write made (here the reset finalizing the run as an
// orphan) is not this owner's release, even when the projection is gone.
func TestFinishRunDoesNotClaimAnotherWritersTerminal(t *testing.T) {
	t.Parallel()
	manager, runs := newResetRacingFixture(t)
	ctx := context.Background()
	admission := admitResetRaceRun(t, manager)
	if _, applied, err := runs.fakeLedger.Finalize(ctx, ledger.FinalizeParams{
		RunID: admission.RunID, FencingToken: admission.Handle.FencingToken, State: ledger.StateAborted, ErrorCode: "history_reset",
	}); err != nil || !applied {
		t.Fatalf("reset finalization = (%v, %v)", applied, err)
	}
	if err := manager.invalidateHistoryResetSnapshots(ctx, []Key{{BotID: testBotID, SessionID: testSessionID}}, true); err != nil {
		t.Fatal(err)
	}

	terminal, err := manager.FinishRun(ctx, admission.Handle, RunStatusCompleted)
	if !errors.Is(err, ErrRunOwnershipLost) {
		t.Fatalf("FinishRun() = (%+v, %v), want ErrRunOwnershipLost for a terminal this owner did not write", terminal, err)
	}
}

// A memory-backend finish handed to the reaper ends once the run is terminal
// and the projection no longer holds it, instead of failing every tick.
func TestLocalFinishHandoffEndsWhenResetDroppedTheProjection(t *testing.T) {
	t.Parallel()
	manager, runs := newResetRacingFixture(t)
	ctx := context.Background()
	admission := admitResetRaceRun(t, manager)
	manager.handoffLocalDurableFinish(admission.Handle)
	if _, applied, err := runs.fakeLedger.Finalize(ctx, ledger.FinalizeParams{
		RunID: admission.RunID, FencingToken: admission.Handle.FencingToken, State: ledger.StateAborted, ErrorCode: "history_reset",
	}); err != nil || !applied {
		t.Fatalf("reset finalization = (%v, %v)", applied, err)
	}
	if err := manager.invalidateHistoryResetSnapshots(ctx, []Key{{BotID: testBotID, SessionID: testSessionID}}, true); err != nil {
		t.Fatal(err)
	}

	if err := manager.reconcileLocalFinishHandoffs(ctx); err != nil {
		t.Fatalf("reconcile handoffs = %v, want the handoff settled", err)
	}
	manager.mu.Lock()
	pending := len(manager.localFinishHandoffs)
	manager.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending handoffs = %d, want none", pending)
	}
}

// The pass after the deletion restarts every projection so subscribers read
// the ledger again, but it never takes an active run off its owner.
func TestHistoryResetRestartKeepsActiveRun(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)
	ctx := context.Background()
	admission, err := f.manager.Admit(ctx, f.input("inv-active", `{"text":"hi"}`))
	if err != nil || !admission.Started {
		t.Fatalf("admit = (%+v, %v)", admission, err)
	}
	key := Key{BotID: testBotID, SessionID: testSessionID}
	before, _, err := f.manager.backend.Load(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.manager.invalidateHistoryResetSnapshots(ctx, []Key{key}, false); err != nil {
		t.Fatalf("restart projections: %v", err)
	}
	after, _, err := f.manager.backend.Load(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if after.CurrentRunView == nil || after.CurrentRunView.RunID != admission.RunID || after.Epoch != before.Epoch {
		t.Fatalf("active projection after restart = %+v (epoch %q), want it untouched (epoch %q)", after.CurrentRunView, after.Epoch, before.Epoch)
	}
	f.finish(t, admission)
}

// A bot-wide reset that cannot name the sessions it covers cannot clear their
// projections, so it fails before any deletion and gives its lease back.
func TestBeginBotHistoryResetFailsClosedWithoutSessionList(t *testing.T) {
	t.Parallel()
	runs := newFakeResetLedger()
	runs.botSessionsErr = errors.New("list failed")
	manager, backend := newResetTestManager(t, runs)

	if _, _, err := manager.BeginBotHistoryReset(context.Background(), "bot-1"); err == nil {
		t.Fatal("bot history reset began without its session list")
	}
	if released := runs.releasedLeases(); len(released) != 1 || released[0].Scope != ledger.ResetScopeBot {
		t.Fatalf("durable release = %#v, want the bot lease returned", released)
	}
	if _, blocked, err := backend.EffectiveHistoryReset(context.Background(), ResetScope{BotID: "bot-1"}); err != nil || blocked {
		t.Fatalf("live mirror after the failed reset = (%v, %v), want released", blocked, err)
	}
}
