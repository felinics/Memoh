package workspacedeps

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// DefaultReapInterval is how often the stale reaper runs after its initial
// pass.
const DefaultReapInterval = time.Minute

// StartReaper runs ReapStale once right away and then every interval until
// the returned stop function is called or ctx ends. A container restart cuts
// an exec mid-flight and leaves the record in installing/updating/removing.
// The reaper checks kernel lock ownership and durable receipts to complete a
// confirmed result or mark an abandoned operation retryable. It never expires
// a live owner merely because time passed. A non-positive interval selects
// DefaultReapInterval. Stop waits for a pass in flight and is idempotent.
func StartReaper(ctx context.Context, svc *Service, interval time.Duration, logger *slog.Logger) (stop func()) {
	if svc == nil {
		panic("workspacedeps: reaper needs a service")
	}
	if interval <= 0 {
		interval = DefaultReapInterval
	}
	if logger == nil {
		logger = slog.Default()
	}
	ticker := time.NewTicker(interval)
	return startReaper(ctx, svc.ReapStale, ticker.C, ticker.Stop, logger)
}

// startReaper keeps timer ownership and pass execution together. Its explicit
// tick source lets lifecycle tests coordinate work without wall-clock races.
func startReaper(ctx context.Context, reapPass func(context.Context) (int, error), ticks <-chan time.Time, stopTicks func(), logger *slog.Logger) func() {
	logger = logger.With(slog.String("component", "workspacedeps_reaper"))
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer stopTicks()
		reap := func() {
			reaped, err := reapPass(loopCtx)
			switch {
			case err != nil && loopCtx.Err() != nil:
				// Shutting down; the interrupted pass is not a fault.
			case err != nil:
				logger.Warn("stale dependency reaper pass finished with errors",
					slog.Int("reaped", reaped),
					slog.Any("error", err),
				)
			case reaped > 0:
				logger.Info("interrupted dependency operations reconciled", slog.Int("reaped", reaped))
			}
		}
		reap()
		for loopCtx.Err() == nil {
			select {
			case <-loopCtx.Done():
				return
			case <-ticks:
				reap()
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}
