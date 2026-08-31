package codex

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeResource struct {
	id     int
	done   chan struct{}
	closed atomic.Bool
}

func newFakeResource(id int) *fakeResource {
	return &fakeResource{id: id, done: make(chan struct{})}
}

func (f *fakeResource) Close() error {
	if f.closed.CompareAndSwap(false, true) {
		close(f.done)
	}
	return nil
}

func (f *fakeResource) Done() <-chan struct{} { return f.done }

type fakeStarter struct {
	mu      sync.Mutex
	next    int
	started []*fakeResource
	// block, when set, is closed by the test to let a startup finish.
	block chan struct{}
	err   error
}

func (s *fakeStarter) start(context.Context, string) (recyclable, error) {
	s.mu.Lock()
	block := s.block
	err := s.err
	s.next++
	resource := newFakeResource(s.next)
	if err == nil {
		s.started = append(s.started, resource)
	}
	s.mu.Unlock()
	if block != nil {
		<-block
	}
	if err != nil {
		return nil, err
	}
	return resource, nil
}

func TestServerTableSharesOneServerAcrossAcquires(t *testing.T) {
	starter := &fakeStarter{}
	table := newServerTable(starter.start, slog.Default())

	a, releaseA, err := table.acquire(context.Background(), "bot")
	if err != nil {
		t.Fatalf("acquire a: %v", err)
	}
	b, releaseB, err := table.acquire(context.Background(), "bot")
	if err != nil {
		t.Fatalf("acquire b: %v", err)
	}
	if a != b {
		t.Fatal("two acquires built two servers")
	}
	releaseA()
	releaseB()
	if a.(*fakeResource).closed.Load() {
		t.Fatal("release closed a server that was never recycled")
	}
}

// The core of the fix: recycling while a sibling holds the server must not
// kill it under the sibling — the server drains and dies at the last release,
// while a new acquire gets a fresh generation immediately.
func TestServerTableRecycleDrainsInsteadOfKilling(t *testing.T) {
	starter := &fakeStarter{}
	table := newServerTable(starter.start, slog.Default())

	old, releaseOld, err := table.acquire(context.Background(), "bot")
	if err != nil {
		t.Fatalf("acquire old: %v", err)
	}
	table.recycle("bot")
	if old.(*fakeResource).closed.Load() {
		t.Fatal("recycle killed a server a sibling still holds")
	}

	fresh, releaseFresh, err := table.acquire(context.Background(), "bot")
	if err != nil {
		t.Fatalf("acquire fresh: %v", err)
	}
	if fresh == old {
		t.Fatal("acquire after recycle returned the draining server")
	}
	releaseOld()
	if !old.(*fakeResource).closed.Load() {
		t.Fatal("last release did not close the drained server")
	}
	releaseFresh()
	if fresh.(*fakeResource).closed.Load() {
		t.Fatal("fresh generation closed without being recycled")
	}
}

func TestServerTableRecycleClosesIdleServerImmediately(t *testing.T) {
	starter := &fakeStarter{}
	table := newServerTable(starter.start, slog.Default())
	srv, release, err := table.acquire(context.Background(), "bot")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	release()
	table.recycle("bot")
	if !srv.(*fakeResource).closed.Load() {
		t.Fatal("recycle left an idle server running")
	}
}

// A server displaced while still starting must destroy itself and the
// acquire must rebuild from post-recycle state — the configuration-update
// escape hatch this table exists to close.
func TestServerTableDisplacesServerStillStarting(t *testing.T) {
	starter := &fakeStarter{block: make(chan struct{})}
	table := newServerTable(starter.start, slog.Default())

	got := make(chan recyclable, 1)
	go func() {
		srv, release, err := table.acquire(context.Background(), "bot")
		if err != nil {
			got <- nil
			return
		}
		defer release()
		got <- srv
	}()

	// Wait for the placeholder, then recycle while startup is blocked.
	deadline := time.Now().Add(2 * time.Second)
	for {
		table.mu.Lock()
		entry := table.entries["bot"]
		starting := entry != nil && entry.starting
		table.mu.Unlock()
		if starting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("startup placeholder never appeared")
		}
		time.Sleep(time.Millisecond)
	}
	table.recycle("bot")
	close(starter.block)

	srv := <-got
	if srv == nil {
		t.Fatal("acquire failed after displacement")
	}
	starter.mu.Lock()
	first := starter.started[0]
	total := len(starter.started)
	starter.mu.Unlock()
	if total < 2 {
		t.Fatalf("started %d servers, want the displaced one plus a rebuild", total)
	}
	if srv == first {
		t.Fatal("acquire returned the displaced pre-recycle server")
	}
	if !first.closed.Load() {
		t.Fatal("displaced server was not self-destroyed")
	}
}

func TestServerTableStartFailureIsReturnedAndClearsPlaceholder(t *testing.T) {
	starter := &fakeStarter{err: errors.New("bridge unavailable")}
	table := newServerTable(starter.start, slog.Default())
	if _, _, err := table.acquire(context.Background(), "bot"); err == nil {
		t.Fatal("acquire swallowed a startup failure")
	}
	starter.mu.Lock()
	starter.err = nil
	starter.mu.Unlock()
	if _, release, err := table.acquire(context.Background(), "bot"); err != nil {
		t.Fatalf("acquire after failure: %v", err)
	} else {
		release()
	}
}

func TestServerTableReapsExitedProcess(t *testing.T) {
	starter := &fakeStarter{}
	table := newServerTable(starter.start, slog.Default())
	srv, release, err := table.acquire(context.Background(), "bot")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	release()
	_ = srv.(*fakeResource).Close() // simulate the process dying on its own
	deadline := time.Now().Add(2 * time.Second)
	for {
		table.mu.Lock()
		_, present := table.entries["bot"]
		table.mu.Unlock()
		if !present {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("dead server was never reaped")
		}
		time.Sleep(time.Millisecond)
	}
	fresh, releaseFresh, err := table.acquire(context.Background(), "bot")
	if err != nil {
		t.Fatalf("acquire after death: %v", err)
	}
	if fresh == srv {
		t.Fatal("acquire returned a dead server")
	}
	releaseFresh()
}
