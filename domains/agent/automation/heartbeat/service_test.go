package heartbeat

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
)

type runOnceSchedule struct {
	nextCalled atomic.Bool
}

func (s *runOnceSchedule) Next(now time.Time) time.Time {
	if s.nextCalled.Swap(true) {
		return time.Time{}
	}
	return now
}

func TestServiceLifecycle(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := NewService(logger, nil, nil, nil, "")

	started := make(chan struct{})
	stopped := make(chan struct{})
	var runs atomic.Int32
	svc.cron.Schedule(&runOnceSchedule{}, cron.FuncJob(func() {
		runCtx, cancel, ok := svc.jobContext(time.Minute)
		if !ok {
			return
		}
		defer cancel()
		runs.Add(1)
		close(started)
		<-runCtx.Done()
		close(stopped)
	}))

	select {
	case <-started:
		t.Fatal("NewService started the cron scheduler")
	default:
	}

	if err := svc.Start(t.Context()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := svc.Start(t.Context()); err != nil {
		t.Fatalf("second Start() error = %v", err)
	}
	waitForSignal(t, started, "heartbeat job to start")

	if err := svc.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	waitForSignal(t, stopped, "active heartbeat job to stop")
	if err := svc.Shutdown(t.Context()); err != nil {
		t.Fatalf("second Shutdown(): %v", err)
	}
	if err := svc.Start(t.Context()); err == nil {
		t.Fatal("Start() after Shutdown() succeeded")
	}
	if got := runs.Load(); got != 1 {
		t.Fatalf("job runs = %d, want 1", got)
	}
}

func waitForSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for %s", description)
	}
}

func TestNormalizeHeartbeatIntervalDefault(t *testing.T) {
	t.Parallel()

	if got := normalizeHeartbeatInterval(0); got != 1440 {
		t.Fatalf("normalizeHeartbeatInterval(0) = %d, want 1440", got)
	}
	if got := normalizeHeartbeatInterval(-5); got != 1440 {
		t.Fatalf("normalizeHeartbeatInterval(-5) = %d, want 1440", got)
	}
	if got := normalizeHeartbeatInterval(60); got != 60 {
		t.Fatalf("normalizeHeartbeatInterval(60) = %d, want 60", got)
	}
}
