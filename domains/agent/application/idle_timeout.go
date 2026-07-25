package application

import (
	"context"
	"sync"
	"time"
)

// idleCancel wraps a resettable idle timer. If Reset() is not called before
// the timer fires, the underlying context is cancelled.
type idleCancel struct {
	cancel      context.CancelFunc
	timer       *time.Timer
	mu          sync.Mutex
	fired       bool
	baseTimeout time.Duration
	toolCalls   int
}

func (ic *idleCancel) Reset() {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	if !ic.fired {
		ic.timer.Stop()
		ic.timer.Reset(ic.currentTimeout())
	}
}

// RecordToolCall increments the tool call counter and extends the idle timeout.
func (ic *idleCancel) RecordToolCall() {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	ic.toolCalls++
}

func (ic *idleCancel) Stop() {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	ic.timer.Stop()
}

func (ic *idleCancel) DidFire() bool {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	return ic.fired
}

// ToolCalls returns the number of tool calls recorded.
func (ic *idleCancel) ToolCalls() int {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	return ic.toolCalls
}

// currentTimeout returns the adaptive timeout: base + 60s per tool call, capped at 600s.
// Tool calls (especially spawn/subagent) can take minutes to complete, so the
// extension per tool call is generous to avoid interrupting active work.
func (ic *idleCancel) currentTimeout() time.Duration {
	extra := time.Duration(ic.toolCalls) * 60 * time.Second
	timeout := ic.baseTimeout + extra
	if timeout > 600*time.Second {
		timeout = 600 * time.Second
	}
	return timeout
}

const defaultIdleTimeout = 90 * time.Second

// withIdleTimeout returns a context that is cancelled if no Reset() call is
// made within the adaptive idle timeout. The returned idleCancel must have
// Reset() called for each meaningful event to prevent the timeout from firing.
// The timeout adapts: base + 10s per tool call, capped at 300s.
func withIdleTimeout(parent context.Context, baseTimeout ...time.Duration) (context.Context, *idleCancel) {
	bt := defaultIdleTimeout
	if len(baseTimeout) > 0 && baseTimeout[0] > 0 {
		bt = baseTimeout[0]
	}

	ctx, cancel := context.WithCancel(parent)
	ic := &idleCancel{
		cancel:      cancel,
		baseTimeout: bt,
	}
	ic.timer = time.AfterFunc(bt, func() {
		ic.mu.Lock()
		ic.fired = true
		ic.mu.Unlock()
		cancel()
	})
	return ctx, ic
}
