package sessionruntime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/memohai/memoh/internal/agent/runtime/session/ledger"
)

var errRecoveryReservation = errors.New("injected recovered-run reservation failure")

type retryingRecoveryBackend struct {
	DistributedBackend
	memory *MemoryBackend

	mu             sync.Mutex
	generation     string
	startCalls     int
	ref            RunRef
	indexed        LeaseCandidate
	releaseCalls   int
	releaseApplied int
}

func newRetryingRecoveryBackend(generation string, candidate LeaseCandidate) *retryingRecoveryBackend {
	return &retryingRecoveryBackend{
		memory:     NewMemoryBackend(),
		generation: generation,
		indexed:    candidate,
	}
}

func (b *retryingRecoveryBackend) Now(ctx context.Context) (time.Time, error) {
	return b.memory.Now(ctx)
}

func (b *retryingRecoveryBackend) Load(ctx context.Context, key Key) (Snapshot, bool, error) {
	return b.memory.Load(ctx, key)
}

func (b *retryingRecoveryBackend) Update(ctx context.Context, key Key, update SnapshotUpdate) (Snapshot, bool, error) {
	return b.memory.Update(ctx, key, update)
}

func (b *retryingRecoveryBackend) Publish(ctx context.Context, event Event) error {
	return b.memory.Publish(ctx, event)
}

func (b *retryingRecoveryBackend) Subscribe(ctx context.Context, key Key) (Subscription, error) {
	return b.memory.Subscribe(ctx, key)
}

func (b *retryingRecoveryBackend) Close() error {
	return b.memory.Close()
}

func (b *retryingRecoveryBackend) StartRun(ctx context.Context, key Key, ref RunRef, update SnapshotUpdate) (Snapshot, bool, error) {
	b.mu.Lock()
	b.startCalls++
	call := b.startCalls
	b.mu.Unlock()
	if call == 1 {
		return Snapshot{}, false, errRecoveryReservation
	}
	snapshot, changed, err := b.memory.Update(ctx, key, update)
	if err != nil || !changed {
		return snapshot, changed, err
	}
	b.mu.Lock()
	b.ref = ref
	expiresAt := time.Now().Add(time.Hour)
	if snapshot.CurrentRunView != nil && snapshot.CurrentRunView.OwnerLeaseExpiresAt != nil {
		expiresAt = *snapshot.CurrentRunView.OwnerLeaseExpiresAt
	}
	b.indexed = LeaseCandidate{
		Key: key, RunID: ref.RunID, FencingToken: ref.FencingToken, ExpiresAt: expiresAt,
	}
	b.mu.Unlock()
	return snapshot, true, nil
}

func (b *retryingRecoveryBackend) LoadRunRef(_ context.Context, key Key, runID string) (RunRef, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ref.RunID != runID || b.ref.BotID != key.BotID || b.ref.SessionID != key.SessionID {
		return RunRef{}, false, nil
	}
	return b.ref, true, nil
}

func (b *retryingRecoveryBackend) RenewLease(_ context.Context, key Key, runID, _ string, _ string, _ time.Time, expiresAt time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.indexed.Key != key || b.indexed.RunID != runID {
		return ErrRunOwnershipLost
	}
	b.indexed.ExpiresAt = expiresAt
	return nil
}

func (b *retryingRecoveryBackend) LivenessGeneration(context.Context) (string, error) {
	return b.generation, nil
}

func (b *retryingRecoveryBackend) ExpiredLeaseCandidates(_ context.Context, limit int64) ([]LeaseCandidate, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.indexed.RunID == "" || b.indexed.ExpiresAt.After(time.Now()) || limit == 0 {
		return nil, nil
	}
	return []LeaseCandidate{b.indexed}, nil
}

func (b *retryingRecoveryBackend) ReleaseLeaseCandidate(_ context.Context, candidate LeaseCandidate) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.releaseCalls++
	if b.indexed.RunID != candidate.RunID || b.indexed.FencingToken != candidate.FencingToken {
		return false, nil
	}
	b.indexed = LeaseCandidate{}
	b.releaseApplied++
	return true, nil
}

func (*retryingRecoveryBackend) AcquireLeaderLease(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}

func (*retryingRecoveryBackend) ReleaseLeaderLease(context.Context, string) error { return nil }

func (b *retryingRecoveryBackend) recoveryState() (int, RunRef, LeaseCandidate, int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.startCalls, b.ref, b.indexed, b.releaseCalls, b.releaseApplied
}

type waitingDecisionRecoveryFence struct {
	runs      *fakeLedger
	decisions *fakeDecisionStore

	mu       sync.Mutex
	reclaims [][2]int64
}

func (*waitingDecisionRecoveryFence) Activate(context.Context, string, string, int64) error {
	return nil
}

func (f *waitingDecisionRecoveryFence) ReclaimWaitingDecision(
	_ context.Context,
	botID, sessionID, runID, ownerID, liveGeneration string,
	previousToken, newToken int64,
	_ string, decisionID string,
) error {
	f.runs.mu.Lock()
	f.decisions.mu.Lock()
	defer f.decisions.mu.Unlock()
	defer f.runs.mu.Unlock()
	run := f.runs.runs[runID]
	if run == nil || run.BotID != botID || run.SessionID != sessionID ||
		run.State != ledger.StateWaitingDecision || run.FencingToken != previousToken {
		return ErrRunOwnershipLost
	}
	if f.decisions.target.ID != decisionID || f.decisions.target.RunID != runID ||
		f.decisions.target.FencingToken != previousToken {
		return ErrCommandTargetMismatch
	}
	run.OwnerID = ownerID
	run.FencingToken = newToken
	run.LiveGeneration = liveGeneration
	run.OwnerSince = time.Now().UTC()
	f.decisions.target.FencingToken = newToken
	f.mu.Lock()
	f.reclaims = append(f.reclaims, [2]int64{previousToken, newToken})
	f.mu.Unlock()
	return nil
}

func (f *waitingDecisionRecoveryFence) recordedReclaims() [][2]int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]int64(nil), f.reclaims...)
}

func TestWaitingDecisionRecoveryRetriesAfterFenceCommitWhenLiveReservationFails(t *testing.T) {
	type contextKey struct{}
	const (
		runID      = "run-waiting-recovery"
		sessionID  = "session-waiting-recovery"
		decisionID = "decision-waiting-recovery"
	)
	key := Key{BotID: testBotID, SessionID: sessionID}
	runs := newFakeLedger()
	runs.insertClaimed(runID, sessionID, 5, "generation-old")
	if _, applied, err := runs.SetWaitingDecision(context.Background(), runID, 5); err != nil || !applied {
		t.Fatalf("park run: applied=%v err=%v", applied, err)
	}
	runs.mu.Lock()
	runs.token = 5
	runs.mu.Unlock()
	decisions := &fakeDecisionStore{target: DecisionTarget{
		Type: CommandUserInputResponse, ID: decisionID,
		BotID: testBotID, SessionID: sessionID, RunID: runID, TurnID: runID + "-turn",
		Status: "pending", FencingToken: 5,
	}}
	candidate := LeaseCandidate{
		Key: key, RunID: runID, FencingToken: 5, ExpiresAt: time.Now().Add(-time.Minute),
	}
	backend := newRetryingRecoveryBackend("generation-new", candidate)
	fence := &waitingDecisionRecoveryFence{runs: runs, decisions: decisions}
	manager := NewManager(backend, Options{
		OwnerID: "owner-recovery", OwnerLeaseTTL: time.Hour, Ledger: runs, Fence: fence,
	})
	t.Cleanup(func() {
		manager.forgetLocalControl(context.Background(), runID)
		_ = backend.Close()
	})
	manager.SetDecisionStore(decisions)
	reaper := newTestReaperWithLiveness(t, runs, backend, "generation-new")
	reaper.SetWaitingDecisionRecoverer(manager.recoverWaitingDecision)
	reaperCtx := context.WithValue(context.Background(), contextKey{}, "reaper-scope")

	reaper.tick(reaperCtx)

	firstRun, err := runs.Get(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	decisions.mu.Lock()
	firstDecisionToken := decisions.target.FencingToken
	decisions.mu.Unlock()
	startCalls, ref, indexed, releaseCalls, releaseApplied := backend.recoveryState()
	if firstRun.State != ledger.StateWaitingDecision || firstRun.FencingToken != 6 || firstDecisionToken != 6 {
		t.Fatalf("durable handoff = run:%+v decision_token:%d, want waiting token 6", firstRun, firstDecisionToken)
	}
	if startCalls != 1 || ref.RunID != "" || manager.localControlForScope(testBotID, sessionID, runID) != nil {
		t.Fatalf("failed live reservation leaked ownership: starts=%d ref=%+v control=%v", startCalls, ref, manager.localControlForScope(testBotID, sessionID, runID))
	}
	if indexed.FencingToken != 5 || releaseCalls != 0 || releaseApplied != 0 {
		t.Fatalf("retry pointer changed after reservation failure: indexed=%+v release_calls=%d applied=%d", indexed, releaseCalls, releaseApplied)
	}

	reaper.tick(reaperCtx)

	secondRun, err := runs.Get(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	decisions.mu.Lock()
	secondDecisionToken := decisions.target.FencingToken
	decisions.mu.Unlock()
	startCalls, ref, indexed, releaseCalls, releaseApplied = backend.recoveryState()
	if secondRun.State != ledger.StateWaitingDecision || secondRun.FencingToken != 7 || secondDecisionToken != 7 {
		t.Fatalf("retried handoff = run:%+v decision_token:%d, want waiting token 7", secondRun, secondDecisionToken)
	}
	if startCalls != 2 || ref.FencingToken != 7 || manager.localControlForScope(testBotID, sessionID, runID) == nil {
		t.Fatalf("retried live reservation = starts:%d ref:%+v", startCalls, ref)
	}
	ctrl := manager.localControlForScope(testBotID, sessionID, runID)
	if got := ctrl.lifecycleCtx.Value(contextKey{}); got != "reaper-scope" {
		t.Fatalf("recovered run context value = %v, want reaper-scope", got)
	}
	if indexed.FencingToken != 7 || releaseCalls != 1 || releaseApplied != 0 {
		t.Fatalf("stale release changed successor lease: indexed=%+v release_calls=%d applied=%d", indexed, releaseCalls, releaseApplied)
	}
	if got := fence.recordedReclaims(); len(got) != 2 || got[0] != [2]int64{5, 6} || got[1] != [2]int64{6, 7} {
		t.Fatalf("reclaims = %+v, want [[5 6] [6 7]]", got)
	}
}
