package sessionruntime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryManagerRunsTerminalReconcilerUntilClose(t *testing.T) {
	t.Parallel()
	manager := NewManager(NewMemoryBackend(), Options{
		Ledger:        newFakeLedger(),
		OwnerLeaseTTL: 30 * time.Millisecond,
	})
	var calls atomic.Int64
	called := make(chan struct{}, 4)
	manager.SetTerminalReconciler(func(context.Context) error {
		calls.Add(1)
		select {
		case called <- struct{}{}:
		default:
		}
		return nil
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		select {
		case <-called:
		case <-time.After(time.Second):
			t.Fatal("memory terminal reconciler did not run")
		}
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	closedCalls := calls.Load()
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != closedCalls {
		t.Fatalf("terminal reconciler calls after Close = %d, want %d", got, closedCalls)
	}
}
