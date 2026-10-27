package compaction

import (
	"context"
	"testing"
)

func TestCompactRunsNothingWhileSessionIsBusy(t *testing.T) {
	t.Parallel()

	q := &fakeQueries{uncompacted: machineryCorpus(t)}
	stub := &stubModel{summary: "healthy summary"}
	svc := newMachineryService(q)
	cfg := machineryConfig(stub, 450)

	if _, ok := svc.beginSessionCompaction(cfg.SessionID); !ok {
		t.Fatal("first acquisition must succeed")
	}
	passes, err := svc.Compact(context.Background(), cfg)
	if err != nil || len(passes) != 0 || stub.calls != 0 || len(q.markedIDs) != 0 {
		t.Fatalf("busy session must noop without running: passes=%d err=%v calls=%d marked=%d", len(passes), err, stub.calls, len(q.markedIDs))
	}

	svc.endSessionCompaction(cfg.SessionID)
	if passes, err = svc.Compact(context.Background(), cfg); err != nil || len(passes) == 0 {
		t.Fatalf("session must be usable after the owner completes: passes=%d err=%v", len(passes), err)
	}
}
