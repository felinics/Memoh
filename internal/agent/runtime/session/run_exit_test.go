package sessionruntime

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
)

// Every active run state needs an exit that does not depend on the process
// that put the run there staying alive. The cases below are the windows where
// that exit used to be missing.

// A claim that committed without ever reaching the live backend is invisible to
// lease expiry (no index entry) and to the stale-generation sweep (its
// generation is current). Only rows the live backend does not hold may be
// condemned, and only once the claim-to-reservation latency has passed.
func TestReaperFinalizesClaimThatNeverReachedLiveBackend(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name       string
		state      ledger.State
		generation string
		ownerAge   time.Duration
		reserved   bool
		wantState  ledger.State
		wantCode   string
	}{
		{
			name: "unreserved claim", state: ledger.StateRunning, generation: "generation-1", ownerAge: time.Minute,
			wantState: ledger.StateLost, wantCode: runErrorAdmissionOrphaned,
		},
		{
			name: "live reservation", state: ledger.StateRunning, generation: "generation-1", ownerAge: time.Minute,
			reserved: true, wantState: ledger.StateRunning,
		},
		{
			name: "claim younger than the reservation grace", state: ledger.StateRunning, generation: "generation-1",
			ownerAge: time.Second, wantState: ledger.StateRunning,
		},
		{
			name: "graceful waiting-decision handoff", state: ledger.StateWaitingDecision, generation: "generation-1",
			ownerAge: time.Minute, wantState: ledger.StateWaitingDecision,
		},
		{
			name: "older generation is left to the backend-loss sweep", state: ledger.StateRunning, generation: "generation-0",
			ownerAge: time.Minute, wantState: ledger.StateLost, wantCode: runErrorBackendLost,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runs := newFakeLedger()
			runs.InsertClaimed("run-claim", "session-claim", 5, tt.generation)
			setOwnerSince(runs, "run-claim", time.Now().Add(-tt.ownerAge))
			if tt.state == ledger.StateWaitingDecision {
				if _, applied, err := runs.SetWaitingDecision(context.Background(), "run-claim", 5); err != nil || !applied {
					t.Fatalf("park run = (%v, %v)", applied, err)
				}
			}
			live := newFakeLiveness("generation-1")
			if tt.reserved {
				live.reserve("run-claim")
			}
			reaper := newTestReaper(t, runs, live)

			reaper.tick(context.Background())

			if got := runs.State("run-claim"); got != tt.wantState {
				t.Fatalf("state = %q, want %q", got, tt.wantState)
			}
			if got := runs.ErrorCode("run-claim"); got != tt.wantCode {
				t.Fatalf("error code = %q, want %q", got, tt.wantCode)
			}
		})
	}
}

// The pass pages through healthy long runs without losing its place, so a
// stranded claim behind a full batch of live ones is still reached.
func TestReaperUnreservedClaimPassPagesPastLiveRuns(t *testing.T) {
	t.Parallel()
	runs := newFakeLedger()
	live := newFakeLiveness("generation-1")
	for _, runID := range []string{"run-a", "run-b", "run-c", "run-d", "run-e", "run-f", "run-g"} {
		runs.InsertClaimed(runID, "session-"+runID, 5, "generation-1")
		live.reserve(runID)
	}
	runs.InsertClaimed("run-z", "session-run-z", 5, "generation-1")
	// Two runs per batch and three batches per tick: the stranded run sits in
	// the fourth batch, so only a cursor carried across ticks reaches it.
	reaper := newTestReaper(t, runs, live)

	reaper.tick(context.Background())
	if got := runs.State("run-z"); got != ledger.StateRunning {
		t.Fatalf("first tick reached run-z early: state %q", got)
	}
	reaper.tick(context.Background())
	if got := runs.State("run-z"); got != ledger.StateLost {
		t.Fatalf("state after second tick = %q, want lost", got)
	}
}

// abandonClaim's own terminal write can fail. The run is then running under
// this process's token with no live reservation, which is exactly the claim
// the reaper must finish; its log line promises so.
func TestAbandonedClaimWhoseReleaseFailedIsFinishedByReaper(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)
	f.fence.setErr(errors.New("fence is stale"))
	f.runs.SetFinalizeErr(errors.New("database unavailable"))

	if _, err := f.manager.Admit(context.Background(), f.input("inv-abandon", `{"text":"a"}`)); err == nil {
		t.Fatal("admit should fail when the fence cannot be activated")
	}
	run, err := f.runs.ActiveRun(context.Background(), testSessionID)
	if err != nil || run.State != ledger.StateRunning || run.OwnerID == "" {
		t.Fatalf("stranded claim = %+v, %v; want running under an owner", run, err)
	}
	f.runs.SetFinalizeErr(nil)
	setOwnerSince(f.runs, run.RunID, time.Now().Add(-time.Minute))
	backend := f.manager.backend.(*MemoryBackend)
	reaper := NewReaper(f.runs, backend, newTuning(time.Second, 0, 2, 3), "reaper-abandon", slog.New(slog.DiscardHandler))
	reaper.generation = run.LiveGeneration
	reaper.generationObservedAt = time.Now()

	reaper.tick(context.Background())

	if got := f.runs.State(run.RunID); got != ledger.StateLost {
		t.Fatalf("state = %q, want lost", got)
	}
	if _, err := f.manager.Admit(context.Background(), f.input("inv-after-abandon", `{"text":"b"}`)); err == nil {
		// The fence still fails; reaching the claim at all proves the session
		// is no longer busy.
		t.Fatal("admit unexpectedly succeeded with a failing fence")
	} else if errors.Is(err, ErrSessionBusy) {
		t.Fatalf("session still busy after the reaper finished the claim: %v", err)
	}
}

// An unnamed finish first reads the projection to learn whether the run
// parked. A failed read must enter the same bounded durable retry as any other
// finish failure; returning without one left the lease renewed and the session
// busy for the life of the process.
func TestUnnamedFinishRetriesAfterProjectionReadFailure(t *testing.T) {
	t.Parallel()
	backend := &failingLoadMemoryBackend{MemoryBackend: NewMemoryBackend()}
	runs := newFakeLedger()
	manager := NewManager(backend, Options{
		OwnerID: "owner-unnamed-load", StateTTL: time.Minute, OwnerLeaseTTL: time.Second,
		Ledger: runs, Fence: &fakeFence{},
	})
	t.Cleanup(func() { _ = manager.Close() })
	admission, err := manager.Admit(context.Background(), AdmitInput{
		BotID: testBotID, SessionID: testSessionID, InvocationID: "inv-unnamed-load",
		Payload: []byte(`{"text":"hi"}`),
		Execution: Execution{Admission: func(context.Context, RunHandle) (RunAdmissionView, error) {
			return RunAdmissionView{}, nil
		}},
	})
	if err != nil {
		t.Fatalf("admit run: %v", err)
	}

	backend.failLoads.Store(1)
	if _, err := manager.FinishRun(context.Background(), admission.Handle, ""); err == nil {
		t.Fatal("FinishRun() error = nil, want the injected projection read failure")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runs.State(admission.RunID) == ledger.StateCompleted && manager.localControlForHandle(admission.Handle) == nil {
			snapshot, err := manager.Snapshot(context.Background(), testBotID, testSessionID)
			if err == nil && snapshot.CurrentRunView != nil && snapshot.CurrentRunView.Status == RunStatusCompleted {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("unnamed finish was not retried: ledger state %q", runs.State(admission.RunID))
}

// Once the durable outcome is committed, a live backend that keeps refusing the
// release must not keep this owner holding the run. After the budget the owner
// lets go and the reaper repairs the projection from the durable outcome.
func TestMemoryRuntimeHandsExhaustedLiveFinishToReaper(t *testing.T) {
	t.Parallel()
	backend := &failingReleaseMemoryBackend{MemoryBackend: NewMemoryBackend()}
	runs := newFakeLedger()
	manager := NewManager(backend, Options{
		OwnerID: "owner-live-budget", StateTTL: time.Minute, OwnerLeaseTTL: 60 * time.Millisecond,
		liveFinishRetryBudget: 30 * time.Millisecond,
		Ledger:                runs, Fence: &fakeFence{},
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	admission, err := manager.Admit(context.Background(), AdmitInput{
		BotID: testBotID, SessionID: testSessionID, InvocationID: "inv-live-budget",
		Payload: []byte(`{"text":"hi"}`),
		Execution: Execution{Admission: func(context.Context, RunHandle) (RunAdmissionView, error) {
			return RunAdmissionView{}, nil
		}},
	})
	if err != nil {
		t.Fatalf("admit run: %v", err)
	}

	backend.failing.Store(true)
	if _, err := manager.FinishRun(context.Background(), admission.Handle, RunStatusCompleted); err == nil {
		t.Fatal("FinishRun() error = nil, want the injected live release failure")
	}
	if got := runs.State(admission.RunID); got != ledger.StateCompleted {
		t.Fatalf("ledger state = %q, want completed before the live release", got)
	}
	controlDeadline := time.Now().Add(2 * time.Second)
	for manager.localControlForHandle(admission.Handle) != nil && time.Now().Before(controlDeadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if manager.localControlForHandle(admission.Handle) != nil {
		t.Fatal("owner kept retrying the live release past its budget")
	}

	backend.failing.Store(false)
	waitRuntimeSnapshot(t, manager, testBotID, testSessionID, func(snapshot Snapshot) bool {
		return snapshot.CurrentRunView != nil && snapshot.CurrentRunView.Status == RunStatusCompleted
	})
}

// Decision cleanup after a reaper transition is retried until it succeeds,
// whichever duty made the run terminal. The terminal outcome itself is never
// rolled back.
func TestReaperRetriesLostRunDecisionCleanup(t *testing.T) {
	t.Parallel()
	for _, duty := range []string{"expired lease", "backend loss"} {
		t.Run(duty, func(t *testing.T) {
			t.Parallel()
			runs := newFakeLedger()
			live := newFakeLiveness("generation-1")
			generation := "generation-1"
			if duty == "backend loss" {
				generation = "generation-0"
			}
			runs.InsertClaimed("run-cleanup", "session-cleanup", 5, generation)
			if duty == "expired lease" {
				live.setCandidates(LeaseCandidate{
					Key: Key{BotID: testBotID, SessionID: "session-cleanup"}, RunID: "run-cleanup",
					FencingToken: 5, ExpiresAt: time.Now().Add(-time.Minute),
				})
			}
			reaper := newTestReaper(t, runs, live)
			var calls atomic.Int32
			var failing atomic.Bool
			failing.Store(true)
			reaper.SetLostRunDecisionCanceller(func(context.Context, string, string, string, int64, string) error {
				calls.Add(1)
				if failing.Load() {
					return errors.New("decision store unavailable")
				}
				return nil
			})

			reaper.tick(context.Background())
			if got := runs.State("run-cleanup"); got != ledger.StateLost {
				t.Fatalf("state = %q, want lost despite cleanup failure", got)
			}
			if calls.Load() == 0 {
				t.Fatal("cleanup was never attempted")
			}

			failing.Store(false)
			reaper.tick(context.Background())
			succeeded := calls.Load()
			reaper.tick(context.Background())
			if calls.Load() != succeeded {
				t.Fatalf("cleanup kept running after it succeeded: %d calls, then %d", succeeded, calls.Load())
			}
			if succeeded < 2 {
				t.Fatalf("cleanup attempts = %d, want a retry after the failure", succeeded)
			}
			if len(live.indexed()) != 0 {
				t.Fatalf("recovery pointer kept after cleanup succeeded: %+v", live.indexed())
			}
		})
	}
}

// A decision request that reaches the manager after an abort must not park the
// run. Parking kept ownership and the lease for an answer the abort already
// refuses, so the run stayed active with nothing left to end it.
func TestAbortedRunIsNotParkedByLateDecisionRequest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newAdmitFixture(t)
	admission := driveLiveRunTo(ctx, t, f, RunStatusAborting)

	if _, err := f.manager.HandleAgentEvent(ctx, admission.Handle, pendingUserInputEvent("input-late")); err != nil {
		t.Fatalf("publish late decision request: %v", err)
	}
	if _, err := f.manager.FinishRun(ctx, admission.Handle, ""); err != nil {
		t.Fatalf("finish aborted run: %v", err)
	}

	if got := liveRunStatusOf(ctx, t, f); got != RunStatusAborted {
		t.Fatalf("live status = %q, want aborted", got)
	}
	if got := f.runs.State(admission.RunID); got != ledger.StateAborted {
		t.Fatalf("durable state = %q, want aborted", got)
	}
	if f.manager.localControlForHandle(admission.Handle) != nil {
		t.Fatal("aborted run kept its owner control")
	}
}

func setOwnerSince(runs *fakeLedger, runID string, since time.Time) {
	runs.Mu.Lock()
	defer runs.Mu.Unlock()
	runs.Runs[runID].OwnerSince = since
}

type failingLoadMemoryBackend struct {
	*MemoryBackend
	failLoads atomic.Int32
}

func (b *failingLoadMemoryBackend) Load(ctx context.Context, key Key) (Snapshot, bool, error) {
	if b.failLoads.Add(-1) >= 0 {
		return Snapshot{}, false, errors.New("injected projection read failure")
	}
	b.failLoads.Store(0)
	return b.MemoryBackend.Load(ctx, key)
}

// failingReleaseMemoryBackend fails every projection write while set. A memory
// backend releases a run through Update, so this is the live release.
type failingReleaseMemoryBackend struct {
	*MemoryBackend
	failing atomic.Bool
}

func (b *failingReleaseMemoryBackend) Update(ctx context.Context, key Key, update SnapshotUpdate) (Snapshot, bool, error) {
	if b.failing.Load() {
		return Snapshot{}, false, errors.New("injected live release failure")
	}
	return b.MemoryBackend.Update(ctx, key, update)
}
