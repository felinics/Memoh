package workspace

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

type fakeWatchRun struct {
	botID string
	dir   string
	ctx   context.Context
	batch func([]string)
	errCh chan error
}

type fakeWatcher struct {
	mu   sync.Mutex
	runs []*fakeWatchRun
	ch   chan *fakeWatchRun
}

func newFakeWatcher() *fakeWatcher {
	return &fakeWatcher{ch: make(chan *fakeWatchRun, 16)}
}

func (f *fakeWatcher) watchDir(ctx context.Context, botID, dir string, onBatch func([]string)) error {
	run := &fakeWatchRun{botID: botID, dir: dir, ctx: ctx, batch: onBatch, errCh: make(chan error, 1)}
	f.mu.Lock()
	f.runs = append(f.runs, run)
	f.mu.Unlock()
	f.ch <- run
	select {
	case <-ctx.Done():
		return nil
	case err := <-run.errCh:
		return err
	}
}

func (f *fakeWatcher) next(t *testing.T) *fakeWatchRun {
	t.Helper()
	select {
	case run := <-f.ch:
		return run
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watch run")
		return nil
	}
}

func (f *fakeWatcher) expectNone(t *testing.T, wait time.Duration) {
	t.Helper()
	select {
	case run := <-f.ch:
		t.Fatalf("unexpected watch run for %s %s", run.botID, run.dir)
	case <-time.After(wait):
	}
}

type publishRecord struct {
	botID string
	paths []string
}

func newTestFSWatchService(watcher *fakeWatcher) (*FSWatchService, chan publishRecord) {
	published := make(chan publishRecord, 16)
	svc := NewFSWatchService(nil, nil, func(botID string, paths []string) {
		published <- publishRecord{botID: botID, paths: paths}
	})
	svc.watchDir = watcher.watchDir
	svc.retryDelay = 20 * time.Millisecond
	return svc, published
}

func TestFSWatchServiceStartsAndStopsWithSubscriptions(t *testing.T) {
	watcher := newFakeWatcher()
	svc, published := newTestFSWatchService(watcher)

	svc.SetSubscription("conn-1", "bot-1", []string{"/data"})
	run := watcher.next(t)
	if run.botID != "bot-1" || run.dir != "/data" {
		t.Fatalf("run = %s %s", run.botID, run.dir)
	}

	run.batch([]string{"/data/a.txt"})
	select {
	case rec := <-published:
		if rec.botID != "bot-1" || len(rec.paths) != 1 || rec.paths[0] != "/data/a.txt" {
			t.Fatalf("published = %+v", rec)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for publish")
	}

	// A second viewer of the same dir shares the watch.
	svc.SetSubscription("conn-2", "bot-1", []string{"/data"})
	watcher.expectNone(t, 100*time.Millisecond)

	svc.DropSubscription("conn-1")
	select {
	case <-run.ctx.Done():
		t.Fatal("watch canceled while another viewer holds it")
	case <-time.After(100 * time.Millisecond):
	}

	svc.DropSubscription("conn-2")
	select {
	case <-run.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("watch not canceled after last viewer left")
	}
}

func TestFSWatchServiceDiffsSubscriptionDirs(t *testing.T) {
	watcher := newFakeWatcher()
	svc, _ := newTestFSWatchService(watcher)

	svc.SetSubscription("conn-1", "bot-1", []string{"/data", "/data/sub"})
	first := watcher.next(t)
	second := watcher.next(t)

	svc.SetSubscription("conn-1", "bot-1", []string{"/data/sub"})
	var rootRun *fakeWatchRun
	if first.dir == "/data" {
		rootRun = first
	} else {
		rootRun = second
	}
	select {
	case <-rootRun.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("dropped dir watch not canceled")
	}
}

func TestFSWatchServiceUnsupportedBridgeBacksOff(t *testing.T) {
	watcher := newFakeWatcher()
	svc, _ := newTestFSWatchService(watcher)

	svc.SetSubscription("conn-1", "bot-1", []string{"/data"})
	run := watcher.next(t)
	run.errCh <- bridge.ErrWatchUnsupported

	// New subscriptions for the same bot do not retry while marked
	// unsupported.
	time.Sleep(50 * time.Millisecond)
	svc.SetSubscription("conn-1", "bot-1", []string{"/data", "/data/sub"})
	watcher.expectNone(t, 150*time.Millisecond)
}

func TestFSWatchServiceRecoversAfterUnsupportedTTL(t *testing.T) {
	watcher := newFakeWatcher()
	svc, _ := newTestFSWatchService(watcher)
	svc.unsupportedFor = 50 * time.Millisecond

	svc.SetSubscription("conn-1", "bot-1", []string{"/data"})
	run := watcher.next(t)
	run.errCh <- bridge.ErrWatchUnsupported

	// Within the TTL an identical re-send stays backed off.
	time.Sleep(20 * time.Millisecond)
	svc.SetSubscription("conn-1", "bot-1", []string{"/data"})
	watcher.expectNone(t, 20*time.Millisecond)

	// After the TTL (e.g. the workspace was upgraded to a watch-capable
	// bridge), re-sending the SAME set restarts the watch — a subscription
	// must not stay watchless forever just because its dirs never changed.
	time.Sleep(60 * time.Millisecond)
	svc.SetSubscription("conn-1", "bot-1", []string{"/data"})
	retry := watcher.next(t)
	if retry.dir != "/data" {
		t.Fatalf("retry dir = %s", retry.dir)
	}
}

func TestFSWatchServiceReacquiresAutomaticallyAfterUnsupportedTTL(t *testing.T) {
	watcher := newFakeWatcher()
	svc, _ := newTestFSWatchService(watcher)
	svc.unsupportedFor = 50 * time.Millisecond

	svc.SetSubscription("conn-1", "bot-1", []string{"/data"})
	run := watcher.next(t)
	run.errCh <- bridge.ErrWatchUnsupported

	// The client's reporter suppresses identical directory sets, so with the
	// pane simply left open no further SetSubscription arrives. The service
	// itself must retry still-wanted keys once the TTL expires.
	retry := watcher.next(t)
	if retry.botID != "bot-1" || retry.dir != "/data" {
		t.Fatalf("auto retry = %s %s", retry.botID, retry.dir)
	}
}

func TestFSWatchServiceEnforcesPerBotBudget(t *testing.T) {
	watcher := newFakeWatcher()
	svc, _ := newTestFSWatchService(watcher)
	svc.maxPerBot = 2

	svc.SetSubscription("conn-1", "bot-1", []string{"/data", "/data/a", "/data/b"})
	first := watcher.next(t)
	second := watcher.next(t)
	if first.botID != "bot-1" || second.botID != "bot-1" {
		t.Fatalf("runs = %+v %+v", first, second)
	}
	watcher.expectNone(t, 100*time.Millisecond)

	// Another bot is unaffected by bot-1's budget.
	svc.SetSubscription("conn-2", "bot-2", []string{"/data"})
	other := watcher.next(t)
	if other.botID != "bot-2" {
		t.Fatalf("other bot run = %+v", other)
	}
}

func TestFSWatchServiceEnforcesTotalBudget(t *testing.T) {
	watcher := newFakeWatcher()
	svc, _ := newTestFSWatchService(watcher)
	svc.maxTotal = 2

	svc.SetSubscription("conn-1", "bot-1", []string{"/data", "/data/a"})
	watcher.next(t)
	watcher.next(t)
	svc.SetSubscription("conn-2", "bot-2", []string{"/data"})
	watcher.expectNone(t, 100*time.Millisecond)

	// Releasing capacity lets a later subscription start a watch again.
	svc.DropSubscription("conn-1")
	svc.SetSubscription("conn-2", "bot-2", []string{"/data", "/data/x"})
	if run := watcher.next(t); run.botID != "bot-2" {
		t.Fatalf("run after release = %+v", run)
	}
}

func TestFSWatchServicePromotesWatchlessKeysWhenBudgetFrees(t *testing.T) {
	watcher := newFakeWatcher()
	svc, _ := newTestFSWatchService(watcher)
	svc.maxPerBot = 1

	svc.SetSubscription("conn-1", "bot-1", []string{"/data"})
	first := watcher.next(t)
	if first.dir != "/data" {
		t.Fatalf("first dir = %s", first.dir)
	}
	// Over budget: recorded but watchless.
	svc.SetSubscription("conn-2", "bot-1", []string{"/data/sub"})
	watcher.expectNone(t, 100*time.Millisecond)

	// The frontend suppresses unchanged fs_watch reports, so freeing capacity
	// must promote the waiting key without any new client message.
	svc.DropSubscription("conn-1")
	promoted := watcher.next(t)
	if promoted.dir != "/data/sub" {
		t.Fatalf("promoted dir = %s", promoted.dir)
	}
}

func TestFSWatchServicePromotesAcrossBotsWhenTotalBudgetFrees(t *testing.T) {
	watcher := newFakeWatcher()
	svc, _ := newTestFSWatchService(watcher)
	svc.maxTotal = 1

	svc.SetSubscription("conn-1", "bot-1", []string{"/data"})
	watcher.next(t)
	svc.SetSubscription("conn-2", "bot-2", []string{"/data"})
	watcher.expectNone(t, 100*time.Millisecond)

	svc.DropSubscription("conn-1")
	promoted := watcher.next(t)
	if promoted.botID != "bot-2" || promoted.dir != "/data" {
		t.Fatalf("promoted = %s %s", promoted.botID, promoted.dir)
	}
}

func TestFSWatchServiceShrinkingASetPromotesWaiters(t *testing.T) {
	watcher := newFakeWatcher()
	svc, _ := newTestFSWatchService(watcher)
	svc.maxPerBot = 1

	svc.SetSubscription("conn-1", "bot-1", []string{"/data"})
	watcher.next(t)
	svc.SetSubscription("conn-2", "bot-1", []string{"/data/sub"})
	watcher.expectNone(t, 100*time.Millisecond)

	// conn-1 collapses its directory: SetSubscription's removal path must
	// promote just like a full drop.
	svc.SetSubscription("conn-1", "bot-1", nil)
	promoted := watcher.next(t)
	if promoted.dir != "/data/sub" {
		t.Fatalf("promoted dir = %s", promoted.dir)
	}
}

func TestFSWatchServiceUnsupportedDeathPromotesOtherBots(t *testing.T) {
	watcher := newFakeWatcher()
	svc, _ := newTestFSWatchService(watcher)
	svc.maxTotal = 1
	svc.unsupportedFor = time.Hour

	svc.SetSubscription("conn-1", "bot-1", []string{"/data"})
	run := watcher.next(t)
	svc.SetSubscription("conn-2", "bot-2", []string{"/data"})
	watcher.expectNone(t, 100*time.Millisecond)

	// bot-1's bridge cannot watch: its slot must go to the waiting bot-2
	// immediately, not sit reserved for bot-1's TTL timer.
	run.errCh <- bridge.ErrWatchUnsupported
	promoted := watcher.next(t)
	if promoted.botID != "bot-2" || promoted.dir != "/data" {
		t.Fatalf("promoted = %s %s", promoted.botID, promoted.dir)
	}
}

func TestFSWatchServiceStreamDeathPromotesWaiters(t *testing.T) {
	watcher := newFakeWatcher()
	svc, _ := newTestFSWatchService(watcher)
	svc.maxTotal = 1

	svc.SetSubscription("conn-1", "bot-1", []string{"/data"})
	run := watcher.next(t)
	svc.SetSubscription("conn-2", "bot-2", []string{"/data"})
	watcher.expectNone(t, 100*time.Millisecond)

	run.errCh <- errors.New("stream broke")
	promoted := watcher.next(t)
	if promoted.botID != "bot-2" {
		t.Fatalf("promoted = %s %s", promoted.botID, promoted.dir)
	}
}

func TestFSWatchServiceFailingWatchWaitsForRetryDelay(t *testing.T) {
	watcher := newFakeWatcher()
	svc, _ := newTestFSWatchService(watcher)
	svc.retryDelay = 80 * time.Millisecond

	svc.SetSubscription("conn-1", "bot-1", []string{"/data"})
	run := watcher.next(t)
	failedAt := time.Now()
	run.errCh <- errors.New("dial failed")

	// The death-path promotion must not restart the key that just died —
	// that would bypass the retry backoff and spin on a persistent failure.
	retry := watcher.next(t)
	if since := time.Since(failedAt); since < 60*time.Millisecond {
		t.Fatalf("retry arrived after %v, want >= retryDelay", since)
	}
	if retry.dir != "/data" {
		t.Fatalf("retry dir = %s", retry.dir)
	}
}

func TestFSWatchServiceUnsupportedExpiryIgnoresDroppedSubscriptions(t *testing.T) {
	watcher := newFakeWatcher()
	svc, _ := newTestFSWatchService(watcher)
	svc.unsupportedFor = 40 * time.Millisecond

	svc.SetSubscription("conn-1", "bot-1", []string{"/data"})
	run := watcher.next(t)
	run.errCh <- bridge.ErrWatchUnsupported
	svc.DropSubscription("conn-1")

	watcher.expectNone(t, 150*time.Millisecond)
}

func TestFSWatchServiceImmediateFailureRetriesWithoutStaleSignal(t *testing.T) {
	watcher := newFakeWatcher()
	svc, published := newTestFSWatchService(watcher)

	svc.SetSubscription("conn-1", "bot-1", []string{"/data"})
	run := watcher.next(t)
	// A watch that dies before it was ever established (e.g. the workspace is
	// stopped) missed nothing — no stale signal, just a retry.
	run.errCh <- errors.New("dial failed")

	select {
	case rec := <-published:
		t.Fatalf("unexpected publish %+v for never-established watch", rec)
	case <-time.After(60 * time.Millisecond):
	}
	retry := watcher.next(t)
	if retry.dir != "/data" {
		t.Fatalf("retry dir = %s", retry.dir)
	}
}

func TestFSWatchServiceRetriesFailedWatchAndSignalsStaleDir(t *testing.T) {
	watcher := newFakeWatcher()
	svc, published := newTestFSWatchService(watcher)
	svc.establishedAfter = 30 * time.Millisecond

	svc.SetSubscription("conn-1", "bot-1", []string{"/data"})
	run := watcher.next(t)
	time.Sleep(60 * time.Millisecond)
	run.errCh <- errors.New("stream broke")

	// The dying watch may have missed events anywhere under it: viewers get a
	// wildcard so the stale directory itself (not just its parent) reloads.
	select {
	case rec := <-published:
		if rec.botID != "bot-1" || rec.paths != nil {
			t.Fatalf("published = %+v, want wildcard", rec)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stale wildcard publish")
	}

	// And the watch is re-attempted while a subscription still wants it.
	retry := watcher.next(t)
	if retry.dir != "/data" {
		t.Fatalf("retry dir = %s", retry.dir)
	}
}
