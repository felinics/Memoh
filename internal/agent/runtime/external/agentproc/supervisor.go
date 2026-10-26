// Package agentproc owns the lifecycle of the long-lived agent CLI processes
// that external agent runtimes keep in a bot's workspace. Drivers only say
// how to start a process and which configuration it starts from; when a
// process is started, shared, replaced, closed for idleness, or torn down at
// shutdown is decided here, in one place.
//
// The rules:
//
//   - Every use leases the key's process and releases it when done. A leased
//     process is never closed for idleness.
//   - A lease names the fingerprint of the configuration it would start a
//     process from. A live process started from another one is retired: it
//     takes no new leases and closes when its last lease is released.
//   - One start runs per key at a time; everyone waiting on it gets its
//     process or its error.
//   - A process that has exited is replaced on the next lease. Dying is
//     normal: drivers resume their sessions on the new process.
//   - A reconcile loop closes idle and dead processes, so nothing depends on
//     an exit being noticed the moment it happens.
//   - Close tears every process down at once and refuses later leases.
package agentproc

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
)

// Process is a running agent CLI. Close must be safe to call more than once.
type Process interface {
	Done() <-chan struct{}
	Close() error
}

// busyReporter is implemented by a process that can be working while no lease
// covers it; idle closing leaves such a process alone.
type busyReporter interface {
	Busy() bool
}

// Starter starts a process. Its context bounds the start, not the process.
// It may return the fingerprint the process actually started from, when the
// start learned what the lease could not know (the CLI version the handshake
// reported); empty keeps the lease's.
type Starter func(ctx context.Context) (Process, string, error)

var (
	// ErrClosed answers a lease after Close.
	ErrClosed = errors.New("agent process supervisor is closed")
	// ErrBusy answers a drain while a process of the key is at work.
	ErrBusy = errors.New("agent process is in use")
)

// Options configures a Supervisor. Zero values take the defaults.
type Options struct {
	IdleTTL        time.Duration // close a process unleased this long
	StartTimeout   time.Duration // bound one start
	ReconcileEvery time.Duration
	Logger         *slog.Logger
}

// Supervisor holds every process of every key: the current one and those
// retired but still leased.
type Supervisor struct {
	opts Options
	now  func() time.Time

	mu      sync.Mutex
	slots   map[string]*slot
	closed  bool
	stop    chan struct{}
	stopped chan struct{}
	closing sync.WaitGroup
}

type slot struct {
	key     string
	current *gen
	retired []*gen
	// blocked is non-nil while a drain holds the key off; closed on unblock.
	blocked chan struct{}
}

// gen is one process generation of a key.
type gen struct {
	slot *slot
	proc Process
	// requested is the fingerprint the start was asked for; fingerprint is
	// the one it reported starting from. A lease naming either matches.
	requested   string
	fingerprint string
	refs        int
	lastUsed    time.Time
	ready       chan struct{} // closed when the start has finished
	err         error
	closed      bool
}

func (g *gen) matches(fingerprint string) bool {
	return fingerprint == g.requested || fingerprint == g.fingerprint
}

func (g *gen) starting() bool {
	select {
	case <-g.ready:
		return false
	default:
		return true
	}
}

// New returns a Supervisor whose reconcile loop runs until Close.
func New(opts Options) *Supervisor {
	if opts.IdleTTL <= 0 {
		opts.IdleTTL = 30 * time.Minute
	}
	if opts.StartTimeout <= 0 {
		opts.StartTimeout = 2 * time.Minute
	}
	if opts.ReconcileEvery <= 0 {
		opts.ReconcileEvery = time.Minute
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	s := &Supervisor{
		opts:    opts,
		now:     time.Now,
		slots:   map[string]*slot{},
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	go s.loop()
	return s
}

// Lease returns the key's live process started from fingerprint, starting
// one when there is none, and a release the caller must call when done.
func (s *Supervisor) Lease(ctx context.Context, key, fingerprint string, start Starter) (Process, func(), error) {
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, nil, ErrClosed
		}
		sl := s.slotLocked(key)
		if wait := sl.blocked; wait != nil {
			s.mu.Unlock()
			if err := waitFor(ctx, wait); err != nil {
				return nil, nil, err
			}
			continue
		}
		g := sl.current
		if g != nil && g.starting() {
			s.mu.Unlock()
			if err := waitFor(ctx, g.ready); err != nil {
				return nil, nil, err
			}
			if g.err != nil && g.matches(fingerprint) {
				return nil, nil, g.err
			}
			continue
		}
		if g != nil && (!alive(g.proc) || !g.matches(fingerprint)) {
			s.retireLocked(g)
			g = nil
		}
		if g != nil {
			g.refs++
			s.mu.Unlock()
			return g.proc, s.releaseFunc(g), nil
		}
		g = &gen{slot: sl, requested: fingerprint, fingerprint: fingerprint, refs: 1, ready: make(chan struct{})}
		sl.current = g
		s.mu.Unlock()

		// The start outlives the caller's cancellation: others may be waiting.
		startCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.opts.StartTimeout)
		proc, started, err := start(startCtx)
		cancel()

		s.mu.Lock()
		g.proc, g.err, g.lastUsed = proc, err, s.now()
		if started != "" {
			g.fingerprint = started
		}
		close(g.ready)
		if err != nil {
			g.refs = 0
			if sl.current == g {
				sl.current = nil
			}
			s.mu.Unlock()
			return nil, nil, err
		}
		if s.closed || sl.current != g {
			// Retired or shut down while it started: it serves nobody.
			g.refs = 0
			s.closeLocked(g)
			s.mu.Unlock()
			continue
		}
		s.mu.Unlock()
		return proc, s.releaseFunc(g), nil
	}
}

// Peek returns the key's current live process without leasing or starting
// one; callers may only read state the process already holds.
func (s *Supervisor) Peek(key string) Process {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sl := s.slots[key]; sl != nil && sl.current != nil && !sl.current.starting() && alive(sl.current.proc) {
		return sl.current.proc
	}
	return nil
}

// Processes returns every live process of the key: the current one and those
// retired but still leased. Callers may only read or close their own state on
// them (hand a session from an older process to a newer one).
func (s *Supervisor) Processes(key string) []Process {
	s.mu.Lock()
	defer s.mu.Unlock()
	sl := s.slots[key]
	if sl == nil {
		return nil
	}
	var procs []Process
	for _, g := range append(slices.Clone(sl.retired), sl.current) {
		if g != nil && !g.starting() && alive(g.proc) {
			procs = append(procs, g.proc)
		}
	}
	return procs
}

// MarkBroken retires proc if it is still the key's current process.
func (s *Supervisor) MarkBroken(key string, proc Process) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sl := s.slots[key]; sl != nil && sl.current != nil && sl.current.proc == proc {
		s.retireLocked(sl.current)
	}
}

// Key names the process a runtime keeps for a bot and a scope: a Bot Agent
// for runtimes that share one process between conversations, a session for
// those that run one per conversation.
func Key(runtime, botID, scope string) string {
	return runtime + "\x00" + botID + "\x00" + scope
}

// RetireBot retires every process of the bot, whatever its runtime.
func (s *Supervisor) RetireBot(botID string) {
	s.RetireWhere(func(key string) bool {
		parts := strings.SplitN(key, "\x00", 3)
		return len(parts) == 3 && parts[1] == botID
	})
}

// RetireWhere retires the current process of every matching key.
func (s *Supervisor) RetireWhere(match func(key string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, sl := range s.slots {
		if sl.current != nil && match(key) {
			s.retireLocked(sl.current)
		}
	}
}

// Drain closes every process of the key and holds new starts off until the
// returned unblock is called, so the caller can change what the next process
// starts from (delete a login file). It refuses with ErrBusy when busy
// reports a process of the key at work, and returns once they have exited.
func (s *Supervisor) Drain(ctx context.Context, key string, busy func(Process) bool) (func(), error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	sl := s.slotLocked(key)
	gens := append([]*gen{}, sl.retired...)
	if sl.current != nil {
		gens = append(gens, sl.current)
	}
	for _, g := range gens {
		if sl.blocked != nil || (!g.starting() && busy != nil && busy(g.proc)) {
			s.mu.Unlock()
			return nil, ErrBusy
		}
	}
	blocked := make(chan struct{})
	sl.blocked = blocked
	for _, g := range gens {
		if g.starting() {
			s.retireLocked(g)
		} else {
			s.closeLocked(g)
		}
	}
	s.mu.Unlock()
	unblock := sync.OnceFunc(func() {
		s.mu.Lock()
		sl.blocked = nil
		s.mu.Unlock()
		close(blocked)
	})
	for _, g := range gens {
		err := waitFor(ctx, g.ready)
		if err == nil && g.proc != nil {
			err = waitFor(ctx, g.proc.Done())
		}
		if err != nil {
			unblock()
			return nil, err
		}
	}
	return unblock, nil
}

// Close tears every process down at once and refuses later leases. It waits
// for the processes to close until ctx ends.
func (s *Supervisor) Close(ctx context.Context) {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.stop)
		for _, sl := range s.slots {
			for _, g := range append(slices.Clone(sl.retired), sl.current) {
				if g != nil && !g.starting() {
					s.closeLocked(g)
				}
			}
		}
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.closing.Wait()
		close(done)
	}()
	_ = waitFor(ctx, done)
	<-s.stopped
}

func (s *Supervisor) releaseFunc(g *gen) func() {
	return sync.OnceFunc(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		g.refs--
		g.lastUsed = s.now()
		if g.refs == 0 && g.slot.current != g {
			s.closeLocked(g)
		}
	})
}

func (s *Supervisor) loop() {
	defer close(s.stopped)
	ticker := time.NewTicker(s.opts.ReconcileEvery)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.reconcile()
		}
	}
}

// reconcile closes dead and idle processes. Busy is asked outside the lock:
// it belongs to the driver.
func (s *Supervisor) reconcile() {
	var idle []*gen
	s.mu.Lock()
	for key, sl := range s.slots {
		for _, g := range slices.Clone(sl.retired) {
			if g.refs == 0 || !alive(g.proc) {
				s.closeLocked(g)
			}
		}
		if g := sl.current; g != nil && !g.starting() {
			if !alive(g.proc) {
				s.closeLocked(g)
			} else if s.idleLocked(g) {
				idle = append(idle, g)
			}
		}
		if sl.current == nil && len(sl.retired) == 0 && sl.blocked == nil {
			delete(s.slots, key)
		}
	}
	s.mu.Unlock()
	for _, g := range idle {
		if b, ok := g.proc.(busyReporter); ok && b.Busy() {
			continue
		}
		s.mu.Lock()
		if g.slot.current == g && s.idleLocked(g) {
			s.opts.Logger.Info("closing idle agent process", slog.String("key", g.slot.key))
			s.closeLocked(g)
		}
		s.mu.Unlock()
	}
}

func (s *Supervisor) idleLocked(g *gen) bool {
	return g.refs == 0 && s.now().Sub(g.lastUsed) >= s.opts.IdleTTL
}

func (s *Supervisor) slotLocked(key string) *slot {
	sl := s.slots[key]
	if sl == nil {
		sl = &slot{key: key}
		s.slots[key] = sl
	}
	return sl
}

// retireLocked takes g out of service: no new leases, closed once unleased.
// A generation still starting closes when its start sees it is not current.
func (s *Supervisor) retireLocked(g *gen) {
	sl := g.slot
	if sl.current == g {
		sl.current = nil
	}
	if g.starting() {
		return
	}
	if g.refs == 0 {
		s.closeLocked(g)
		return
	}
	if !slices.Contains(sl.retired, g) {
		sl.retired = append(sl.retired, g)
	}
}

// closeLocked drops g from its slot and closes its process in the background.
func (s *Supervisor) closeLocked(g *gen) {
	sl := g.slot
	if sl.current == g {
		sl.current = nil
	}
	sl.retired = slices.DeleteFunc(sl.retired, func(other *gen) bool { return other == g })
	if g.closed || g.proc == nil {
		return
	}
	g.closed = true
	proc := g.proc
	s.closing.Add(1)
	go func() {
		defer s.closing.Done()
		_ = proc.Close()
	}()
}

func alive(proc Process) bool {
	if proc == nil {
		return false
	}
	select {
	case <-proc.Done():
		return false
	default:
		return true
	}
}

func waitFor(ctx context.Context, ch <-chan struct{}) error {
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
