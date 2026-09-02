package workspacedeps

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// DefaultReapInterval is how often the stale reaper runs after its initial
// pass (WD-STATE-002).
const DefaultReapInterval = time.Hour

// StartReaper runs ReapStale once right away and then every interval until
// the returned stop function is called or ctx ends. A container restart cuts
// an exec mid-flight and leaves the record in installing/updating/removing;
// the reaper is what turns that into failed once the script's own timeout
// has passed (WD-STATE-002). A non-positive interval selects
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
	logger = logger.With(slog.String("component", "workspacedeps_reaper"))

	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		reap := func() {
			reaped, err := svc.ReapStale(loopCtx)
			switch {
			case err != nil && loopCtx.Err() != nil:
				// Shutting down; the interrupted pass is not a fault.
			case err != nil:
				logger.Warn("stale dependency reaper pass finished with errors",
					slog.Int("reaped", reaped),
					slog.Any("error", err),
				)
			case reaped > 0:
				logger.Info("stale dependency operations marked failed", slog.Int("reaped", reaped))
			}
		}
		reap()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
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
