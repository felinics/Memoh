package sessionruntime

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
)

// fakeLiveness is a scriptable live backend. The reaper reads liveness and
// durable state together, so testing its decisions means controlling exactly
// what liveness reports.
type fakeLiveness struct {
	mu         sync.Mutex
	generation string
	candidates []LeaseCandidate
	released   []LeaseCandidate
	leader     bool
	leaderErr  error
	candErr    error
	leaseCalls int
}

func newFakeLiveness(generation string) *fakeLiveness {
	return &fakeLiveness{generation: generation, leader: true}
}

func (f *fakeLiveness) LivenessGeneration(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.generation, nil
}

func (f *fakeLiveness) ExpiredLeaseCandidates(_ context.Context, limit int64) ([]LeaseCandidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.candErr != nil {
		return nil, f.candErr
	}
	candidates := f.candidates
	if limit > 0 && int64(len(candidates)) > limit {
		candidates = candidates[:limit]
	}
	return append([]LeaseCandidate(nil), candidates...), nil
}

func (f *fakeLiveness) ReleaseLeaseCandidate(_ context.Context, candidate LeaseCandidate) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, existing := range f.candidates {
		if existing.RunID != candidate.RunID || existing.FencingToken != candidate.FencingToken {
			continue
		}
		f.candidates = append(f.candidates[:i:i], f.candidates[i+1:]...)
		f.released = append(f.released, candidate)
		return true, nil
	}
	return false, nil
}

func (f *fakeLiveness) AcquireLeaderLease(context.Context, string, time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.leaseCalls++
	return f.leader, f.leaderErr
}

func (*fakeLiveness) ReleaseLeaderLease(context.Context, string) error { return nil }

func (f *fakeLiveness) setCandidates(candidates ...LeaseCandidate) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.candidates = candidates
}

func (f *fakeLiveness) indexed() []LeaseCandidate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]LeaseCandidate(nil), f.candidates...)
}

func (f *fakeLiveness) releasedCandidates() []LeaseCandidate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]LeaseCandidate(nil), f.released...)
}

func newTestReaper(t *testing.T, runs *fakeLedger, live *fakeLiveness) *Reaper {
	t.Helper()
	return newTestReaperWithLiveness(t, runs, live, live.generation)
}

func newTestReaperWithLiveness(t *testing.T, runs *fakeLedger, live LivenessBackend, generation string) *Reaper {
	t.Helper()
	reaper := NewReaper(runs, live, newTuning(time.Second, 0, 2, 3), "reaper-test",
		slog.New(slog.DiscardHandler))
	reaper.generation = generation
	// Duties are driven by calling tick directly, so the grace has to be
	// declared expired rather than waited out.
	reaper.generationObservedAt = time.Now().Add(-time.Hour)
	return reaper
}

// The ordinary case: an owner stopped renewing, so the durable outcome is lost
// under the token the index carried, and only then is the entry released.
func TestReaperMarksExpiredLeaseLost(t *testing.T) {
	t.Parallel()
	runs := newFakeLedger()
	runs.insertClaimed("run-expired", "session-expired", 5, "generation-1")
	live := newFakeLiveness("generation-1")
	live.setCandidates(LeaseCandidate{
		Key:          Key{BotID: testBotID, SessionID: "session-expired"},
		RunID:        "run-expired",
		FencingToken: 5,
		ExpiresAt:    time.Now().Add(-time.Minute),
	})
	reaper := newTestReaper(t, runs, live)

	reaper.tick(context.Background())

	if got := runs.state("run-expired"); got != "lost" {
		t.Fatalf("state = %q, want lost", got)
	}
	if got := runs.errorCode("run-expired"); got != runErrorOwnerLeaseExpired {
		t.Fatalf("error code = %q, want %q", got, runErrorOwnerLeaseExpired)
	}
	if len(live.releasedCandidates()) != 1 || len(live.indexed()) != 0 {
		t.Fatalf("candidate should be released after the durable write: released=%d indexed=%d",
			len(live.releasedCandidates()), len(live.indexed()))
	}
}

func TestReaperObservesAppliedAndAlreadyTerminalOutcomes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		seedState      ledger.State
		abortRequested bool
		wantState      ledger.State
	}{
		{name: "applied lost", wantState: ledger.StateLost},
		{name: "applied abort intent", abortRequested: true, wantState: ledger.StateAborted},
		{name: "already completed", seedState: ledger.StateCompleted, wantState: ledger.StateCompleted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runs := newFakeLedger()
			runs.insertClaimed("run-observed", "session-observed", 5, "generation-1")
			if tt.abortRequested {
				runs.mu.Lock()
				runs.runs["run-observed"].AbortRequestedAt = time.Now()
				runs.mu.Unlock()
			}
			if tt.seedState != "" {
				if _, applied, err := runs.Finalize(context.Background(), ledger.FinalizeParams{
					RunID: "run-observed", FencingToken: 5, State: tt.seedState,
				}); err != nil || !applied {
					t.Fatalf("seed terminal = applied:%v err:%v", applied, err)
				}
			}
			live := newFakeLiveness("generation-1")
			live.setCandidates(LeaseCandidate{
				Key: Key{BotID: testBotID, SessionID: "session-observed"}, RunID: "run-observed", FencingToken: 5,
			})
			reaper := newTestReaper(t, runs, live)
			var observed []TerminalRun
			reaper.SetTerminalObserver(func(_ context.Context, run TerminalRun) {
				observed = append(observed, run)
			})

			reaper.tick(context.Background())

			if len(observed) != 1 {
				t.Fatalf("terminal observations = %d, want 1", len(observed))
			}
			if observed[0].RunID != "run-observed" || observed[0].BotID != testBotID ||
				observed[0].SessionID != "session-observed" || observed[0].FencingToken != 5 ||
				observed[0].State != string(tt.wantState) {
				t.Fatalf("terminal observation = %+v, want state %q and authoritative identity", observed[0], tt.wantState)
			}
		})
	}
}

func TestReaperDoesNotObserveNewerActiveOwner(t *testing.T) {
	t.Parallel()
	runs := newFakeLedger()
	runs.insertClaimed("run-newer-owner", "session-newer-owner", 6, "generation-1")
	live := newFakeLiveness("generation-1")
	live.setCandidates(LeaseCandidate{
		Key: Key{BotID: testBotID, SessionID: "session-newer-owner"}, RunID: "run-newer-owner", FencingToken: 5,
	})
	reaper := newTestReaper(t, runs, live)
	var observed []TerminalRun
	reaper.SetTerminalObserver(func(_ context.Context, run TerminalRun) {
		observed = append(observed, run)
	})

	reaper.tick(context.Background())

	if len(observed) != 0 {
		t.Fatalf("newer active owner emitted terminal observations: %+v", observed)
	}
	if got := runs.state("run-newer-owner"); got != ledger.StateRunning {
		t.Fatalf("ledger state = %q, want running", got)
	}
}

func TestReaperRetriesWaitingDecisionRecoveryAfterTokenHandoff(t *testing.T) {
	t.Parallel()
	runs := newFakeLedger()
	runs.insertClaimed("run-waiting-handoff", "session-waiting-handoff", 5, "generation-1")
	runs.mu.Lock()
	runs.runs["run-waiting-handoff"].State = ledger.StateWaitingDecision
	runs.runs["run-waiting-handoff"].FencingToken = 6
	runs.mu.Unlock()
	live := newFakeLiveness("generation-1")
	live.setCandidates(LeaseCandidate{
		Key:   Key{BotID: testBotID, SessionID: "session-waiting-handoff"},
		RunID: "run-waiting-handoff", FencingToken: 5,
	})
	reaper := newTestReaper(t, runs, live)
	var recovered []LeaseCandidate
	reaper.SetWaitingDecisionRecoverer(func(_ context.Context, candidate LeaseCandidate) (bool, error) {
		recovered = append(recovered, candidate)
		return true, nil
	})

	reaper.tick(context.Background())

	if len(recovered) != 1 || recovered[0].FencingToken != 5 {
		t.Fatalf("recovery calls = %+v, want the stale candidate retried once", recovered)
	}
	if got := runs.state("run-waiting-handoff"); got != ledger.StateWaitingDecision {
		t.Fatalf("ledger state = %q, want waiting_decision", got)
	}
	if len(live.indexed()) != 0 || len(live.releasedCandidates()) != 1 {
		t.Fatalf("stale candidate was not released after recovery: indexed=%+v released=%+v", live.indexed(), live.releasedCandidates())
	}
}

func TestReaperRunsTerminalReconcilerOnlyAsLeader(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name      string
		leader    bool
		wantCalls int
	}{
		{name: "leader", leader: true, wantCalls: 1},
		{name: "follower", leader: false, wantCalls: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runs := newFakeLedger()
			live := newFakeLiveness("generation-1")
			live.leader = tt.leader
			reaper := newTestReaper(t, runs, live)
			calls := 0
			reaper.SetTerminalReconciler(func(context.Context) error {
				calls++
				return errors.New("repair unavailable")
			})

			reaper.tick(context.Background())

			if calls != tt.wantCalls {
				t.Fatalf("reconciler calls = %d, want %d", calls, tt.wantCalls)
			}
		})
	}
}

// A durable write that fails must keep the index entry. Releasing it would leave
// the run active with no lease left to rediscover it.
func TestReaperKeepsCandidateWhenTerminalWriteFails(t *testing.T) {
	t.Parallel()
	runs := newFakeLedger()
	runs.insertClaimed("run-retry", "session-retry", 5, "generation-1")
	runs.setFinalizeErr(errors.New("database is unreachable"))
	live := newFakeLiveness("generation-1")
	live.setCandidates(LeaseCandidate{
		Key:          Key{BotID: testBotID, SessionID: "session-retry"},
		RunID:        "run-retry",
		FencingToken: 5,
	})
	reaper := newTestReaper(t, runs, live)

	reaper.tick(context.Background())
	if len(live.indexed()) != 1 {
		t.Fatal("failed transition must leave the candidate indexed for the next tick")
	}
	if got := runs.state("run-retry"); got != "running" {
		t.Fatalf("state = %q, want running", got)
	}

	runs.setFinalizeErr(nil)
	reaper.tick(context.Background())
	if got := runs.state("run-retry"); got != "lost" {
		t.Fatalf("state after retry = %q, want lost", got)
	}
	if len(live.indexed()) != 0 {
		t.Fatal("candidate should be released once the transition applies")
	}
}

// A candidate read before the run was reclaimed carries a token that no longer
// matches, so its transition applies to nothing and the live owner is untouched.
func TestReaperStaleTokenCannotCondemnReclaimedRun(t *testing.T) {
	t.Parallel()
	runs := newFakeLedger()
	runs.insertClaimed("run-reclaimed", "session-reclaimed", 9, "generation-1")
	live := newFakeLiveness("generation-1")
	live.setCandidates(LeaseCandidate{
		Key:          Key{BotID: testBotID, SessionID: "session-reclaimed"},
		RunID:        "run-reclaimed",
		FencingToken: 8,
	})
	reaper := newTestReaper(t, runs, live)

	reaper.tick(context.Background())

	if got := runs.state("run-reclaimed"); got != "running" {
		t.Fatalf("state = %q, want running; a stale token must not condemn a reclaimed run", got)
	}
}

// Fail-closed recovery: rows claimed by a backend incarnation that no longer
// exists have no live state to resume, so they end as lost.
func TestReaperRecoversRunsFromLostBackendGeneration(t *testing.T) {
	t.Parallel()
	runs := newFakeLedger()
	runs.insertClaimed("run-stale-a", "session-stale-a", 1, "generation-old")
	runs.insertClaimed("run-stale-b", "session-stale-b", 2, "generation-old")
	runs.insertClaimed("run-stale-c", "session-stale-c", 3, "generation-old")
	runs.insertClaimed("run-current", "session-current", 4, "generation-new")
	live := newFakeLiveness("generation-new")
	reaper := newTestReaper(t, runs, live)

	reaper.tick(context.Background())

	for _, runID := range []string{"run-stale-a", "run-stale-b", "run-stale-c"} {
		if got := runs.state(runID); got != "lost" {
			t.Fatalf("%s state = %q, want lost", runID, got)
		}
		if got := runs.errorCode(runID); got != runErrorBackendLost {
			t.Fatalf("%s error code = %q, want %q", runID, got, runErrorBackendLost)
		}
	}
	if got := runs.state("run-current"); got != "running" {
		t.Fatalf("current generation run = %q, want running", got)
	}
}

// The sweep must wait out the restart budget: an owner reconnecting through a
// brief blip is not a lost run.
func TestReaperDefersRecoveryUntilBackendLossGrace(t *testing.T) {
	t.Parallel()
	runs := newFakeLedger()
	runs.insertClaimed("run-blip", "session-blip", 1, "generation-old")
	live := newFakeLiveness("generation-new")
	reaper := newTestReaper(t, runs, live)
	reaper.generationObservedAt = time.Now()

	reaper.tick(context.Background())

	if got := runs.state("run-blip"); got != "running" {
		t.Fatalf("state = %q, want running during the grace period", got)
	}
}

// An admission that committed and was never claimed has no lease and no
// generation, so this is the only pass that can free its session.
func TestReaperRepairsOrphanedAdmissions(t *testing.T) {
	t.Parallel()
	runs := newFakeLedger()
	runs.insertOrphan("run-orphan", "session-orphan", "inv-orphan", "fingerprint")
	live := newFakeLiveness("generation-1")
	reaper := newTestReaper(t, runs, live)

	reaper.tick(context.Background())

	if got := runs.state("run-orphan"); got != "lost" {
		t.Fatalf("state = %q, want lost", got)
	}
	if got := runs.errorCode("run-orphan"); got != runErrorAdmissionOrphaned {
		t.Fatalf("error code = %q, want %q", got, runErrorAdmissionOrphaned)
	}
}

// Only the leader acts. Every instance runs a reaper, so a follower that swept
// would multiply PostgreSQL traffic by the size of the cluster.
func TestReaperFollowerDoesNothing(t *testing.T) {
	t.Parallel()
	runs := newFakeLedger()
	runs.insertClaimed("run-follower", "session-follower", 1, "generation-old")
	runs.insertOrphan("run-follower-orphan", "session-follower-orphan", "inv", "fingerprint")
	live := newFakeLiveness("generation-new")
	live.leader = false
	live.setCandidates(LeaseCandidate{RunID: "run-follower", FencingToken: 1})
	reaper := newTestReaper(t, runs, live)

	reaper.tick(context.Background())

	if got := runs.state("run-follower"); got != "running" {
		t.Fatalf("state = %q, want running", got)
	}
	if got := runs.state("run-follower-orphan"); got != "accepted" {
		t.Fatalf("orphan state = %q, want accepted", got)
	}
}

// Failover repeats work rather than coordinating: the second leader applying the
// same transitions must be a no-op, which is what makes a single leader safe.
func TestReaperTransitionsAreIdempotentAcrossLeaders(t *testing.T) {
	t.Parallel()
	runs := newFakeLedger()
	runs.insertClaimed("run-failover", "session-failover", 3, "generation-old")
	live := newFakeLiveness("generation-new")
	first := newTestReaper(t, runs, live)
	second := newTestReaper(t, runs, live)

	first.tick(context.Background())
	second.tick(context.Background())

	if got := runs.state("run-failover"); got != "lost" {
		t.Fatalf("state = %q, want lost", got)
	}
	writes := runs.terminalWrites()
	if len(writes) != 1 {
		t.Fatalf("terminal writes = %d, want 1; the repeat must not apply", len(writes))
	}
}
