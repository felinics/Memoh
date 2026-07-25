package client

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRuntimeLifecycleLocksIsolateRuntimeIDs(t *testing.T) {
	locks := newRuntimeLifecycleLocks()
	releaseA, err := locks.lock(context.Background(), "runtime-a")
	if err != nil {
		t.Fatalf("acquire Runtime A: %v", err)
	}

	waitCtx, cancelWait := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelWait()
	if _, err := locks.lock(waitCtx, "runtime-a"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second Runtime A acquire error = %v, want deadline", err)
	}
	otherCtx, cancelOther := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelOther()
	releaseB, err := locks.lock(otherCtx, "runtime-b")
	if err != nil {
		t.Fatalf("Runtime B was blocked by Runtime A: %v", err)
	}
	releaseB()

	locks.mu.Lock()
	entryA := locks.entries["runtime-a"]
	if len(locks.entries) != 1 || entryA == nil || entryA.refs != 1 {
		t.Fatalf("entries while Runtime A is retained = %#v", locks.entries)
	}
	locks.mu.Unlock()

	releaseA()
	locks.mu.Lock()
	defer locks.mu.Unlock()
	if len(locks.entries) != 0 {
		t.Fatalf("locks leaked released entries: %#v", locks.entries)
	}
}
