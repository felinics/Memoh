// Package watchdog provides an activity-based cancellation timer for agent
// streams. It is deliberately independent from the application layer so
// native runtime adapters can use the same timeout policy.
package watchdog

import (
	"context"
	"sync"
	"time"
)

const (
	DefaultTimeout = 90 * time.Second
	MaxTimeout     = 600 * time.Second
	ToolExtension  = 60 * time.Second
)

// Controller owns a resettable idle timer. Tool calls extend the next idle
// window because a tool can legitimately take longer than the first response.
type Controller struct {
	timer       *time.Timer
	mu          sync.Mutex
	fired       bool
	baseTimeout time.Duration
	toolCalls   int
}

func (c *Controller) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.fired {
		c.timer.Stop()
		c.timer.Reset(c.currentTimeout())
	}
}

func (c *Controller) RecordToolCall() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.toolCalls++
	if !c.fired {
		c.timer.Stop()
		c.timer.Reset(c.currentTimeout())
	}
}

func (c *Controller) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.timer.Stop()
}

func (c *Controller) DidFire() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fired
}

func (c *Controller) currentTimeout() time.Duration {
	timeout := c.baseTimeout + time.Duration(c.toolCalls)*ToolExtension
	if timeout > MaxTimeout {
		return MaxTimeout
	}
	return timeout
}

// WithIdleTimeout returns a context canceled after the configured period of
// stream inactivity. A positive baseTimeout is intended for deterministic
// tests; production callers use DefaultTimeout.
func WithIdleTimeout(parent context.Context, baseTimeout ...time.Duration) (context.Context, *Controller) {
	base := DefaultTimeout
	if len(baseTimeout) > 0 && baseTimeout[0] > 0 {
		base = baseTimeout[0]
	}
	ctx, cancel := context.WithCancel(parent)
	c := &Controller{baseTimeout: base}
	c.timer = time.AfterFunc(base, func() {
		c.mu.Lock()
		c.fired = true
		c.mu.Unlock()
		cancel()
	})
	return ctx, c
}
