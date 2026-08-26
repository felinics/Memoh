package application

import (
	"context"
	"sync"
	"time"

	"github.com/memohai/memoh/internal/apperror"
)

// idleCancel wraps a resettable idle timer. If Reset() is not called before
// the timer fires, the underlying context is cancelled.
type idleCancel struct {
	cancel      context.CancelCauseFunc
	timer       *time.Timer
	mu          sync.Mutex
	fired       bool
	baseTimeout time.Duration
	toolCalls   int
}

func (ic *idleCancel) Reset() {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	ic.rearmLocked()
}

// RecordToolCall increments the tool call counter and extends the idle timeout.
func (ic *idleCancel) RecordToolCall() {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	ic.toolCalls++
	ic.rearmLocked()
}

func (ic *idleCancel) rearmLocked() {
	if ic.fired {
		return
	}
	ic.timer.Stop()
	ic.timer.Reset(ic.currentTimeout())
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
// The timeout adapts: base + 60s per tool call, capped at 600s.
func withIdleTimeout(parent context.Context, baseTimeout ...time.Duration) (context.Context, *idleCancel) {
	bt := defaultIdleTimeout
	if len(baseTimeout) > 0 && baseTimeout[0] > 0 {
		bt = baseTimeout[0]
	}

	ctx, cancel := context.WithCancelCause(parent)
	ic := &idleCancel{
		cancel:      cancel,
		baseTimeout: bt,
	}
	ic.timer = time.AfterFunc(bt, func() {
		ic.mu.Lock()
		ic.fired = true
		ic.mu.Unlock()
		cancel(apperror.Wrap(apperror.CodeAgentResponseTimeout, context.DeadlineExceeded, nil))
	})
	return ctx, ic
}

func (s *Service) withStreamIdleTimeout(parent context.Context) (context.Context, *idleCancel) {
	if s != nil && s.streamIdleTimeout > 0 {
		return withIdleTimeout(parent, s.streamIdleTimeout)
	}
	return withIdleTimeout(parent)
}
