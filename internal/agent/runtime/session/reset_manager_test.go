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

// fakeResetLedger layers the durable reset arbiter over the shared run-ledger
// fake so Manager.beginHistoryReset can be exercised without PostgreSQL.
type fakeResetLedger struct {
	*fakeLedger

	resetMu sync.Mutex
	// blockedAcquires makes the first N AcquireReset calls report an active
	// competing lease (applied=false) before granting the lease.
	blockedAcquires int
	acquireErr      error
	acquires        int
	renewResults    []fakeRenewResult
	renews          int
	released        []ledger.ResetLease
	releaseCtx      context.Context
	// activeRunsByBot is consumed one call at a time; when exhausted the bot
	// has no active runs left.
	activeRunsByBot [][]ledger.Run
	orphanLeases    []ledger.ResetLease
}

type fakeRenewResult struct {
	ok  bool
	err error
}

func newFakeResetLedger() *fakeResetLedger {
	return &fakeResetLedger{fakeLedger: newFakeLedger()}
}

func (f *fakeResetLedger) AcquireReset(_ context.Context, lease ledger.ResetLease, ttl time.Duration) (ledger.ResetLease, bool, error) {
	f.resetMu.Lock()
	defer f.resetMu.Unlock()
	f.acquires++
	if f.acquireErr != nil {
		return ledger.ResetLease{}, false, f.acquireErr
	}
	if f.acquires <= f.blockedAcquires {
		return ledger.ResetLease{}, false, nil
	}
	lease.ExpiresAt = time.Now().Add(ttl)
	return lease, true, nil
}

func (f *fakeResetLedger) RenewReset(_ context.Context, lease ledger.ResetLease, ttl time.Duration) (ledger.ResetLease, bool, error) {
	f.resetMu.Lock()
	defer f.resetMu.Unlock()
	result := fakeRenewResult{ok: true}
	if f.renews < len(f.renewResults) {
		result = f.renewResults[f.renews]
	}
	f.renews++
	if result.err != nil {
		return ledger.ResetLease{}, false, result.err
	}
	if !result.ok {
		return ledger.ResetLease{}, false, nil
	}
	lease.ExpiresAt = time.Now().Add(ttl)
	return lease, true, nil
}

func (f *fakeResetLedger) ReleaseReset(ctx context.Context, lease ledger.ResetLease) (bool, error) {
	f.resetMu.Lock()
	defer f.resetMu.Unlock()
	f.releaseCtx = ctx
	f.released = append(f.released, lease)
	return true, nil
}

func (*fakeResetLedger) EffectiveReset(context.Context, string, string) (ledger.ResetLease, bool, error) {
	return ledger.ResetLease{}, false, nil
}

func (f *fakeResetLedger) ActiveRunsByBot(context.Context, string) ([]ledger.Run, error) {
	f.resetMu.Lock()
	defer f.resetMu.Unlock()
	if len(f.activeRunsByBot) == 0 {
		return nil, nil
	}
	runs := f.activeRunsByBot[0]
	f.activeRunsByBot = f.activeRunsByBot[1:]
	return runs, nil
}

func (f *fakeResetLedger) FenceAndFinalizeOrphan(_ context.Context, reset ledger.ResetLease, run ledger.Run) (ledger.Run, bool, error) {
	f.resetMu.Lock()
	defer f.resetMu.Unlock()
	f.orphanLeases = append(f.orphanLeases, reset)
	return run, true, nil
}

func (f *fakeResetLedger) acquireCount() int {
	f.resetMu.Lock()
	defer f.resetMu.Unlock()
	return f.acquires
}

func (f *fakeResetLedger) releasedLeases() []ledger.ResetLease {
	f.resetMu.Lock()
	defer f.resetMu.Unlock()
	return append([]ledger.ResetLease(nil), f.released...)
}

var (
	_ ledger.ResetStore       = (*fakeResetLedger)(nil)
	_ ledger.OrphanResetStore = (*fakeResetLedger)(nil)
)

func newResetTestManager(t *testing.T, runs ledger.Store) (*Manager, *MemoryBackend) {
	t.Helper()
	backend := NewMemoryBackend()
	manager := NewManager(backend, Options{Ledger: runs, OwnerLeaseTTL: 40 * time.Millisecond})
	t.Cleanup(func() { _ = manager.Close() })
	return manager, backend
}

func TestBeginSessionHistoryResetAcquiresFencesAndReleases(t *testing.T) {
	t.Parallel()
	type contextKey struct{}
	runs := newFakeResetLedger()
	manager, backend := newResetTestManager(t, runs)

	ctx := context.WithValue(context.Background(), contextKey{}, "reset-scope")
	resetCtx, release, err := manager.BeginSessionHistoryReset(ctx, "bot-1", "session-1")
	if err != nil {
		t.Fatalf("BeginSessionHistoryReset() error = %v", err)
	}
	fence, ok := runtimefence.ResetFromContext(resetCtx)
	if !ok || fence.Scope != "session" || fence.BotID != "bot-1" || fence.SessionID != "session-1" || fence.Token == "" {
		t.Fatalf("reset fence = (%#v, %v)", fence, ok)
	}
	// The live memory backend mirrors the durable lease while it is held.
	if _, blocked, err := backend.EffectiveHistoryReset(context.Background(), ResetScope{BotID: "bot-1", SessionID: "session-1"}); err != nil || !blocked {
		t.Fatalf("live mirror while held = (%v, %v)", blocked, err)
	}

	release()
	if err := context.Cause(resetCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("reset context after release = %v", err)
	}
	released := runs.releasedLeases()
	if len(released) != 1 || released[0].Token != fence.Token || released[0].Scope != ledger.ResetScopeSession {
		t.Fatalf("durable release = %#v, want the acquired token", released)
	}
	runs.resetMu.Lock()
	releaseCtx := runs.releaseCtx
	runs.resetMu.Unlock()
	if got := releaseCtx.Value(contextKey{}); got != "reset-scope" {
		t.Fatalf("release context value = %v, want reset-scope", got)
	}
	if _, blocked, err := backend.EffectiveHistoryReset(context.Background(), ResetScope{BotID: "bot-1", SessionID: "session-1"}); err != nil || blocked {
		t.Fatalf("live mirror after release = (%v, %v)", blocked, err)
	}
	// A second release is a no-op rather than a double durable release.
	release()
	if got := runs.releasedLeases(); len(got) != 1 {
		t.Fatalf("release is not idempotent: %#v", got)
	}
}

func TestBeginHistoryResetRetriesWhileScopeIsBusy(t *testing.T) {
	t.Parallel()
	runs := newFakeResetLedger()
	runs.blockedAcquires = 2
	manager, _ := newResetTestManager(t, runs)

	_, release, err := manager.BeginBotHistoryReset(context.Background(), "bot-1")
	if err != nil {
		t.Fatalf("BeginBotHistoryReset() error = %v", err)
	}
	release()
	if got := runs.acquireCount(); got < 3 {
		t.Fatalf("acquire attempts = %d, want the blocked attempts retried", got)
	}
}

func TestBeginHistoryResetFailsFastWhenScopeIsGone(t *testing.T) {
	t.Parallel()
	runs := newFakeResetLedger()
	runs.acquireErr = ledger.ErrResetScopeNotFound
	manager, _ := newResetTestManager(t, runs)

	_, _, err := manager.BeginSessionHistoryReset(context.Background(), "bot-1", "session-1")
	if !errors.Is(err, ledger.ErrResetScopeNotFound) {
		t.Fatalf("BeginSessionHistoryReset() error = %v, want ErrResetScopeNotFound", err)
	}
	if got := runs.acquireCount(); got != 1 {
		t.Fatalf("acquire attempts = %d, want no retry on a deleted scope", got)
	}
}

func TestBeginBotHistoryResetFinalizesOrphanRuns(t *testing.T) {
	t.Parallel()
	runs := newFakeResetLedger()
	// One drain pass sees an ownerless durable run; the next sees it gone.
	runs.activeRunsByBot = [][]ledger.Run{{{
		RunID: "run-1", BotID: "bot-1", SessionID: "session-1",
		State: ledger.StateRunning, FencingToken: 7,
	}}}
	manager, _ := newResetTestManager(t, runs)

	resetCtx, release, err := manager.BeginBotHistoryReset(context.Background(), "bot-1")
	if err != nil {
		t.Fatalf("BeginBotHistoryReset() error = %v", err)
	}
	defer release()
	fence, _ := runtimefence.ResetFromContext(resetCtx)
	runs.resetMu.Lock()
	orphans := append([]ledger.ResetLease(nil), runs.orphanLeases...)
	runs.resetMu.Unlock()
	if len(orphans) != 1 || orphans[0].Token != fence.Token || orphans[0].Scope != ledger.ResetScopeBot {
		t.Fatalf("orphan finalization leases = %#v, want the owning reset lease", orphans)
	}
}

func TestRenewHistoryResetCancelsOnTokenLoss(t *testing.T) {
	t.Parallel()
	runs := newFakeResetLedger()
	// A transport error must be tolerated; the following CAS miss is loss.
	runs.renewResults = []fakeRenewResult{
		{err: errors.New("transient network failure")},
		{ok: false},
	}
	manager, _ := newResetTestManager(t, runs)

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	durable := ledger.ResetLease{
		Scope: ledger.ResetScopeSession, BotID: "bot-1", SessionID: "session-1",
		Token: "token-1", ExpiresAt: time.Now().Add(time.Second),
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go manager.renewHistoryReset(ctx, cancel, runs, 30*time.Millisecond, ResetLease{}, false, durable, stop, done)

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("renew loop did not observe the lost lease")
	}
	if err := context.Cause(ctx); !errors.Is(err, ErrHistoryResetLeaseLost) {
		t.Fatalf("cancellation cause = %v, want ErrHistoryResetLeaseLost", err)
	}
	close(stop)
	<-done
}

func TestRenewHistoryResetKeepsLeaseAliveAcrossTransportErrors(t *testing.T) {
	t.Parallel()
	runs := newFakeResetLedger()
	runs.renewResults = []fakeRenewResult{
		{err: errors.New("timeout")},
		{err: errors.New("timeout")},
		{ok: true},
	}
	manager, _ := newResetTestManager(t, runs)

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	durable := ledger.ResetLease{
		Scope: ledger.ResetScopeBot, BotID: "bot-1",
		Token: "token-1", ExpiresAt: time.Now().Add(time.Second),
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go manager.renewHistoryReset(ctx, cancel, runs, 30*time.Millisecond, ResetLease{}, false, durable, stop, done)

	deadline := time.After(2 * time.Second)
	for {
		runs.resetMu.Lock()
		renews := runs.renews
		runs.resetMu.Unlock()
		if renews >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("renew loop did not keep ticking through transport errors")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if ctx.Err() != nil {
		t.Fatalf("renew loop canceled on a transport error: %v", context.Cause(ctx))
	}
	close(stop)
	<-done
}
