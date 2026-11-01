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
//
// A process generation follows the lifecycle model in AGENTS.md: starting,
// live, retired, closing, gone, one way, each transition in one function.
package agentproc

import (
	"context"
	"errors"
	"log/slog"
	"slices"
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
	slots   map[Key]*slot
	blocks  []*block
	closed  bool
	stop    chan struct{}
	stopped chan struct{}
}

type slot struct {
	key Key
	// current is the generation leases take, starting or live; gens holds
	// every generation of the key not yet gone, current included.
	current *gen
	gens    []*gen
}

// block is a drain holding the keys it matches off; done closes on unblock.
type block struct {
	match func(Key) bool
	done  chan struct{}
}

// genState is a generation's place in the lifecycle (AGENTS.md).
type genState int

const (
	genStarting genState = iota
	genLive
	genRetired
	genClosing
	genGone
)

// gen is one process generation of a key.
type gen struct {
	slot  *slot
	state genState
	proc  Process
	// requested is the fingerprint the start was asked for; fingerprint is
	// the one it reported starting from. A lease naming either matches.
	requested   string
	fingerprint string
	refs        int
	lastUsed    time.Time
	err         error
	// ready closes when the start has ended; gone when the generation is
	// gone: its process closed, or its start ended without one.
	ready chan struct{}
	gone  chan struct{}
	// closeAfterStart asks a generation taken out of service while starting
	// to close once its start ends.
	closeAfterStart bool
}

func (g *gen) matches(fingerprint string) bool {
	return fingerprint == g.requested || fingerprint == g.fingerprint
}

// serving reports a generation a lease may take: live and its process not
// out of service on its own.
func (g *gen) serving() bool {
	return g.state == genLive && alive(g.proc)
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
		slots:   map[Key]*slot{},
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	go s.loop()
	return s
}

// Lease returns the key's live process started from fingerprint, starting
// one when there is none, and a release the caller must call when done.
func (s *Supervisor) Lease(ctx context.Context, key Key, fingerprint string, start Starter) (Process, func(), error) {
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, nil, ErrClosed
		}
		if b := s.blockedLocked(key); b != nil {
			s.mu.Unlock()
			if err := waitFor(ctx, b.done); err != nil {
				return nil, nil, err
			}
			continue
		}
		sl := s.slotLocked(key)
		g := sl.current
		if g != nil && g.state == genStarting {
			s.mu.Unlock()
			if err := waitFor(ctx, g.ready); err != nil {
				return nil, nil, err
			}
			if g.err != nil && g.matches(fingerprint) {
				return nil, nil, g.err
			}
			continue
		}
		if g != nil && (!g.serving() || !g.matches(fingerprint)) {
			s.retireLocked(g)
			g = nil
		}
		if g != nil {
			g.refs++
			s.mu.Unlock()
			return g.proc, s.releaseFunc(g), nil
		}
		g = &gen{slot: sl, state: genStarting, requested: fingerprint, fingerprint: fingerprint, refs: 1, ready: make(chan struct{}), gone: make(chan struct{})}
		sl.current = g
		sl.gens = append(sl.gens, g)
		s.mu.Unlock()

		// The start outlives the caller's cancellation: others may be waiting.
		startCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.opts.StartTimeout)
		proc, started, err := start(startCtx)
		cancel()

		s.mu.Lock()
		served := s.startedLocked(g, proc, started, err)
		s.mu.Unlock()
		if err != nil {
			return nil, nil, err
		}
		if !served {
			continue
		}
		return proc, s.releaseFunc(g), nil
	}
}

// startedLocked ends g's start: it goes live and serves the lease that
// started it, or, retired or shut down meanwhile, closes and serves nobody.
func (s *Supervisor) startedLocked(g *gen, proc Process, started string, err error) bool {
	g.proc, g.err, g.lastUsed = proc, err, s.now()
	if started != "" {
		g.fingerprint = started
	}
	close(g.ready)
	if err != nil {
		g.refs = 0
		if g.slot.current == g {
			g.slot.current = nil
		}
		s.goneLocked(g)
		return false
	}
	g.state = genLive
	if s.closed || g.closeAfterStart || g.slot.current != g {
		g.refs = 0
		s.closeLocked(g)
		return false
	}
	return true
}

// Peek returns the key's current live process without leasing or starting
// one; callers may only read state the process already holds.
func (s *Supervisor) Peek(key Key) Process {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sl := s.slots[key]; sl != nil && sl.current != nil && sl.current.serving() {
		return sl.current.proc
	}
	return nil
}

// MarkBroken retires proc if it is still the key's current process.
func (s *Supervisor) MarkBroken(key Key, proc Process) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sl := s.slots[key]; sl != nil && sl.current != nil && sl.current.proc == proc {
		s.retireLocked(sl.current)
	}
}

// Key names one process a runtime keeps for a Bot Agent. Scope is empty for
// runtimes that share one process between the Agent's conversations and the
// session ID for those that run one per conversation.
type Key struct {
	Runtime    string
	BotID      string
	BotAgentID string
	Scope      string
}

// Retire retires the current process of every matching key; a leased one
// closes when its last lease is released.
func (s *Supervisor) Retire(match func(Key) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, sl := range s.slots {
		if sl.current != nil && match(key) {
			s.retireLocked(sl.current)
		}
	}
}

// Drain closes every process of the matching keys and holds new starts on
// them off until the returned unblock is called, so the caller can change
// what the next process starts from (delete a login file, clear a
// conversation). It refuses with ErrBusy when busy reports one of them at
// work, or another drain holds a matching key, and returns once they have
// exited. A nil busy closes processes at work too.
func (s *Supervisor) Drain(ctx context.Context, match func(Key) bool, busy func(Process) bool) (func(), error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	var gens []*gen
	for key, sl := range s.slots {
		if match(key) {
			gens = append(gens, sl.gens...)
		}
	}
	for key := range s.slots {
		if match(key) && s.blockedLocked(key) != nil {
			s.mu.Unlock()
			return nil, ErrBusy
		}
	}
	for _, g := range gens {
		if busy != nil && g.state != genStarting && g.state != genClosing && busy(g.proc) {
			s.mu.Unlock()
			return nil, ErrBusy
		}
	}
	b := &block{match: match, done: make(chan struct{})}
	s.blocks = append(s.blocks, b)
	for _, g := range gens {
		s.closeLocked(g)
	}
	s.mu.Unlock()
	unblock := sync.OnceFunc(func() {
		s.mu.Lock()
		s.blocks = slices.DeleteFunc(s.blocks, func(other *block) bool { return other == b })
		s.mu.Unlock()
		close(b.done)
	})
	for _, g := range gens {
		if err := waitFor(ctx, g.gone); err != nil {
			unblock()
			return nil, err
		}
	}
	return unblock, nil
}

// DrainConversations closes every process of the bot whose scope is
// conversation, or every process of the bot, of any runtime, when
// conversation is empty, and holds new ones off until the returned unblock.
// A process an Agent shares between its conversations stays for a single
// conversation: the conversation's state lives in what the reset clears, not
// in the process. It waits out another drain of the same processes.
func (s *Supervisor) DrainConversations(ctx context.Context, botID, conversation string) (func(), error) {
	match := func(k Key) bool {
		return k.BotID == botID && (conversation == "" || k.Scope == conversation)
	}
	for {
		unblock, err := s.Drain(ctx, match, nil)
		if !errors.Is(err, ErrBusy) {
			return unblock, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// blockedLocked returns the drain holding key off, if any.
func (s *Supervisor) blockedLocked(key Key) *block {
	for _, b := range s.blocks {
		if b.match(key) {
			return b
		}
	}
	return nil
}

// Close tears every process down at once and refuses later leases. It waits
// until every generation is gone, one still starting included, or ctx ends.
func (s *Supervisor) Close(ctx context.Context) {
	s.mu.Lock()
	var gens []*gen
	if !s.closed {
		s.closed = true
		close(s.stop)
	}
	for _, sl := range s.slots {
		for _, g := range slices.Clone(sl.gens) {
			s.closeLocked(g)
			gens = append(gens, g)
		}
	}
	s.mu.Unlock()
	for _, g := range gens {
		if waitFor(ctx, g.gone) != nil {
			break
		}
	}
	<-s.stopped
}

func (s *Supervisor) releaseFunc(g *gen) func() {
	return sync.OnceFunc(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		g.refs--
		g.lastUsed = s.now()
		if g.refs == 0 && g.state == genRetired {
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
		for _, g := range slices.Clone(sl.gens) {
			switch {
			case g.state == genRetired && (g.refs == 0 || !alive(g.proc)):
				s.closeLocked(g)
			case g.state == genLive && !alive(g.proc):
				s.closeLocked(g)
			case g.state == genLive && g == sl.current && s.idleLocked(g):
				idle = append(idle, g)
			}
		}
		if len(sl.gens) == 0 {
			delete(s.slots, key)
		}
	}
	s.mu.Unlock()
	for _, g := range idle {
		if b, ok := g.proc.(busyReporter); ok && b.Busy() {
			continue
		}
		s.mu.Lock()
		if g.state == genLive && g.slot.current == g && s.idleLocked(g) {
			s.opts.Logger.Info("closing idle agent process", slog.String("runtime", g.slot.key.Runtime), slog.String("bot_id", g.slot.key.BotID), slog.String("bot_agent_id", g.slot.key.BotAgentID), slog.String("scope", g.slot.key.Scope))
			s.closeLocked(g)
		}
		s.mu.Unlock()
	}
}

func (s *Supervisor) idleLocked(g *gen) bool {
	return g.refs == 0 && s.now().Sub(g.lastUsed) >= s.opts.IdleTTL
}

func (s *Supervisor) slotLocked(key Key) *slot {
	sl := s.slots[key]
	if sl == nil {
		sl = &slot{key: key}
		s.slots[key] = sl
	}
	return sl
}

// retireLocked takes g out of service: live with leases, it is retired and
// closes once its last lease is released; unleased, it closes now; still
// starting, it closes once its start ends.
func (s *Supervisor) retireLocked(g *gen) {
	if g.slot.current == g {
		g.slot.current = nil
	}
	switch g.state {
	case genStarting:
		g.closeAfterStart = true
	case genLive:
		if g.refs == 0 {
			s.closeLocked(g)
		} else {
			g.state = genRetired
		}
	}
}

// closeLocked tears g down: its process closes in the background, and g is
// gone once the close has returned. One still starting closes once its
// start ends.
func (s *Supervisor) closeLocked(g *gen) {
	if g.slot.current == g {
		g.slot.current = nil
	}
	switch g.state {
	case genStarting:
		g.closeAfterStart = true
		return
	case genClosing, genGone:
		return
	}
	g.state = genClosing
	proc := g.proc
	go func() {
		_ = proc.Close()
		s.mu.Lock()
		s.goneLocked(g)
		s.mu.Unlock()
	}()
}

// goneLocked ends g: nothing of it is left to wait for.
func (*Supervisor) goneLocked(g *gen) {
	g.state = genGone
	g.slot.gens = slices.DeleteFunc(g.slot.gens, func(other *gen) bool { return other == g })
	close(g.gone)
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
