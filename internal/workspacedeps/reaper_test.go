package workspacedeps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

func TestStartReaperRunsImmediatelyAndOnInterval(t *testing.T) {
	f := newServiceFixture(t)
	f.store.seed(Installation{BotID: "b1", DependencyID: "agent-x", Status: StatusInstalling, OperationID: strings.Repeat("a", 32), UpdatedAt: f.now.Add(-2 * time.Minute)})
	ticks := make(chan time.Time)
	type passResult struct {
		count int
		err   error
	}
	completed := make(chan passResult, 1)
	reap := func(ctx context.Context) (int, error) {
		count, err := f.svc.ReapStale(ctx)
		completed <- passResult{count: count, err: err}
		return count, err
	}
	stop := startReaper(f.ctx(), reap, ticks, func() {}, slog.New(slog.DiscardHandler))
	defer stop()

	assertPass := func(botID string) {
		t.Helper()
		pass := <-completed
		if pass.err != nil || pass.count != 1 {
			t.Fatalf("reaper pass = %d, %v; want one fenced operation", pass.count, pass.err)
		}
		records, err := f.store.ListForBot(f.ctx(), botID)
		if err != nil || len(records) != 1 || records[0].Status != StatusFailed || records[0].OperationID != "" {
			t.Fatalf("operation was not fenced and released: %+v %v", records, err)
		}
	}
	assertPass("b1")

	// This reachable workspace is added after the initial pass. Only an
	// explicit tick can schedule the next recovery round.
	f.store.seed(Installation{BotID: "b2", DependencyID: "agent-x", Status: StatusUpdating, OperationID: strings.Repeat("b", 32), UpdatedAt: f.now.Add(-2 * time.Minute)})
	ticks <- f.now
	assertPass("b2")
}

func TestStartReaperStopIsIdempotentAndWaits(t *testing.T) {
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ticks, tickerStopped := make(chan time.Time), make(chan struct{})
	reap := func(ctx context.Context) (int, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-release
		return 0, ctx.Err()
	}
	stop := startReaper(t.Context(), reap, ticks, func() { close(tickerStopped) }, slog.New(slog.DiscardHandler))
	<-started
	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	<-cancelled
	select {
	case <-stopped:
		t.Fatal("stop returned while a pass was still in flight")
	default:
	}
	close(release)
	<-stopped
	<-tickerStopped
	stop()
	select {
	case ticks <- time.Time{}:
		t.Fatal("stopped reaper consumed a tick")
	default:
	}
}

func TestStartReaperStopsWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started, tickerStopped := make(chan struct{}), make(chan struct{})
	reap := func(ctx context.Context) (int, error) {
		close(started)
		<-ctx.Done()
		return 0, ctx.Err()
	}
	stop := startReaper(ctx, reap, make(chan time.Time), func() { close(tickerStopped) }, slog.New(slog.DiscardHandler))
	defer stop()
	<-started
	cancel()
	// Loop cleanup proves cancellation ends the worker before Stop is called.
	<-tickerStopped
	stop()
}

func TestStartReaperWritesOneResultRecordPerPass(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	ticks := make(chan time.Time)
	done := make(chan struct{})
	reap := func(context.Context) (int, error) {
		defer close(done)
		return 2, errors.New("one operation could not be reconciled")
	}
	stop := startReaper(t.Context(), reap, ticks, func() {}, logger)
	<-done
	stop()

	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		records = append(records, rec)
	}
	if len(records) != 1 {
		t.Fatalf("records = %v, want exactly one", records)
	}
	rec := records[0]
	if rec["msg"] != "job" || rec["operation"] != "workspacedeps.reap" || rec["level"] != "ERROR" || rec["reaped"] != float64(2) {
		t.Fatalf("record = %v, want an ERROR job record for workspacedeps.reap with reaped=2", rec)
	}
}

// countingCatalog records how often the Service loads the provider catalog.
type countingCatalog struct {
	snapshotCatalog
	loads int
}

func (p *countingCatalog) Snapshot(ctx context.Context, refresh bool) (CatalogResult, error) {
	p.loads++
	return p.snapshotCatalog.Snapshot(ctx, refresh)
}

func (p *countingCatalog) Cached(ctx context.Context) (CatalogResult, error) {
	p.loads++
	return p.snapshotCatalog.Cached(ctx)
}

// A reaper pass with nothing to reconcile must not load the catalog: the
// reaper runs per team, and hydrating verifies every stored definition.
func TestReapStaleLoadsCatalogOnlyForAbandonedOperations(t *testing.T) {
	f := newServiceFixture(t)
	provider := &countingCatalog{snapshotCatalog: snapshotCatalog{cat: f.cat}}
	f.svc.provider, f.svc.catalog = provider, catalog.Empty()
	reap := func(want int) {
		t.Helper()
		if count, err := f.svc.ReapStale(f.ctx()); err != nil || count != want {
			t.Fatalf("ReapStale = %d, %v; want %d", count, err, want)
		}
	}

	reap(0)
	f.store.seed(Installation{BotID: "settled", DependencyID: "agent-x", Status: StatusInstalled})
	reap(0)
	running := InstallationKey{BotID: "running", DependencyID: "agent-x"}
	f.store.seed(Installation{BotID: running.BotID, DependencyID: running.DependencyID, Status: StatusInstalling, OperationID: strings.Repeat("d", 32), UpdatedAt: f.now.Add(-2 * time.Minute)})
	f.svc.locks.tryLock(running)
	reap(0)
	f.svc.locks.unlock(running)
	if provider.loads != 0 {
		t.Fatalf("catalog loaded %d times with no abandoned operation", provider.loads)
	}

	reap(1)
	if provider.loads != 1 {
		t.Fatalf("catalog loads = %d, want one for the abandoned operation", provider.loads)
	}
}
