package application

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/reasoning"
)

// idleCancel wraps a resettable idle timer. If Reset() is not called before
// the timer fires, the underlying context is cancelled.
type idleCancel struct {
	cancel      context.CancelCauseFunc
	timer       *time.Timer
	mu          sync.Mutex
	fired       bool
	baseTimeout time.Duration
	maxTimeout  time.Duration
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

// currentTimeout returns the adaptive timeout: base + 60s per tool call, capped
// at maxTimeout. Tool calls (especially spawn/subagent) can take minutes, so the
// extension per tool call is generous to avoid interrupting active work.
func (ic *idleCancel) currentTimeout() time.Duration {
	extra := time.Duration(ic.toolCalls) * 60 * time.Second
	timeout := ic.baseTimeout + extra
	if ic.maxTimeout > 0 && timeout > ic.maxTimeout {
		timeout = ic.maxTimeout
	}
	return timeout
}

const (
	defaultIdleTimeout    = 90 * time.Second
	defaultIdleTimeoutMax = 15 * time.Minute
)

// withIdleTimeout returns a context that is cancelled if no Reset() call is
// made within the adaptive idle timeout. Optional durations are base then max.
// The timeout adapts: base + 60s per tool call, capped at max (15m default).
func withIdleTimeout(parent context.Context, timeouts ...time.Duration) (context.Context, *idleCancel) {
	bt := defaultIdleTimeout
	if len(timeouts) > 0 && timeouts[0] > 0 {
		bt = timeouts[0]
	}
	mx := defaultIdleTimeoutMax
	if len(timeouts) > 1 && timeouts[1] > 0 {
		mx = timeouts[1]
	}

	ctx, cancel := context.WithCancelCause(parent)
	ic := &idleCancel{
		cancel:      cancel,
		baseTimeout: bt,
		maxTimeout:  mx,
	}
	ic.timer = time.AfterFunc(bt, func() {
		ic.mu.Lock()
		ic.fired = true
		ic.mu.Unlock()
		cancel(apperror.Wrap(apperror.CodeAgentResponseTimeout, context.DeadlineExceeded, nil))
	})
	return ctx, ic
}

func (s *Service) withStreamIdleTimeout(parent context.Context, effort string) (context.Context, *idleCancel) {
	base := defaultIdleTimeout
	maxTimeout := defaultIdleTimeoutMax
	if s != nil && s.streamIdleTimeout > 0 {
		base = s.streamIdleTimeout
	}
	if s != nil && s.streamIdleTimeoutMax > 0 {
		maxTimeout = s.streamIdleTimeoutMax
	}
	return withIdleTimeout(parent, scaleIdleTimeoutForEffort(base, effort), maxTimeout)
}

func scaleIdleTimeoutForEffort(base time.Duration, effort string) time.Duration {
	if base <= 0 {
		base = defaultIdleTimeout
	}
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case reasoning.EffortMax:
		return base * 8
	case reasoning.EffortXHigh:
		return base * 6
	case reasoning.EffortHigh:
		return base * 4
	case reasoning.EffortMedium:
		return base * 2
	default:
		return base
	}
}

func reasoningEffortForIdle(cfg native.RunConfig) string {
	if cfg.ReasoningConfig != nil {
		if effort := strings.TrimSpace(cfg.ReasoningConfig.Effort); effort != "" {
			return effort
		}
	}
	return strings.TrimSpace(cfg.ReasoningRequestedEffort)
}
