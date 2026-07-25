package compaction

import (
	"context"
	"log/slog"
	"runtime"
	"testing"
	"time"
)

func TestRunCompactionKeepsAutomaticAttemptInsideCallerLifetime(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	store := &fakeQueries{listStarted: started, listRelease: release}
	svc := NewService(slog.New(slog.DiscardHandler), store, store)
	returned := make(chan error, 1)
	go func() {
		returned <- svc.RunCompaction(context.Background(), TriggerConfig{
			BotID:     "00000000-0000-0000-0000-00000000b715",
			SessionID: "00000000-0000-0000-0000-00000000e715",
		})
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("automatic compaction did not reach history selection")
	}
	select {
	case err := <-returned:
		t.Fatalf("RunCompaction returned before its query finished: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("RunCompaction() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunCompaction did not return after its query finished")
	}
}

func awaitWaiter(t *testing.T, run *inflightRun) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for run.waiters.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("waiter never attached to the in-flight owner")
		}
		runtime.Gosched()
	}
}

func TestRunCompactionSyncWaitsForOwnerAndReusesItsResult(t *testing.T) {
	t.Parallel()

	q := &fakeQueries{uncompacted: machineryCorpus(t)}
	stub := &stubModel{summary: "unused"}
	svc := newMachineryService(q)
	cfg := machineryConfig(stub, 450)

	run, ok := svc.beginSessionCompaction(cfg.SessionID)
	if !ok {
		t.Fatal("first acquisition must succeed")
	}

	got := make(chan Result, 1)
	go func() {
		res, err := svc.RunCompactionSync(context.Background(), cfg)
		if err != nil {
			t.Errorf("waiter: %v", err)
		}
		got <- res
	}()
	awaitWaiter(t, run)

	want := Result{Status: StatusOK, Summary: "owner summary", MessageCount: 3}
	svc.endSessionCompaction(cfg.SessionID, run, want, nil)

	if res := <-got; res != want {
		t.Fatalf("waiter must reuse the owner's result, got %#v", res)
	}
	if stub.calls != 0 || q.created || len(q.markedIDs) != 0 {
		t.Fatalf("waiter must not run its own compaction (calls=%d created=%v marked=%d)", stub.calls, q.created, len(q.markedIDs))
	}
}

func TestRunCompactionSyncCanceledWaiterDegradesToNoop(t *testing.T) {
	t.Parallel()

	q := &fakeQueries{uncompacted: machineryCorpus(t)}
	stub := &stubModel{summary: "healthy summary"}
	svc := newMachineryService(q)
	cfg := machineryConfig(stub, 450)

	run, ok := svc.beginSessionCompaction(cfg.SessionID)
	if !ok {
		t.Fatal("first acquisition must succeed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan Result, 1)
	go func() {
		res, err := svc.RunCompactionSync(ctx, cfg)
		if err != nil {
			t.Errorf("canceled waiter: %v", err)
		}
		got <- res
	}()
	awaitWaiter(t, run)
	cancel()
	if res := <-got; res.Status != StatusNoop {
		t.Fatalf("canceled waiter must degrade to noop, got %#v", res)
	}
	if stub.calls != 0 || len(q.markedIDs) != 0 {
		t.Fatalf("canceled waiter must not run compaction (calls=%d marked=%d)", stub.calls, len(q.markedIDs))
	}

	svc.endSessionCompaction(cfg.SessionID, run, Result{Status: StatusNoop}, nil)
	res, err := svc.RunCompactionSync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("run after owner completion: %v", err)
	}
	if res.Status != StatusOK {
		t.Fatalf("session must be usable after the owner completes, got %q", res.Status)
	}
}
