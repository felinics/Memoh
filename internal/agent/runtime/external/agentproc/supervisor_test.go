package agentproc

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeProc struct {
	done   chan struct{}
	once   sync.Once
	closes atomic.Int32
	busy   atomic.Bool
	// teardown, when set, holds Close after the process is out of service,
	// as a real one releases what it held only once it has exited.
	teardown chan struct{}
}

func newFakeProc() *fakeProc { return &fakeProc{done: make(chan struct{})} }

func (p *fakeProc) Done() <-chan struct{} { return p.done }
func (p *fakeProc) Busy() bool            { return p.busy.Load() }
func (p *fakeProc) Close() error {
	p.closes.Add(1)
	p.exit()
	if p.teardown != nil {
		<-p.teardown
	}
	return nil
}
func (p *fakeProc) exit() { p.once.Do(func() { close(p.done) }) }

func (p *fakeProc) isClosed() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// starter counts starts and hands out a fresh fake process each time.
type starter struct {
	starts atomic.Int32
	gate   chan struct{} // when set, a start waits for it
	err    error
	// reports is the fingerprint a start says it started from.
	reports string

	mu    sync.Mutex
	procs []*fakeProc
}

func (st *starter) start(ctx context.Context) (Process, string, error) {
	st.starts.Add(1)
	if st.gate != nil {
		select {
		case <-st.gate:
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
	}
	if st.err != nil {
		return nil, "", st.err
	}
	p := newFakeProc()
	st.mu.Lock()
	st.procs = append(st.procs, p)
	st.mu.Unlock()
	return p, st.reports, nil
}

func newTestSupervisor(t *testing.T) *Supervisor {
	t.Helper()
	s := New(Options{ReconcileEvery: time.Hour})
	t.Cleanup(func() { s.Close(context.Background()) })
	return s
}

func lease(t *testing.T, s *Supervisor, fingerprint string, st *starter) (*fakeProc, func()) {
	t.Helper()
	proc, release, err := s.Lease(t.Context(), testKey, fingerprint, st.start)
	if err != nil {
		t.Fatal(err)
	}
	return proc.(*fakeProc), release
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestLeasesShareOneProcess(t *testing.T) {
	s := newTestSupervisor(t)
	st := &starter{}
	a, releaseA := lease(t, s, "fp", st)
	b, releaseB := lease(t, s, "fp", st)
	releaseA()
	releaseB()
	releaseB() // repeated release is harmless
	if a != b || st.starts.Load() != 1 || a.isClosed() {
		t.Fatalf("want one shared, open process; starts=%d", st.starts.Load())
	}
}

func TestConcurrentFirstLeasesStartOnceAndShareTheError(t *testing.T) {
	s := newTestSupervisor(t)
	st := &starter{gate: make(chan struct{}), err: errors.New("cli missing")}
	var wg sync.WaitGroup
	var entered atomic.Int32
	errs := make(chan error, 5)
	for range 5 {
		wg.Go(func() {
			entered.Add(1)
			_, _, err := s.Lease(t.Context(), testKey, "fp", st.start)
			errs <- err
		})
	}
	// A lease that arrives after the start failed rightly starts again, so
	// every lease must be waiting on the start before it ends.
	eventually(t, func() bool { return st.starts.Load() == 1 && entered.Load() == 5 })
	time.Sleep(50 * time.Millisecond)
	close(st.gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		if !errors.Is(err, st.err) {
			t.Fatalf("err = %v, want the start's error", err)
		}
	}
	if st.starts.Load() != 1 {
		t.Fatalf("starts = %d, want 1", st.starts.Load())
	}
}

func TestANewFingerprintRetiresTheProcessOnlyAfterItsLeases(t *testing.T) {
	s := newTestSupervisor(t)
	st := &starter{}
	old, releaseOld := lease(t, s, "v1", st)
	fresh, releaseFresh := lease(t, s, "v2", st)
	defer releaseFresh()
	if fresh == old || old.isClosed() {
		t.Fatal("a leased process was closed, or reused across fingerprints")
	}
	releaseOld()
	eventually(t, old.isClosed)
	if again, release := lease(t, s, "v2", st); again != fresh {
		t.Fatal("the retired process came back")
	} else {
		release()
	}
}

func TestAnExitedProcessIsReplacedOnTheNextLease(t *testing.T) {
	s := newTestSupervisor(t)
	st := &starter{}
	first, release := lease(t, s, "fp", st)
	release()
	first.exit()
	second, release := lease(t, s, "fp", st)
	release()
	if second == first || st.starts.Load() != 2 {
		t.Fatal("a dead process was handed out")
	}
}

func TestMarkBrokenRetiresOnlyThatProcess(t *testing.T) {
	s := newTestSupervisor(t)
	st := &starter{}
	wedged, release := lease(t, s, "fp", st)
	s.MarkBroken(testKey, newFakeProc()) // a stale handle changes nothing
	if again, r := lease(t, s, "fp", st); again != wedged {
		t.Fatal("a stale MarkBroken retired the current process")
	} else {
		r()
	}
	s.MarkBroken(testKey, wedged)
	next, r := lease(t, s, "fp", st)
	r()
	if next == wedged || wedged.isClosed() {
		t.Fatal("broken process still served, or closed under its lease")
	}
	release()
	eventually(t, wedged.isClosed)
}

func TestIdleProcessesCloseUnlessBusyOrLeased(t *testing.T) {
	s := newTestSupervisor(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	st := &starter{}
	proc, release := lease(t, s, "fp", st)
	now = now.Add(time.Hour)
	s.reconcile()
	if proc.isClosed() {
		t.Fatal("a leased process was closed for idleness")
	}
	release()
	proc.busy.Store(true)
	now = now.Add(time.Hour)
	s.reconcile()
	if proc.isClosed() {
		t.Fatal("a busy process was closed for idleness")
	}
	proc.busy.Store(false)
	s.reconcile()
	eventually(t, proc.isClosed)
}

func TestDrainRefusesWorkClosesEverythingAndHoldsStartsOff(t *testing.T) {
	s := newTestSupervisor(t)
	st := &starter{}
	proc, release := lease(t, s, "fp", st)
	release()
	proc.busy.Store(true)
	if _, err := s.Drain(t.Context(), isTestKey, func(p Process) bool { return p.(*fakeProc).Busy() }); !errors.Is(err, ErrBusy) {
		t.Fatalf("drain over a busy process: %v", err)
	}
	proc.busy.Store(false)
	unblock, err := s.Drain(t.Context(), isTestKey, func(p Process) bool { return p.(*fakeProc).Busy() })
	if err != nil || !proc.isClosed() {
		t.Fatalf("drain = %v, closed = %v", err, proc.isClosed())
	}
	leased := make(chan struct{})
	go func() {
		_, r, _ := s.Lease(context.Background(), testKey, "fp", st.start)
		r()
		close(leased)
	}()
	select {
	case <-leased:
		t.Fatal("a lease started a process while drained")
	case <-time.After(20 * time.Millisecond):
	}
	unblock()
	<-leased
}

func TestCloseTearsDownEveryGenerationAndRefusesLeases(t *testing.T) {
	s := New(Options{ReconcileEvery: time.Hour})
	st := &starter{}
	old, releaseOld := lease(t, s, "v1", st)
	current, releaseCurrent := lease(t, s, "v2", st)
	s.Close(context.Background())
	if !old.isClosed() || !current.isClosed() {
		t.Fatal("shutdown left a process running")
	}
	releaseOld()
	releaseCurrent()
	if _, _, err := s.Lease(t.Context(), testKey, "v2", st.start); !errors.Is(err, ErrClosed) {
		t.Fatalf("lease after close: %v", err)
	}
}

var testKey = Key{Runtime: "rt", BotID: "bot", BotAgentID: "agent"}

func isTestKey(k Key) bool { return k == testKey }

func TestDrainConversationsClosesOneConversationOrTheWholeBot(t *testing.T) {
	s := newTestSupervisor(t)
	start := func(key Key) *fakeProc {
		t.Helper()
		proc, release, err := s.Lease(t.Context(), key, "fp", (&starter{}).start)
		if err != nil {
			t.Fatal(err)
		}
		release()
		return proc.(*fakeProc)
	}
	shared := start(Key{Runtime: "codex", BotID: "bot", BotAgentID: "a"})
	first := start(Key{Runtime: "acp", BotID: "bot", BotAgentID: "a", Scope: "s1"})
	second := start(Key{Runtime: "claude", BotID: "bot", BotAgentID: "b", Scope: "s2"})
	other := start(Key{Runtime: "acp", BotID: "other", BotAgentID: "a", Scope: "s1"})

	unblock, err := s.DrainConversations(t.Context(), "bot", "s1")
	if err != nil {
		t.Fatal(err)
	}
	unblock()
	if !first.isClosed() || shared.isClosed() || second.isClosed() || other.isClosed() {
		t.Fatal("a conversation drain reached past its conversation")
	}
	unblock, err = s.DrainConversations(t.Context(), "bot", "")
	if err != nil {
		t.Fatal(err)
	}
	unblock()
	if !shared.isClosed() || !second.isClosed() || other.isClosed() {
		t.Fatal("a bot drain must close every runtime's processes of that bot, and only them")
	}
}
