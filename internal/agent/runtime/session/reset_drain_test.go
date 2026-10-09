package sessionruntime

import (
	"context"
	"encoding/json"
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
// fail, the way a Redis outage fails it: from now on, or once after failAfter
// more reads while failAfter is not negative.
type failingClockBackend struct {
	*MemoryBackend
	fail      atomic.Bool
	failAfter atomic.Int64
}

func newFailingClockBackend() *failingClockBackend {
	b := &failingClockBackend{MemoryBackend: NewMemoryBackend()}
	b.failAfter.Store(-1)
	return b
}

func (b *failingClockBackend) Now(ctx context.Context) (time.Time, error) {
	if b.fail.Load() || b.failAfter.Load() >= 0 && b.failAfter.Add(-1) < 0 {
		return time.Time{}, errors.New("runtime backend unreachable")
	}
	return b.MemoryBackend.Now(ctx)
}

// The run stays its owner's when the reset fails, and nothing of the failed
// reset is left to hold the next one up.
func TestBeginHistoryResetClockFailureLeavesTheRun(t *testing.T) {
	t.Parallel()
	backend := newFailingClockBackend()
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

// The lease can be taken over while the reset waits for a run to stop or
// clears the projections; the reset is then the busy conversation, whatever it
// was doing when it noticed. The lease is renewed every ten seconds at the
// shortest.
func TestBeginHistoryResetLeaseLostIsBusy(t *testing.T) {
	t.Parallel()
	orphan := ledger.Run{RunID: "run-1", BotID: "bot-1", SessionID: "session-1", State: ledger.StateRunning, FencingToken: 3}
	for name, setup := range map[string]func(*fakeResetLedger){
		"while draining": func(runs *fakeResetLedger) {
			for range 5000 {
				runs.activeRunsByBot = append(runs.activeRunsByBot, []ledger.Run{orphan})
			}
		},
		"while clearing projections": func(runs *fakeResetLedger) { runs.botSessionsWait = true },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runs := newFakeResetLedger()
			setup(runs)
			runs.renewResults = []fakeRenewResult{{ok: false}}
			manager, _ := newResetTestManager(t, runs)

			_, _, err := manager.BeginBotHistoryReset(context.Background(), "bot-1")
			if !errors.Is(err, ErrHistoryResetLeaseLost) {
				t.Fatalf("reset that lost its lease = %v, want ErrHistoryResetLeaseLost", err)
			}
			assertHistoryResetDrainFailure(t, err, true, runs)
		})
	}
}

// A command result crosses the runtime backend as its code; a failure of a
// dependency stays one on the way, whatever its code.
func TestCommandResultKeepsDependencyFault(t *testing.T) {
	t.Parallel()
	request := Command{Type: CommandHistoryReset, ID: "cmd-1"}
	for name, cause := range map[string]error{
		"timeout": errs.WrapDependency(context.DeadlineExceeded, "load runtime command result"),
		"failure": errs.WrapDependency(errors.New("redis: connection refused"), "load runtime command result"),
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(newCommandResult(request, cause))
			if err != nil {
				t.Fatal(err)
			}
			var result Command
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			got := commandResultErrorFor(request, result)
			if errs.FaultOf(got) != apperror.FaultDependency || IsHistoryResetBusy(got) {
				t.Fatalf("decoded %v (fault %q, busy %v), want a dependency failure", got, errs.FaultOf(got), IsHistoryResetBusy(got))
			}
		})
	}
	// Running out of time is no evidence that something else holds the
	// conversation.
	if got := commandResultErrorFor(request, newCommandResult(request, context.DeadlineExceeded)); IsHistoryResetBusy(got) {
		t.Fatalf("a command that ran out of time = %v, want no busy conversation", got)
	}
}

// The owner of a run is this process and the reset's command cannot reach the
// runtime backend: reading its own result, or the time it runs until.
func TestBeginHistoryResetLocalCommandBackendFailure(t *testing.T) {
	t.Parallel()
	for name, reads := range map[string]int64{"result": 1, "deadline": 2, "abort": 3} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			backend := newFailingClockBackend()
			runs := newFakeResetLedger()
			manager := NewManager(backend, Options{
				OwnerID: "owner-reset-command", StateTTL: time.Minute, OwnerLeaseTTL: time.Second,
				Ledger: runs, Fence: &fakeFence{},
			})
			t.Cleanup(func() { _ = manager.Close() })
			admitResetRaceRun(t, manager)

			backend.failAfter.Store(reads)
			_, _, err := manager.BeginSessionHistoryReset(context.Background(), testBotID, testSessionID)
			backend.failAfter.Store(-1)
			assertHistoryResetDrainFailure(t, err, false, runs)
		})
	}
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

// failingCommandResultBackend is the Redis backend unable to read command
// results.
type failingCommandResultBackend struct{ *RedisBackend }

func (failingCommandResultBackend) LoadCommandResult(context.Context, string) (Command, bool, error) {
	return Command{}, false, errors.New("redis: connection refused")
}

// A command whose result cannot be read was not left unanswered by a busy
// owner: the wait reports the runtime backend's failure.
func TestRedisCommandResultWaitReportsUnreadableResults(t *testing.T) {
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
	backend, err := NewRedisBackend(context.Background(), RedisOptions{URL: url, KeyPrefix: uniqueRuntimeBackendPrefix("reset-result"), StateTTL: time.Minute})
	if err != nil {
		t.Fatalf("redis backend: %v", err)
	}
	manager := NewManager(failingCommandResultBackend{backend}, Options{OwnerID: "owner-reset-result", StateTTL: time.Minute, OwnerLeaseTTL: time.Second})
	t.Cleanup(func() { _ = manager.Close() })

	err = manager.waitCommandResult(context.Background(), Command{Type: CommandHistoryReset, ID: "cmd-1"}, make(chan error), 200*time.Millisecond)
	if errs.FaultOf(err) != apperror.FaultDependency || IsHistoryResetBusy(err) {
		t.Fatalf("wait with unreadable results = %v (fault %q), want a dependency failure", err, errs.FaultOf(err))
	}
}

// hangingLiveReleaseBackend is the memory backend whose live reset marker
// cannot be released, the way an unresponsive Redis holds a release until its
// deadline, and whose writes fail.
type hangingLiveReleaseBackend struct{ *MemoryBackend }

func (hangingLiveReleaseBackend) ReleaseHistoryReset(ctx context.Context, _ ResetLease) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}

func (hangingLiveReleaseBackend) Update(context.Context, Key, SnapshotUpdate) (Snapshot, bool, error) {
	return Snapshot{}, false, errors.New("runtime backend write failed")
}

// A reset that fails gives its PostgreSQL lease back even when the live
// marker's release hangs: the durable release has a budget of its own. The
// live release waits out its budget, ten seconds at the shortest.
func TestBeginHistoryResetReleasesDurableLeaseWhenLiveReleaseHangs(t *testing.T) {
	t.Parallel()
	runs := newFakeResetLedger()
	manager := NewManager(hangingLiveReleaseBackend{NewMemoryBackend()}, Options{Ledger: runs, OwnerLeaseTTL: 40 * time.Millisecond})
	t.Cleanup(func() { _ = manager.Close() })

	_, _, err := manager.BeginSessionHistoryReset(context.Background(), "bot-1", "session-1")
	assertHistoryResetDrainFailure(t, err, false, runs)
	runs.resetMu.Lock()
	releaseErr := runs.releaseCtxErr
	runs.resetMu.Unlock()
	if releaseErr != nil {
		t.Fatalf("durable release ran with a spent context: %v", releaseErr)
	}
}
