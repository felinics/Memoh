package workspacedeps

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

func TestStartReaperRunsImmediatelyAndOnInterval(t *testing.T) {
	f := newServiceFixture(t)
	agent := f.cat.MustGet("agent-x")
	budget := agent.Timeouts.Duration(catalog.ActionReinstall) + lockStaleGrace
	f.store.seed(Installation{BotID: "b1", DependencyID: "agent-x", Status: StatusInstalling, UpdatedAt: f.now.Add(-budget)})

	stop := StartReaper(context.Background(), f.svc, 10*time.Millisecond, slog.New(slog.DiscardHandler))
	defer stop()

	waitFor(t, func() bool {
		recs, _ := f.store.ListForBot(context.Background(), "b1")
		return len(recs) == 1 && recs[0].Status == StatusFailed
	})

	// A record that goes stale later is caught by a following tick.
	f.store.seed(Installation{BotID: "b2", DependencyID: "agent-x", Status: StatusUpdating, UpdatedAt: f.now.Add(-budget)})
	waitFor(t, func() bool {
		recs, _ := f.store.ListForBot(context.Background(), "b2")
		return len(recs) == 1 && recs[0].Status == StatusFailed
	})
}

func TestStartReaperStopIsIdempotentAndWaits(t *testing.T) {
	f := newServiceFixture(t)
	stop := StartReaper(context.Background(), f.svc, time.Hour, nil)
	stop()
	stop()
	writesAfterStop := f.store.writes
	time.Sleep(20 * time.Millisecond)
	if f.store.writes != writesAfterStop {
		t.Fatalf("reaper kept writing after stop: %d -> %d", writesAfterStop, f.store.writes)
	}
}

func TestStartReaperStopsWhenContextEnds(t *testing.T) {
	f := newServiceFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	stop := StartReaper(ctx, f.svc, time.Hour, nil)
	cancel()
	finished := make(chan struct{})
	go func() {
		stop()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not return after the context ended")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
