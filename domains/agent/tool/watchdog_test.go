package tool

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- Watchdog unit tests ---

func TestWatchdogFiresAfterInactivity(t *testing.T) {
	t.Parallel()

	timeout := 200 * time.Millisecond
	parentCtx := context.Background()
	ctx, wd := NewSubagentWatchdog(parentCtx, timeout, slog.Default())
	defer wd.Stop()

	// Do not touch — watchdog should fire after timeout.
	select {
	case <-ctx.Done():
		if !errors.Is(context.Cause(ctx), ErrWatchdogTimedOut) {
			t.Fatalf("expected ErrWatchdogTimedOut, got: %v", context.Cause(ctx))
		}
	case <-time.After(timeout + 200*time.Millisecond):
		t.Fatal("watchdog did not fire within expected time")
	}
}

func TestWatchdogDoesNotFireWhenTouched(t *testing.T) {
	t.Parallel()

	timeout := 150 * time.Millisecond
	parentCtx := context.Background()
	ctx, wd := NewSubagentWatchdog(parentCtx, timeout, slog.Default())
	defer wd.Stop()

	// Touch repeatedly to keep the watchdog alive past the timeout.
	deadline := time.After(timeout + 300*time.Millisecond)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

loop:
	for {
		select {
		case <-ticker.C:
			wd.Touch()
		case <-deadline:
			break loop
		}
	}

	// Context should NOT be cancelled — watchdog never fired.
	if ctx.Err() != nil {
		t.Fatalf("watchdog should not have fired, but context is done: %v", context.Cause(ctx))
	}
}

func TestWatchdogFiresAfterTouchesStop(t *testing.T) {
	t.Parallel()

	timeout := 200 * time.Millisecond
	parentCtx := context.Background()
	ctx, wd := NewSubagentWatchdog(parentCtx, timeout, slog.Default())
	defer wd.Stop()

	// Touch a few times, then stop touching.
	for i := 0; i < 3; i++ {
		wd.Touch()
		time.Sleep(50 * time.Millisecond)
	}

	// Now wait for the watchdog to fire after we stop touching.
	select {
	case <-ctx.Done():
		if !errors.Is(context.Cause(ctx), ErrWatchdogTimedOut) {
			t.Fatalf("expected ErrWatchdogTimedOut, got: %v", context.Cause(ctx))
		}
	case <-time.After(timeout + 500*time.Millisecond):
		t.Fatal("watchdog did not fire after touches stopped")
	}
}

func TestWatchdogStopsCleanly(t *testing.T) {
	t.Parallel()

	timeout := 5 * time.Second // long timeout, should not fire
	parentCtx := context.Background()
	ctx, wd := NewSubagentWatchdog(parentCtx, timeout, slog.Default())

	wd.Stop()

	// After Stop(), context should be cancelled (with benign Canceled cause).
	if ctx.Err() == nil {
		t.Fatal("context should be cancelled after Stop()")
	}
	if !errors.Is(context.Cause(ctx), context.Canceled) {
		t.Fatalf("expected context.Canceled cause, got: %v", context.Cause(ctx))
	}
}

func TestWatchdogRespectsParentCancellation(t *testing.T) {
	t.Parallel()

	timeout := 5 * time.Second // long timeout, should not fire on its own
	parentCtx, parentCancel := context.WithCancel(context.Background())
	ctx, wd := NewSubagentWatchdog(parentCtx, timeout, slog.Default())
	defer wd.Stop()

	// Cancel the parent context.
	parentCancel()

	// Watchdog context should be cancelled immediately (not after timeout).
	select {
	case <-ctx.Done():
		// Expected — immediate cancellation.
	case <-time.After(200 * time.Millisecond):
		t.Fatal("watchdog context was not cancelled when parent was cancelled")
	}

	// The cause should be context.Canceled (from parent), not ErrWatchdogTimedOut.
	if errors.Is(context.Cause(ctx), ErrWatchdogTimedOut) {
		t.Fatal("watchdog should not have fired — parent was cancelled")
	}
}

func TestWatchdogTouchIsNonBlocking(t *testing.T) {
	t.Parallel()

	timeout := 5 * time.Second
	parentCtx := context.Background()
	ctx, wd := NewSubagentWatchdog(parentCtx, timeout, slog.Default())
	defer wd.Stop()

	// Rapidly call Touch many times — none should block.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10000; i++ {
			wd.Touch()
		}
	}()

	select {
	case <-done:
		// All Touch calls completed without blocking.
	case <-time.After(2 * time.Second):
		t.Fatal("Touch calls blocked — should be non-blocking")
	}

	// Context should not be cancelled (we've been touching).
	if ctx.Err() != nil {
		t.Fatalf("unexpected context cancellation: %v", context.Cause(ctx))
	}
}

func TestWatchdogTouchFromMultipleGoroutines(t *testing.T) {
	t.Parallel()

	timeout := 300 * time.Millisecond
	parentCtx := context.Background()
	ctx, wd := NewSubagentWatchdog(parentCtx, timeout, slog.Default())
	defer wd.Stop()

	// Multiple goroutines touch concurrently.
	var wg sync.WaitGroup
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				wd.Touch()
			}
		}()
	}
	wg.Wait()

	// Give the watchdog a moment to process touches.
	time.Sleep(50 * time.Millisecond)

	if ctx.Err() != nil {
		t.Fatalf("unexpected context cancellation: %v", context.Cause(ctx))
	}
}

func TestWatchdogDefaultTimeout(t *testing.T) {
	t.Parallel()

	// Passing zero timeout should use the default (subagentWatchdogTimeout).
	parentCtx := context.Background()
	ctx, wd := NewSubagentWatchdog(parentCtx, 0, slog.Default())
	defer wd.Stop()

	if wd.timeout != subagentWatchdogTimeout {
		t.Fatalf("expected default timeout %v, got %v", subagentWatchdogTimeout, wd.timeout)
	}
	if ctx.Err() != nil {
		t.Fatalf("unexpected context cancellation: %v", context.Cause(ctx))
	}
}

func TestWatchdogTimerResetOnTouch(t *testing.T) {
	t.Parallel()

	timeout := 200 * time.Millisecond
	parentCtx := context.Background()
	ctx, wd := NewSubagentWatchdog(parentCtx, timeout, slog.Default())
	defer wd.Stop()

	// Wait almost until timeout, then touch to reset.
	time.Sleep(150 * time.Millisecond)
	wd.Touch()

	// Now wait another 150ms — total 300ms > 200ms timeout, but we touched at 150ms.
	// The timer should have been reset at 150ms, so it shouldn't fire until 350ms.
	time.Sleep(150 * time.Millisecond)

	if ctx.Err() != nil {
		t.Fatalf("watchdog fired too early — timer was not properly reset: %v", context.Cause(ctx))
	}

	// Now wait for it to actually fire (no more touches).
	select {
	case <-ctx.Done():
		if !errors.Is(context.Cause(ctx), ErrWatchdogTimedOut) {
			t.Fatalf("expected ErrWatchdogTimedOut, got: %v", context.Cause(ctx))
		}
	case <-time.After(timeout + 200*time.Millisecond):
		t.Fatal("watchdog did not fire after expected time")
	}
}

func TestWatchdogFiresExactlyOnce(t *testing.T) {
	t.Parallel()

	timeout := 100 * time.Millisecond
	parentCtx := context.Background()
	ctx, wd := NewSubagentWatchdog(parentCtx, timeout, slog.Default())

	// Wait for watchdog to fire.
	select {
	case <-ctx.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("watchdog did not fire")
	}

	// Call Stop — should not panic or deadlock even after firing.
	wd.Stop()

	// Verify context cause is still the original fire.
	if !errors.Is(context.Cause(ctx), ErrWatchdogTimedOut) {
		t.Fatalf("expected ErrWatchdogTimedOut, got: %v", context.Cause(ctx))
	}
}

// --- Integration: watchdog + mock GenerateWithWatchdog ---

// mockSpawnAgent implements SpawnAgent for testing.
type mockSpawnAgent struct {
	// generateFunc is the function called by GenerateWithWatchdog.
	// It receives the context and a touchFn. The implementation should
	// call touchFn to simulate activity, or not call it to simulate a hang.
	generateFunc func(ctx context.Context, cfg SpawnRunConfig, touchFn func()) (*SpawnResult, error)

	// generateCount tracks how many times GenerateWithWatchdog was called.
	generateCount atomic.Int32
}

func (m *mockSpawnAgent) Generate(_ context.Context, _ SpawnRunConfig) (*SpawnResult, error) {
	_ = m // interface satisfaction only
	return nil, errors.New("not implemented in mock")
}

func (m *mockSpawnAgent) GenerateWithWatchdog(ctx context.Context, cfg SpawnRunConfig, touchFn func()) (*SpawnResult, error) {
	m.generateCount.Add(1)
	if m.generateFunc != nil {
		return m.generateFunc(ctx, cfg, touchFn)
	}
	return &SpawnResult{Text: "ok"}, nil
}

func TestWatchdogKillsStuckAgentAndRetries(t *testing.T) {
	t.Parallel()

	timeout := 200 * time.Millisecond
	callCount := atomic.Int32{}

	agent := &mockSpawnAgent{
		generateFunc: func(ctx context.Context, _ SpawnRunConfig, touchFn func()) (*SpawnResult, error) {
			count := callCount.Add(1)
			if count <= 2 {
				// First 2 calls: don't touch — simulate a stuck agent.
				<-ctx.Done()
				return nil, context.Cause(ctx)
			}
			// 3rd call: touch repeatedly to simulate normal activity.
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					touchFn()
				case <-ctx.Done():
					if errors.Is(context.Cause(ctx), ErrWatchdogTimedOut) {
						return nil, context.Cause(ctx)
					}
					return &SpawnResult{Text: "completed"}, nil
				}
			}
		},
	}

	// Simulate runSubagentTask's retry loop with short timeout.
	safetyCtx, safetyCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer safetyCancel()

	var lastErr error
	for attempt := 0; attempt <= 3; attempt++ {
		wdCtx, wd := NewSubagentWatchdog(safetyCtx, timeout, slog.Default())
		_, err := agent.GenerateWithWatchdog(wdCtx, SpawnRunConfig{}, wd.Touch)
		wd.Stop()

		if err == nil {
			// Success.
			return
		}
		lastErr = err
		if !errors.Is(err, ErrWatchdogTimedOut) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	t.Fatalf("agent never succeeded after retries, last error: %v", lastErr)
}

func TestWatchdogParentCancelStopsImmediately(t *testing.T) {
	t.Parallel()

	timeout := 5 * time.Second // long — should not fire
	parentCtx, parentCancel := context.WithCancel(context.Background())

	started := make(chan struct{})
	agent := &mockSpawnAgent{
		generateFunc: func(ctx context.Context, _ SpawnRunConfig, _ func()) (*SpawnResult, error) {
			close(started)
			<-ctx.Done()
			return nil, context.Cause(ctx)
		},
	}

	safetyCtx, safetyCancel := context.WithTimeout(parentCtx, 10*time.Second)
	defer safetyCancel()

	wdCtx, wd := NewSubagentWatchdog(safetyCtx, timeout, slog.Default())

	doneCh := make(chan error, 1)
	go func() {
		_, err := agent.GenerateWithWatchdog(wdCtx, SpawnRunConfig{}, wd.Touch)
		wd.Stop()
		doneCh <- err
	}()

	<-started // wait for agent to start
	parentCancel()

	select {
	case err := <-doneCh:
		if errors.Is(err, ErrWatchdogTimedOut) {
			t.Fatal("should not be ErrWatchdogTimedOut — parent was cancelled")
		}
		// Expected: parent cancellation propagated.
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not terminate after parent cancellation")
	}
}

func TestWatchdogKeepsActiveAgentAlive(t *testing.T) {
	t.Parallel()

	timeout := 200 * time.Millisecond
	agent := &mockSpawnAgent{
		generateFunc: func(ctx context.Context, _ SpawnRunConfig, touchFn func()) (*SpawnResult, error) {
			// Simulate a long-running agent that keeps touching the watchdog.
			ticker := time.NewTicker(50 * time.Millisecond)
			defer ticker.Stop()

			elapsed := time.After(800 * time.Millisecond) // run longer than timeout
			for {
				select {
				case <-ticker.C:
					touchFn()
				case <-elapsed:
					return &SpawnResult{Text: "done"}, nil
				case <-ctx.Done():
					return nil, context.Cause(ctx)
				}
			}
		},
	}

	safetyCtx, safetyCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer safetyCancel()

	wdCtx, wd := NewSubagentWatchdog(safetyCtx, timeout, slog.Default())
	result, err := agent.GenerateWithWatchdog(wdCtx, SpawnRunConfig{}, wd.Touch)
	wd.Stop()

	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if result.Text != "done" {
		t.Fatalf("unexpected result text: %q", result.Text)
	}
}

func TestWatchdogSafetyNetFiresWhenAgentTouchesButNeverConverges(t *testing.T) {
	t.Parallel()

	watchdogTimeout := 500 * time.Millisecond
	safetyTimeout := 300 * time.Millisecond

	agent := &mockSpawnAgent{
		generateFunc: func(ctx context.Context, _ SpawnRunConfig, touchFn func()) (*SpawnResult, error) {
			// Agent keeps touching but never completes — safety net should fire.
			ticker := time.NewTicker(50 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					touchFn()
				case <-ctx.Done():
					return nil, context.Cause(ctx)
				}
			}
		},
	}

	safetyCtx, safetyCancel := context.WithTimeout(context.Background(), safetyTimeout)
	defer safetyCancel()

	wdCtx, wd := NewSubagentWatchdog(safetyCtx, watchdogTimeout, slog.Default())
	_, err := agent.GenerateWithWatchdog(wdCtx, SpawnRunConfig{}, wd.Touch)
	wd.Stop()

	if err == nil {
		t.Fatal("expected error from safety net timeout")
	}
	// Safety net fires as context.DeadlineExceeded, not ErrWatchdogTimedOut.
	if errors.Is(err, ErrWatchdogTimedOut) {
		t.Fatal("should be safety net (DeadlineExceeded), not watchdog timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded from safety net, got: %v", err)
	}
}

func TestWatchdogStoppedAfterSuccess(t *testing.T) {
	t.Parallel()

	timeout := 100 * time.Millisecond
	agent := &mockSpawnAgent{
		generateFunc: func(_ context.Context, _ SpawnRunConfig, _ func()) (*SpawnResult, error) {
			return &SpawnResult{Text: "instant"}, nil
		},
	}

	safetyCtx, safetyCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer safetyCancel()

	wdCtx, wd := NewSubagentWatchdog(safetyCtx, timeout, slog.Default())
	result, err := agent.GenerateWithWatchdog(wdCtx, SpawnRunConfig{}, wd.Touch)
	wd.Stop()

	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if result.Text != "instant" {
		t.Fatalf("unexpected text: %q", result.Text)
	}

	// Verify context was cancelled by Stop() (benign Canceled), not by watchdog.
	if !errors.Is(context.Cause(wdCtx), context.Canceled) {
		t.Fatalf("expected Canceled cause from Stop(), got: %v", context.Cause(wdCtx))
	}
}

func TestWatchdogRetryBudget(t *testing.T) {
	t.Parallel()

	// Verify that after exhausting retries, the task fails with the correct error.
	timeout := 100 * time.Millisecond
	maxRetries := 3

	callCount := atomic.Int32{}
	agent := &mockSpawnAgent{
		generateFunc: func(ctx context.Context, _ SpawnRunConfig, _ func()) (*SpawnResult, error) {
			callCount.Add(1)
			<-ctx.Done() // always stuck
			return nil, context.Cause(ctx)
		},
	}

	safetyCtx, safetyCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer safetyCancel()

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		wdCtx, wd := NewSubagentWatchdog(safetyCtx, timeout, slog.Default())
		_, err := agent.GenerateWithWatchdog(wdCtx, SpawnRunConfig{}, wd.Touch)
		wd.Stop()
		if err == nil {
			t.Fatal("should not succeed")
		}
		lastErr = err
		if !errors.Is(err, ErrWatchdogTimedOut) {
			t.Fatalf("attempt %d: expected ErrWatchdogTimedOut, got: %v", attempt, err)
		}
	}

	calls := callCount.Load()
	if calls != int32(maxRetries+1) {
		t.Fatalf("expected %d calls (maxRetries+1), got %d", maxRetries+1, calls)
	}
	if lastErr == nil || !errors.Is(lastErr, ErrWatchdogTimedOut) {
		t.Fatalf("last error should be ErrWatchdogTimedOut, got: %v", lastErr)
	}
}
