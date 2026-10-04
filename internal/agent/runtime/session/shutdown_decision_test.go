package sessionruntime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	"github.com/felinics/memoh/internal/runtimefence"
)

type shutdownDecisionFixture struct {
	manager   *Manager
	backend   *retryingRecoveryBackend
	runs      *fakeLedger
	decisions *fakeDecisionStore
	fence     *waitingDecisionRecoveryFence
	handle    RunHandle
}

func newShutdownDecisionFixture(t *testing.T) shutdownDecisionFixture {
	t.Helper()
	const runID = "shutdown-parked"
	runs := newFakeLedger()
	runs.InsertClaimed(runID, testSessionID, 5, "generation-old")
	if _, _, err := runs.SetWaitingDecision(t.Context(), runID, 5); err != nil {
		t.Fatal(err)
	}
	runs.Token = 5
	decisions := &fakeDecisionStore{target: DecisionTarget{
		Type: CommandToolApprovalResponse, ID: "approval", BotID: testBotID, SessionID: testSessionID,
		RunID: runID, TurnID: runID + "-turn", Status: "pending", FencingToken: 5,
	}, extraTargets: []DecisionTarget{{
		Type: CommandUserInputResponse, ID: "input", BotID: testBotID,
		SessionID: testSessionID, RunID: runID, TurnID: runID + "-turn", Status: "pending", FencingToken: 5,
	}}}
	backend := newRetryingRecoveryBackend("generation-old", LeaseCandidate{})
	backend.startCalls = 1 // This fixture starts with an already parked, live owner.
	fence := &waitingDecisionRecoveryFence{runs: runs, decisions: decisions}
	manager := NewManager(backend, Options{OwnerID: "owner-old", OwnerLeaseTTL: time.Hour, Ledger: runs, Fence: fence})
	manager.SetDecisionStore(decisions)
	run, err := runs.Get(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.reserveRecoveredWaitingDecision(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	ctrl := manager.localControlForScope(testBotID, testSessionID, runID)
	t.Cleanup(func() { manager.forgetLocalControl(context.Background(), runID); _ = backend.Close() })
	return shutdownDecisionFixture{manager, backend, runs, decisions, fence, ctrl.handle()}
}

func TestGracefulShutdownPreservesDecisionsThroughCloseAndRecovery(t *testing.T) {
	f := newShutdownDecisionFixture(t)
	ctrl := f.manager.localControlForHandle(f.handle)
	for range 2 {
		if err := f.manager.InterruptForShutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.manager.ValidateRunOwnership(t.Context(), f.handle); !errors.Is(err, ErrRunOwnershipLost) {
		t.Fatalf("old owner can write: %v", err)
	}
	if !ctrl.ownershipWasLost() {
		t.Fatal("old producer not revoked")
	}
	// A delayed producer completion must not turn the handed-off run terminal.
	if _, err := f.manager.FinishRun(t.Context(), f.handle, RunStatusCompleted); !errors.Is(err, ErrRunOwnershipLost) {
		t.Fatalf("late finish: %v", err)
	}
	if err := f.manager.CloseContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	run, _ := f.runs.Get(t.Context(), f.handle.RunID)
	_, _, indexed, releases, _ := f.backend.recoveryState()
	if run.State != ledger.StateWaitingDecision || run.FencingToken != 6 || indexed.FencingToken != 5 || releases != 0 {
		t.Fatalf("handoff lost run or retry pointer: run=%+v index=%+v releases=%d", run, indexed, releases)
	}
	if got := f.fence.recordedReclaims(); len(got) != 1 {
		t.Fatalf("repeated shutdown reclaimed %d times", len(got))
	}
	// A fresh process discovers the old index, and a failed Redis reservation
	// after the database commit must remain retryable on the following tick.
	indexed.ExpiresAt = time.Now().Add(-time.Minute)
	backend := newRetryingRecoveryBackend("generation-new", indexed)
	next := NewManager(backend, Options{OwnerID: "owner-new", OwnerLeaseTTL: time.Hour, Ledger: f.runs, Fence: f.fence})
	next.SetDecisionStore(f.decisions)
	t.Cleanup(func() { next.forgetLocalControl(context.Background(), f.handle.RunID); _ = backend.Close() })
	reaper := newTestReaperWithLiveness(t, f.runs, backend, "generation-new")
	reaper.SetWaitingDecisionRecoverer(next.recoverWaitingDecision)
	reaper.tick(t.Context())
	reaper.tick(t.Context())
	run, _ = f.runs.Get(t.Context(), f.handle.RunID)
	_, ref, indexed, _, applied := backend.recoveryState()
	if run.State != ledger.StateWaitingDecision || run.FencingToken != 8 || ref.FencingToken != 8 || indexed.FencingToken != 8 || applied != 0 {
		t.Fatalf("recovery failed: run=%+v ref=%+v index=%+v", run, ref, indexed)
	}
	f.decisions.mu.Lock()
	defer f.decisions.mu.Unlock()
	for _, target := range append([]DecisionTarget{f.decisions.target}, f.decisions.extraTargets...) {
		if target.FencingToken != 8 || target.Status != "pending" {
			t.Fatalf("decision lost or answered during handoff: %+v", target)
		}
	}
}

type failingShutdownFence struct {
	DecisionFenceActivator
	err error
}

func (failingShutdownFence) Activate(context.Context, string, string, int64) error { return nil }
func (f failingShutdownFence) ReclaimWaitingDecision(context.Context, string, string, string, string, string, int64, int64, []runtimefence.PreservedDecision) error {
	return f.err
}

func TestGracefulShutdownHandoffFailureRetainsRetryPointer(t *testing.T) {
	f := newShutdownDecisionFixture(t)
	injected := errors.New("database unavailable")
	f.manager.fence = failingShutdownFence{err: injected}
	if err := f.manager.InterruptForShutdown(t.Context()); !errors.Is(err, injected) {
		t.Fatalf("shutdown error=%v", err)
	}
	run, _ := f.runs.Get(t.Context(), f.handle.RunID)
	_, _, indexed, _, _ := f.backend.recoveryState()
	if run.State != ledger.StateWaitingDecision || run.FencingToken != 5 || indexed.FencingToken != 5 {
		t.Fatalf("failed transaction lost waiting state: %+v %+v", run, indexed)
	}
	f.manager.fence = f.fence
	if err := f.manager.InterruptForShutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestGracefulShutdownWaitsForAcceptedDecisionContinuation(t *testing.T) {
	f := newShutdownDecisionFixture(t)
	ctrl := f.manager.localControlForHandle(f.handle)
	// Model the context registered before the answer commits. While the
	// continuation waits for its producer, the row is still waiting_decision.
	ctrl.decisionContinuations.Add(1)
	f.runs.Mu.Lock()
	f.runs.Runs[f.handle.RunID].Input = []byte(`{"resume":{"version":1}}`)
	f.runs.Mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- f.manager.InterruptForShutdown(t.Context()) }()
	select {
	case err := <-done:
		t.Fatalf("shutdown overtook accepted answer: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if _, _, err := f.runs.Resume(t.Context(), f.handle.RunID, f.handle.FencingToken); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not classify resumed continuation")
	}
	ctrl.decisionContinuations.Add(-1)
	run, _ := f.runs.Get(t.Context(), f.handle.RunID)
	if run.State != ledger.StateLost || run.ErrorCode != RunErrorInterrupted {
		t.Fatalf("accepted continuation not resumable: %+v", run)
	}
	if len(f.fence.recordedReclaims()) != 0 {
		t.Fatal("accepted answer was handed off as a pending decision")
	}
}

func TestGracefulShutdownClosesDecisionAdmission(t *testing.T) {
	f := newShutdownDecisionFixture(t)
	f.manager.closeAdmission()
	_, cancel, _, err := f.manager.DecisionContinuationContext(Command{BotID: f.handle.BotID, SessionID: f.handle.SessionID, RunID: f.handle.RunID, Generation: f.handle.Generation})
	cancel()
	if !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("answer admitted after shutdown began: %v", err)
	}
}

func TestGracefulShutdownWaitsForProducerPersistence(t *testing.T) {
	f := newShutdownDecisionFixture(t)
	ctrl := f.manager.localControlForHandle(f.handle)
	ctrl.decisionMu.Lock()
	ctrl.decisionReady = make(chan struct{})
	ctrl.decisionReadyOnce = sync.Once{}
	ctrl.decisionMu.Unlock()
	done := make(chan error, 1)
	go func() { done <- f.manager.InterruptForShutdown(t.Context()) }()
	select {
	case err := <-done:
		t.Fatalf("shutdown overtook producer persistence: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	ctrl.markDecisionReady()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown failed to hand off persisted decision")
	}
}

func (b *retryingRecoveryBackend) ReconcileTerminalRun(ctx context.Context, key Key, ref RunRef, update ActiveRunUpdate) (Snapshot, bool, error) {
	snapshot, changed, err := b.memory.Update(ctx, key, func(s Snapshot, ok bool) (Snapshot, bool, error) {
		if !ok {
			return s, false, nil
		}
		return update(s, time.Now())
	})
	if err == nil && changed {
		b.mu.Lock()
		if b.indexed.FencingToken == ref.FencingToken {
			b.indexed = LeaseCandidate{}
			b.ref = RunRef{}
		}
		b.mu.Unlock()
	}
	return snapshot, changed, err
}

func TestGracefulShutdownDoesNotPreserveInlineWaiters(t *testing.T) {
	for _, runtime := range []string{"codex", "claude-code", "acp_agent", "inline_tool"} {
		t.Run(runtime, func(t *testing.T) {
			f := newShutdownDecisionFixture(t)
			f.decisions.mu.Lock()
			f.decisions.target.SessionRuntime = runtime
			f.decisions.target.InlineDecision = runtime == "inline_tool"
			f.decisions.mu.Unlock()
			if err := f.manager.InterruptForShutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			run, _ := f.runs.Get(t.Context(), f.handle.RunID)
			if run.State != ledger.StateLost || len(f.fence.recordedReclaims()) != 0 {
				t.Fatalf("inline waiter advertised as recoverable: %+v", run)
			}
		})
	}
}

func TestGracefulShutdownDatabaseFailureRejectsLateProducerCompletion(t *testing.T) {
	f := newShutdownDecisionFixture(t)
	injected := errors.New("database unavailable")
	f.manager.fence = failingShutdownFence{err: injected}
	if err := f.manager.CloseContext(t.Context()); !errors.Is(err, injected) {
		t.Fatalf("close error=%v", err)
	}
	if _, err := f.manager.FinishRun(t.Context(), f.handle, RunStatusCompleted); !errors.Is(err, ErrRunOwnershipLost) {
		t.Fatalf("closed owner finalized waiting run: %v", err)
	}
	run, _ := f.runs.Get(t.Context(), f.handle.RunID)
	if run.State != ledger.StateWaitingDecision {
		t.Fatalf("late producer lost the retryable wait: %+v", run)
	}
}
