package sessionruntime

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/runtimefence"
)

// A reset that cannot stop a run because the ledger or the runtime backend
// failed reports that dependency's failure, never a busy conversation, and
// gives its lease back. A reset another one raced is the busy conversation.
func assertHistoryResetDrainFailure(t *testing.T, err error, busy bool, runs *fakeResetLedger) {
	t.Helper()
	if err == nil {
		t.Fatal("history reset began although its drain failed")
	}
	if got := IsHistoryResetBusy(err); got != busy {
		t.Fatalf("IsHistoryResetBusy(%v) = %v, want %v", err, got, busy)
	}
	if fault := errs.FaultOf(err); !busy && fault != apperror.FaultDependency {
		t.Fatalf("drain failure fault = %q (%v), want dependency", fault, err)
	}
	if released := runs.releasedLeases(); len(released) != 1 {
		t.Fatalf("durable release = %#v, want the lease returned", released)
	}
}

func TestBeginHistoryResetOrphanFinalizeFailure(t *testing.T) {
	t.Parallel()
	orphan := ledger.Run{RunID: "run-1", BotID: "bot-1", SessionID: "session-1", State: ledger.StateRunning, FencingToken: 3}
	for _, tc := range []struct {
		name string
		err  error
		busy bool
	}{
		{"ledger unreachable", errors.New("connection refused"), false},
		{"lease raced", runtimefence.ErrResetLeaseLost, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runs := newFakeResetLedger()
			runs.activeRunsByBot = [][]ledger.Run{{orphan}}
			runs.orphanErr = tc.err
			manager, _ := newResetTestManager(t, runs)
			_, _, err := manager.BeginBotHistoryReset(context.Background(), "bot-1")
			assertHistoryResetDrainFailure(t, err, tc.busy, runs)
		})
	}
}

// failingClockBackend is the memory backend with a clock that can be made to
// fail, the way a Redis outage fails it.
type failingClockBackend struct {
	*MemoryBackend
	fail atomic.Bool
}

func (b *failingClockBackend) Now(ctx context.Context) (time.Time, error) {
	if b.fail.Load() {
		return time.Time{}, errors.New("runtime backend unreachable")
	}
	return b.MemoryBackend.Now(ctx)
}

// The run stays its owner's when the reset fails, and nothing of the failed
// reset is left to hold the next one up.
func TestBeginHistoryResetClockFailureLeavesTheRun(t *testing.T) {
	t.Parallel()
	backend := &failingClockBackend{MemoryBackend: NewMemoryBackend()}
	runs := newFakeResetLedger()
	manager := NewManager(backend, Options{
		OwnerID: "owner-reset-drain", StateTTL: time.Minute, OwnerLeaseTTL: time.Second,
		Ledger: runs, Fence: &fakeFence{},
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	admission := admitResetRaceRun(t, manager)

	backend.fail.Store(true)
	_, _, err := manager.BeginSessionHistoryReset(ctx, testBotID, testSessionID)
	backend.fail.Store(false)
	assertHistoryResetDrainFailure(t, err, false, runs)
	snapshot, _, err := backend.Load(ctx, Key{BotID: testBotID, SessionID: testSessionID})
	if err != nil {
		t.Fatal(err)
	}
	if run := snapshot.CurrentRunView; run == nil || run.RunID != admission.RunID || !isActiveRunStatus(run.Status) {
		t.Fatalf("projection after the failed reset = %+v, want run %s still active", run, admission.RunID)
	}

	if _, err := manager.FinishRun(ctx, admission.Handle, RunStatusCompleted); err != nil {
		t.Fatalf("FinishRun() = %v", err)
	}
	_, release, err := manager.BeginSessionHistoryReset(ctx, testBotID, testSessionID)
	if err != nil {
		t.Fatalf("history reset after the run ended = %v", err)
	}
	release()
}

// The lease can be taken over while the drain waits for a run to stop; the
// reset is then the busy conversation, whatever the drain was doing when it
// noticed. The lease is renewed every ten seconds at the shortest.
func TestBeginHistoryResetLeaseLostDuringDrainIsBusy(t *testing.T) {
	t.Parallel()
	runs := newFakeResetLedger()
	orphan := ledger.Run{RunID: "run-1", BotID: "bot-1", SessionID: "session-1", State: ledger.StateRunning, FencingToken: 3}
	for range 5000 {
		runs.activeRunsByBot = append(runs.activeRunsByBot, []ledger.Run{orphan})
	}
	runs.renewResults = []fakeRenewResult{{ok: false}}
	manager, _ := newResetTestManager(t, runs)

	_, _, err := manager.BeginBotHistoryReset(context.Background(), "bot-1")
	if !errors.Is(err, ErrHistoryResetLeaseLost) {
		t.Fatalf("reset that lost its lease during the drain = %v, want ErrHistoryResetLeaseLost", err)
	}
	assertHistoryResetDrainFailure(t, err, true, runs)
}

// failingRunRefBackend is the Redis backend unable to read a run's route.
type failingRunRefBackend struct{ *RedisBackend }

func (failingRunRefBackend) LoadRunRef(context.Context, Key, string) (RunRef, bool, error) {
	return RunRef{}, false, errors.New("redis: connection refused")
}

func TestRedisBeginHistoryResetRunRouteFailure(t *testing.T) {
	url := os.Getenv("MEMOH_TEST_REDIS_URL")
	if url == "" {
		url = os.Getenv("MEMOH_TEST_VALKEY_URL")
	}
	if url == "" {
		if os.Getenv("MEMOH_TEST_DISTRIBUTED_REQUIRED") == "1" {
			t.Fatal("MEMOH_TEST_REDIS_URL or MEMOH_TEST_VALKEY_URL required")
		}
		t.Skip("set MEMOH_TEST_REDIS_URL or MEMOH_TEST_VALKEY_URL")
	}
	backend, err := NewRedisBackend(context.Background(), RedisOptions{URL: url, KeyPrefix: uniqueRuntimeBackendPrefix("reset-route"), StateTTL: time.Minute})
	if err != nil {
		t.Fatalf("redis backend: %v", err)
	}
	runs := newFakeResetLedger()
	runs.activeRunsByBot = [][]ledger.Run{{{
		RunID: "run-1", BotID: "bot-1", SessionID: "session-1", OwnerID: "owner-elsewhere",
		State: ledger.StateRunning, FencingToken: 3,
	}}}
	manager := NewManager(failingRunRefBackend{backend}, Options{
		OwnerID: "owner-reset-route", StateTTL: time.Minute, OwnerLeaseTTL: time.Second, Ledger: runs,
	})
	t.Cleanup(func() { _ = manager.Close() })

	_, _, err = manager.BeginBotHistoryReset(context.Background(), "bot-1")
	assertHistoryResetDrainFailure(t, err, false, runs)
}
