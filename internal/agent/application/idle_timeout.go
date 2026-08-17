package application

import (
	"context"
	"time"

	"github.com/memohai/memoh/internal/agent/runtime/watchdog"
)

type idleCancel = watchdog.Controller

// withIdleTimeout returns a context that is cancelled if no Reset() call is
// made within the adaptive idle timeout. The returned idleCancel must have
// Reset() called for each meaningful event to prevent the timeout from firing.
// The timeout adapts: base + 60s per tool call, capped at 600s.
func withIdleTimeout(parent context.Context, baseTimeout ...time.Duration) (context.Context, *idleCancel) {
	return watchdog.WithIdleTimeout(parent, baseTimeout...)
}

func (s *Service) withStreamIdleTimeout(parent context.Context) (context.Context, *idleCancel) {
	if s != nil && s.streamIdleTimeout > 0 {
		return withIdleTimeout(parent, s.streamIdleTimeout)
	}
	return withIdleTimeout(parent)
}
