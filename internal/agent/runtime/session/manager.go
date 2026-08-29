package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	"github.com/felinics/memoh/internal/agent/turn"
	chatview "github.com/felinics/memoh/internal/agent/view"
	"github.com/felinics/memoh/internal/runtimefence"
)

type Manager struct {
	// tuning is embedded so every derived timing reads as m.ownerLeaseTTL,
	// m.reaperTick and so on, with one construction site deriving them all.
	tuning

	backend     Backend
	distributed DistributedBackend
	// liveness answers "is an owner still alive", which the ledger cannot. Both
	// backends provide it; only a distributed backend has leases to expire.
	liveness LivenessBackend
	// runs is the durable ledger. A nil ledger means durable admission is not
	// wired yet and the manager keeps its pre-ledger behavior.
	runs ledger.Store
	// fence is the persistence-ownership cutover applied with each claim. It is
	// required whenever runs is set: a claim that skipped it would leave a
	// superseded owner able to keep writing history.
	fence FenceActivator
	// cluster records the declared topology so admission can refuse to run
	// multi-instance without a shared backend instead of silently splitting.
	cluster bool
	// reaper is this instance's candidate janitor. Every instance runs one; at
	// most one of them holds leadership at a time.
	reaper         *Reaper
	ownerID        string
	stateTTL       time.Duration
	commandAckTTL  time.Duration
	commandWorkers int
	logger         *slog.Logger
	newEpoch       func() string
	newGeneration  func() string

	mu                     sync.Mutex
	controls               map[runControlKey]*runControl
	commandHandler         func(context.Context, Command) error
	commandReconciler      func(context.Context, Command) (bool, error)
	decisionStore          DecisionStore
	terminalObserver       func(context.Context, TerminalRun)
	terminalReconciler     func(context.Context) error
	historyResetHandler    HistoryResetHandler
	pendingCommands        map[string]map[*commandWaiter]struct{}
	inflightCommandTargets map[string]struct{}
	commandExecutions      map[string]chan struct{}
	admittedCommands       map[string]struct{}
	localCommandResults    map[string]localCommandResult

	commandCancel       context.CancelFunc
	commandDone         chan struct{}
	subscriptionsMu     sync.Mutex
	subscriptionsClosed bool
	subscriptionsWG     sync.WaitGroup
	closeCh             chan struct{}
	closeOnce           sync.Once
	shutdownOnce        sync.Once
	shutdownDone        chan struct{}
	shutdownErr         error
}

type commandWaiter struct {
	result      chan error
	payloadHash string
}

type localCommandResult struct {
	result    Command
	expiresAt time.Time
}

type runControl struct {
	botID             string
	sessionID         string
	runID             string
	turnID            string
	generation        string
	fencingToken      int64
	abortCh           chan<- struct{}
	cancel            context.CancelFunc
	admissionCancel   context.CancelFunc
	lifecycleCtx      context.Context
	lifecycleCancel   context.CancelFunc
	injectCh          chan<- turn.InjectMessage
	injectMu          sync.Mutex
	injectStopped     bool
	converter         *chatview.UIMessageStreamConverter
	leaseStop         func()
	leaseDone         chan struct{}
	leaseLifecycleMu  sync.Mutex
	leaseMu           sync.RWMutex
	leaseValidUntil   time.Time
	leaseChanged      chan struct{}
	ready             chan struct{}
	readyOnce         sync.Once
	decisionMu        sync.Mutex
	decisionReady     chan struct{}
	decisionReadyOnce sync.Once
	abortStateMu      sync.Mutex
	claimEstablished  bool
	admissionComplete bool
	abortRequested    bool
	abortFinalizing   bool
	finishRetryOnce   sync.Once
	ownershipCancel   context.CancelCauseFunc
	ownershipOnce     sync.Once
	// ownershipLost records that ownership was revoked *with cause*, as opposed
	// to the ordinary teardown that also revokes. Only the former means this
	// process may no longer speak for the run, and the runner cannot tell the two
	// apart: both reach it as a cancelled context.
	ownershipLost atomic.Bool
}

type runControlKey struct {
	botID     string
	sessionID string
	runID     string
}

func scopedRunControlKey(botID, sessionID, runID string) runControlKey {
	return runControlKey{
		botID:     strings.TrimSpace(botID),
		sessionID: strings.TrimSpace(sessionID),
		runID:     strings.TrimSpace(runID),
	}
}

func (c *runControl) key() runControlKey {
	if c == nil {
		return runControlKey{}
	}
	return scopedRunControlKey(c.botID, c.sessionID, c.runID)
}

func (c *runControl) handle() RunHandle {
	if c == nil {
		return RunHandle{}
	}
	return RunHandle{BotID: c.botID, SessionID: c.sessionID, RunID: c.runID, TurnID: c.turnID, Generation: c.generation, FencingToken: c.fencingToken}
}

func (c *runControl) beginDecisionWait() {
	if c == nil {
		return
	}
	c.decisionMu.Lock()
	defer c.decisionMu.Unlock()
	c.decisionReady = make(chan struct{})
	c.decisionReadyOnce = sync.Once{}
}

func (c *runControl) markDecisionReady() {
	if c == nil {
		return
	}
	c.decisionMu.Lock()
	defer c.decisionMu.Unlock()
	if c.decisionReady == nil {
		c.decisionReady = make(chan struct{})
	}
	c.decisionReadyOnce.Do(func() { close(c.decisionReady) })
}

func (c *runControl) decisionReadySignal() <-chan struct{} {
	if c == nil {
		return nil
	}
	c.decisionMu.Lock()
	defer c.decisionMu.Unlock()
	return c.decisionReady
}

type Options struct {
	OwnerID       string
	StateTTL      time.Duration
	OwnerLeaseTTL time.Duration
	// BackendLossGrace comes from config because a Redis restart budget depends
	// on the deployment. Everything else below OwnerLeaseTTL is derived.
	BackendLossGrace time.Duration
	// Cluster mirrors session_runtime.cluster.
	Cluster bool
	// Ledger is the durable session run store.
	Ledger ledger.Store
	// Fence applies the persistence-ownership cutover for a claimed run. It is
	// required together with Ledger.
	Fence                  FenceActivator
	CommandAckTTL          time.Duration
	CommandWorkerLimit     int
	Logger                 *slog.Logger
	EpochGenerator         func() string
	RunGenerationGenerator func() string
	// ScanBatchSize and MaxScanBatchesPerTick exist so tests can watch recovery
	// page rather than to be tuned in production; both default to package
	// constants.
	ScanBatchSize         int32
	MaxScanBatchesPerTick int
}

const (
	defaultCommandAckTTL        = 2 * time.Second
	defaultCommandWorkerLimit   = 32
	defaultCommandRejectBacklog = 256
)

func NewManager(backend Backend, opts Options) *Manager {
	ownerID := strings.TrimSpace(opts.OwnerID)
	if ownerID == "" {
		ownerID = uuid.NewString()
	}
	stateTTL := opts.StateTTL
	if stateTTL <= 0 {
		stateTTL = 24 * time.Hour
	}
	leaseTTL := opts.OwnerLeaseTTL
	if leaseTTL <= 0 {
		leaseTTL = 30 * time.Second
	}
	commandAckTTL := opts.CommandAckTTL
	if commandAckTTL <= 0 {
		commandAckTTL = defaultCommandAckTTL
	}
	commandWorkers := opts.CommandWorkerLimit
	if commandWorkers <= 0 {
		commandWorkers = defaultCommandWorkerLimit
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	distributed, _ := backend.(DistributedBackend)
	liveness, _ := backend.(LivenessBackend)
	newEpoch := opts.EpochGenerator
	if newEpoch == nil {
		newEpoch = uuid.NewString
	}
	newGeneration := opts.RunGenerationGenerator
	if newGeneration == nil {
		newGeneration = uuid.NewString
	}
	return &Manager{
		tuning:                 newTuning(leaseTTL, opts.BackendLossGrace, opts.ScanBatchSize, opts.MaxScanBatchesPerTick),
		backend:                backend,
		distributed:            distributed,
		liveness:               liveness,
		runs:                   opts.Ledger,
		fence:                  opts.Fence,
		cluster:                opts.Cluster,
		ownerID:                ownerID,
		stateTTL:               stateTTL,
		commandAckTTL:          commandAckTTL,
		commandWorkers:         commandWorkers,
		logger:                 log.With(slog.String("component", "session_runtime")),
		newEpoch:               newEpoch,
		newGeneration:          newGeneration,
		controls:               make(map[runControlKey]*runControl),
		pendingCommands:        make(map[string]map[*commandWaiter]struct{}),
		inflightCommandTargets: make(map[string]struct{}),
		commandExecutions:      make(map[string]chan struct{}),
		admittedCommands:       make(map[string]struct{}),
		localCommandResults:    make(map[string]localCommandResult),
		closeCh:                make(chan struct{}),
		shutdownDone:           make(chan struct{}),
	}
}

// IsDistributed reports whether this manager coordinates owners through a
// cross-process backend. Memory managers intentionally return false.
func (m *Manager) IsDistributed() bool {
	return m != nil && m.distributed != nil
}

func (m *Manager) isClosed() bool {
	if m == nil || m.closeCh == nil {
		return false
	}
	select {
	case <-m.closeCh:
		return true
	default:
		return false
	}
}

// SetCommandHandler installs the owner-local executor for routed runtime
// commands whose domain behavior lives outside the sessionruntime package.
func (m *Manager) SetCommandHandler(handler func(context.Context, Command) error) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.commandHandler = handler
	m.mu.Unlock()
}

// SetCommandReconciler installs a read-only domain result checker. Unlike the
// owner-local command handler, it may run on any server after the owner or its
// local control disappears.
func (m *Manager) SetCommandReconciler(reconciler func(context.Context, Command) (bool, error)) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.commandReconciler = reconciler
	m.mu.Unlock()
}

// SetDecisionStore installs the PostgreSQL-backed decision authority used by
// every response transport and by waiting-decision recovery.
func (m *Manager) SetDecisionStore(store DecisionStore) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.decisionStore = store
	m.mu.Unlock()
}

// SetTerminalObserver installs the application-owned sink for authoritative
// durable run outcomes. The callback is invoked synchronously without holding
// the manager lock, and may be called more than once for the same run when a
// fenced terminal transition is replayed.
func (m *Manager) SetTerminalObserver(observer func(context.Context, TerminalRun)) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.terminalObserver = observer
	m.mu.Unlock()
}

// SetTerminalReconciler installs a bounded application-owned repair pass for
// terminal runs whose observation was interrupted after the ledger commit.
// The elected reaper invokes it once per tick after its own terminal duties.
func (m *Manager) SetTerminalReconciler(reconciler func(context.Context) error) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.terminalReconciler = reconciler
	m.mu.Unlock()
}

func (m *Manager) observeTerminalRun(ctx context.Context, run TerminalRun) {
	if m == nil || run.RunID == "" {
		return
	}
	m.mu.Lock()
	observer := m.terminalObserver
	m.mu.Unlock()
	if observer != nil {
		observer(context.WithoutCancel(ctx), run)
	}
}

func (m *Manager) reconcileTerminalRuns(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	reconciler := m.terminalReconciler
	m.mu.Unlock()
	if reconciler == nil {
		return nil
	}
	return reconciler(context.WithoutCancel(ctx))
}

func (m *Manager) Start(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if m.isClosed() {
		return ErrManagerClosed
	}
	if checker, ok := m.distributed.(startupHealthChecker); ok {
		if err := checker.CheckHealth(ctx); err != nil {
			return err
		}
	}
	// The reaper runs on both backends. A single memory instance still needs the
	// generation sweep — its previous process owned runs that no live backend
	// remembers — and still needs orphan repair.
	if err := m.startReaper(ctx); err != nil {
		return err
	}
	if m.distributed == nil {
		return nil
	}
	commandCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stopStartupCancellation := context.AfterFunc(ctx, cancel)
	sub, err := m.distributed.SubscribeCommands(commandCtx, m.ownerID)
	if err != nil {
		stopStartupCancellation()
		cancel()
		if m.isClosed() {
			return ErrManagerClosed
		}
		if startupErr := ctx.Err(); startupErr != nil {
			return startupErr
		}
		return err
	}
	stopCommands := sync.OnceFunc(func() {
		cancel()
		sub.Close()
	})
	if !stopStartupCancellation() {
		stopCommands()
		if err := ctx.Err(); err != nil {
			return err
		}
		return context.Canceled
	}
	commandDone := make(chan struct{})
	m.mu.Lock()
	if m.isClosed() {
		m.mu.Unlock()
		stopCommands()
		return ErrManagerClosed
	}
	if m.commandCancel != nil || m.commandDone != nil {
		m.mu.Unlock()
		stopCommands()
		return errors.New("session runtime manager is already started")
	}
	m.commandCancel = stopCommands
	m.commandDone = commandDone
	m.mu.Unlock()
	go func() {
		jobs := make(chan Command, m.commandWorkers)
		rejected := make(chan Command, defaultCommandRejectBacklog)
		var workers sync.WaitGroup
		for range m.commandWorkers {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for cmd := range jobs {
					m.applyCommand(commandCtx, cmd)
					m.releaseCommandAdmission(cmd)
				}
			}()
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			for cmd := range rejected {
				m.publishCommandResult(commandCtx, cmd, ErrCommandBusy)
				m.releaseCommandAdmission(cmd)
			}
		}()
		defer func() {
			close(jobs)
			close(rejected)
			workers.Wait()
			close(commandDone)
		}()
		for {
			select {
			case <-commandCtx.Done():
				return
			case cmd, ok := <-sub.C:
				if !ok {
					return
				}
				if strings.TrimSpace(cmd.Type) == CommandResult {
					m.applyCommand(commandCtx, cmd)
					continue
				}
				if !m.admitCommand(cmd) {
					continue
				}
				select {
				case jobs <- cmd:
				default:
					select {
					case rejected <- cmd:
					case <-commandCtx.Done():
						m.releaseCommandAdmission(cmd)
						return
					}
				}
			}
		}
	}()
	return nil
}

// startReaper is a no-op without a ledger: there is nothing durable to reap, so
// a manager wired for live state only keeps its pre-ledger behavior.
func (m *Manager) startReaper(ctx context.Context) error {
	if m.runs == nil || m.liveness == nil {
		return nil
	}
	reaper := NewReaper(m.runs, m.liveness, m.tuning, m.ownerID, m.logger)
	reaper.SetWaitingDecisionRecoverer(m.recoverWaitingDecision)
	reaper.SetTerminalObserver(m.observeTerminalRun)
	reaper.SetTerminalReconciler(m.reconcileTerminalRuns)
	if err := reaper.Start(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	m.reaper = reaper
	m.mu.Unlock()
	return nil
}

func (m *Manager) Close() error {
	return m.CloseContext(context.Background())
}

func (m *Manager) CloseContext(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if m.closeCh != nil {
		m.closeOnce.Do(func() { close(m.closeCh) })
	}
	shutdownCtx := context.WithoutCancel(ctx)
	m.shutdownOnce.Do(func() {
		go func() {
			m.shutdownErr = m.shutdown(shutdownCtx)
			close(m.shutdownDone)
		}()
	})
	select {
	case <-m.shutdownDone:
		return m.shutdownErr
	default:
	}
	select {
	case <-m.shutdownDone:
		return m.shutdownErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) shutdown(ctx context.Context) error {
	m.mu.Lock()
	commandCancel := m.commandCancel
	commandDone := m.commandDone
	reaper := m.reaper
	m.commandCancel = nil
	m.commandDone = nil
	m.reaper = nil
	pendingCommands := make([]*commandWaiter, 0, len(m.pendingCommands))
	for commandID, waiters := range m.pendingCommands {
		for pending := range waiters {
			pendingCommands = append(pendingCommands, pending)
		}
		delete(m.pendingCommands, commandID)
	}
	m.mu.Unlock()
	if commandCancel != nil {
		commandCancel()
	}
	for _, pending := range pendingCommands {
		pending.result <- ErrManagerClosed
	}
	releaseErr := m.releaseAllLocalRuns(ctx)
	controlErr := m.stopAllLocalControls(ctx)
	if commandDone != nil {
		<-commandDone
	}
	// Hand leadership back before the backend closes, so a peer picks it up on
	// its next tick instead of waiting out the lease.
	var reaperErr error
	if reaper != nil {
		reaperErr = reaper.Close(ctx)
	}
	m.subscriptionsMu.Lock()
	m.subscriptionsClosed = true
	m.subscriptionsMu.Unlock()
	var backendErr error
	if m.backend != nil {
		backendErr = m.backend.Close()
	}
	m.subscriptionsWG.Wait()
	return errors.Join(releaseErr, controlErr, reaperErr, backendErr)
}

func (m *Manager) StartRun(ctx context.Context, botID, sessionID, runID string, abortCh chan<- struct{}, cancel context.CancelFunc, injectCh chan<- turn.InjectMessage) error {
	_, err := m.StartRunHandle(ctx, botID, sessionID, runID, abortCh, cancel, injectCh)
	return err
}

func (m *Manager) StartRunHandle(ctx context.Context, botID, sessionID, runID string, abortCh chan<- struct{}, cancel context.CancelFunc, injectCh chan<- turn.InjectMessage) (RunHandle, error) {
	return m.StartRunWithAdmissionBuilderHandle(ctx, botID, sessionID, runID, func(context.Context, RunHandle) (RunAdmissionView, error) {
		return RunAdmissionView{}, nil
	}, abortCh, cancel, injectCh)
}

// StartRunWithAdmissionBuilderHandle reserves the cross-server run before
// executing builder, then publishes the running view only after the canonical
// request turn and optional replacement operation are ready.
func (m *Manager) StartRunWithAdmissionBuilderHandle(ctx context.Context, botID, sessionID, runID string, builder func(context.Context, RunHandle) (RunAdmissionView, error), abortCh chan<- struct{}, cancel context.CancelFunc, injectCh chan<- turn.InjectMessage) (RunHandle, error) {
	handle, _, err := m.startRun(ctx, runStart{
		botID:     botID,
		sessionID: sessionID,
		runID:     runID,
		builder:   builder,
		abortCh:   abortCh,
		cancel:    cancel,
		injectCh:  injectCh,
	})
	return handle, err
}

func (m *Manager) StartRunWithAdmissionBuilderAndOwnershipHandle(ctx context.Context, botID, sessionID, runID string, builder func(context.Context, RunHandle) (RunAdmissionView, error), ownershipCancel context.CancelCauseFunc, abortCh chan<- struct{}, cancel context.CancelFunc, injectCh chan<- turn.InjectMessage) (RunHandle, error) {
	handle, _, err := m.startRun(ctx, runStart{
		botID:           botID,
		sessionID:       sessionID,
		runID:           runID,
		builder:         builder,
		ownershipCancel: ownershipCancel,
		abortCh:         abortCh,
		cancel:          cancel,
		injectCh:        injectCh,
	})
	return handle, err
}

// runStart is one live reservation request. It is a struct rather than a
// parameter list because the ledger path and the pre-ledger entry points differ
// only in whether they carry a fencing token, and that difference should be
// visible at the call site instead of being a positional zero.
type runStart struct {
	botID     string
	sessionID string
	runID     string
	// fencingToken is the durable ownership token from the ledger claim. Zero
	// means this reservation has no ledger row, so it gets no lease index entry
	// either: there would be nothing for the reaper to transition.
	fencingToken int64
	// turnID is the durable turn this run writes into. It reaches the published
	// run view so every subscriber can line the run up against history, not just
	// the caller that admitted it.
	turnID string
	// invocationID is the caller's intent identity, threaded from admission into
	// the published run view; see CurrentRunView.InvocationID.
	invocationID    string
	builder         func(context.Context, RunHandle) (RunAdmissionView, error)
	ownershipCancel context.CancelCauseFunc
	abortCh         chan<- struct{}
	cancel          context.CancelFunc
	injectCh        chan<- turn.InjectMessage
}

// startRun reserves the session's single live slot and returns the cursor the
// reservation landed on, so a caller can tell a client where in the session's
// event stream its run became observable. The cursor is returned rather than
// read back afterwards because it is only exact at the moment of the activating
// write: the run publishes deltas from then on, and any later read would report
// a position past the one this reservation created.
func (m *Manager) startRun(ctx context.Context, start runStart) (RunHandle, Cursor, error) {
	botID := start.botID
	sessionID := start.sessionID
	runID := start.runID
	builder := start.builder
	ownershipCancel := start.ownershipCancel
	abortCh := start.abortCh
	cancel := start.cancel
	injectCh := start.injectCh
	if m == nil || m.backend == nil {
		if ownershipCancel != nil {
			ownershipCancel(ErrRunOwnershipLost)
		}
		return RunHandle{}, Cursor{}, nil
	}
	botID = strings.TrimSpace(botID)
	sessionID = strings.TrimSpace(sessionID)
	runID = strings.TrimSpace(runID)
	if botID == "" || sessionID == "" || runID == "" {
		if ownershipCancel != nil {
			ownershipCancel(ErrRunOwnershipLost)
		}
		return RunHandle{}, Cursor{}, errors.New("bot_id, session_id, and run_id are required")
	}
	if builder == nil {
		if ownershipCancel != nil {
			ownershipCancel(ErrRunOwnershipLost)
		}
		return RunHandle{}, Cursor{}, errors.New("runtime admission builder is required")
	}
	if err := ctx.Err(); err != nil {
		if ownershipCancel != nil {
			ownershipCancel(ErrRunOwnershipLost)
		}
		return RunHandle{}, Cursor{}, err
	}

	admissionCtx, admissionCancel := context.WithCancel(ctx)
	defer admissionCancel()
	ctx = admissionCtx

	runGeneration := m.newGeneration()
	handle := RunHandle{BotID: botID, SessionID: sessionID, RunID: runID, TurnID: start.turnID, Generation: runGeneration, FencingToken: start.fencingToken}
	if handle.FencingToken > 0 {
		ctx = runtimefence.WithContext(ctx, runtimefence.Fence{
			BotID:     handle.BotID,
			SessionID: handle.SessionID,
			Token:     handle.FencingToken,
		})
	}
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.WithoutCancel(ctx))
	ctrl := &runControl{
		botID:           botID,
		sessionID:       sessionID,
		runID:           runID,
		turnID:          start.turnID,
		generation:      runGeneration,
		fencingToken:    start.fencingToken,
		abortCh:         abortCh,
		cancel:          cancel,
		admissionCancel: admissionCancel,
		lifecycleCtx:    lifecycleCtx,
		lifecycleCancel: lifecycleCancel,
		injectCh:        injectCh,
		converter:       chatview.NewUIMessageStreamConverter(),
		leaseChanged:    make(chan struct{}, 1),
		ready:           make(chan struct{}),
		ownershipCancel: ownershipCancel,
	}
	defer ctrl.markReady()
	m.mu.Lock()
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		ctrl.revokeOwnership(ErrRunOwnershipLost)
		return RunHandle{}, Cursor{}, err
	}
	select {
	case <-m.closeCh:
		m.mu.Unlock()
		ctrl.revokeOwnership(ErrRunOwnershipLost)
		return RunHandle{}, Cursor{}, ErrManagerClosed
	default:
	}
	controlKey := scopedRunControlKey(botID, sessionID, runID)
	if _, exists := m.controls[controlKey]; exists {
		m.mu.Unlock()
		ctrl.revokeOwnership(ErrRunOwnershipLost)
		return RunHandle{}, Cursor{}, fmt.Errorf("run_id %q is already owned by this runtime manager", runID)
	}
	m.controls[controlKey] = ctrl
	m.mu.Unlock()

	localLeaseStarted := time.Now()
	now, err := m.backend.Now(ctx)
	if err != nil {
		m.removeLocalControl(runID, ctrl)
		return RunHandle{}, Cursor{}, fmt.Errorf("load runtime backend time: %w", err)
	}
	key := Key{BotID: botID, SessionID: sessionID}
	ownerID := ""
	var leaseExpiresAt *time.Time
	if m.distributed != nil {
		ownerID = m.ownerID
		expiresAt := now.Add(m.ownerLeaseTTL)
		leaseExpiresAt = &expiresAt
		// The backend claim below is authoritative, but a local deadline must
		// exist before it returns so an abort can cancel blocked admission.
		ctrl.setLeaseValidUntil(localLeaseStarted.Add(m.ownerLeaseTTL))
	}
	epoch := m.newEpoch()
	var expiredRef RunRef
	claim := func(snapshot Snapshot, _ bool) (Snapshot, bool, error) {
		if snapshot.CurrentRunView != nil && isActiveRunStatus(snapshot.CurrentRunView.Status) && snapshot.CurrentRunView.RunID != runID {
			if m.distributed == nil || !m.markLostIfExpired(&snapshot, now) {
				return snapshot, false, fmt.Errorf("session %q already has an active runtime run", sessionID)
			}
			expiredRef = runRefForRun(snapshot.BotID, snapshot.SessionID, snapshot.CurrentRunView)
		}
		snapshot.BotID = botID
		snapshot.SessionID = sessionID
		if strings.TrimSpace(snapshot.Epoch) == "" {
			snapshot.Epoch = epoch
		}
		snapshot.Seq++
		snapshot.UpdatedAt = now
		snapshot.CurrentRunView = &CurrentRunView{
			RunID:               runID,
			TurnID:              start.turnID,
			InvocationID:        start.invocationID,
			Generation:          runGeneration,
			Status:              RunStatusAdmitting,
			OwnerID:             ownerID,
			OwnerLeaseExpiresAt: leaseExpiresAt,
			StartedAt:           now,
			UpdatedAt:           now,
			Messages:            []chatview.UIMessage{},
		}
		return snapshot, true, nil
	}
	var claimedSnapshot Snapshot
	var changed bool
	if m.distributed != nil {
		claimedSnapshot, changed, err = m.distributed.StartRun(ctx, key, RunRef{
			BotID: botID, SessionID: sessionID, RunID: runID, OwnerID: m.ownerID, Generation: runGeneration, FencingToken: start.fencingToken,
		}, claim)
	} else if gated, ok := m.backend.(historyResetStartBackend); ok {
		claimedSnapshot, changed, err = gated.StartRunIfNoHistoryReset(ctx, key, claim)
	} else {
		claimedSnapshot, changed, err = m.backend.Update(ctx, key, claim)
	}
	if err != nil {
		if ctrl.abortWasRequested() {
			reconcileErr := m.reconcileCanceledRunClaim(context.WithoutCancel(ctx), ctrl)
			if reconcileErr == nil {
				return RunHandle{}, Cursor{}, context.Canceled
			}
			m.removeLocalControl(runID, ctrl)
			return RunHandle{}, Cursor{}, errors.Join(err, reconcileErr)
		}
		m.removeLocalControl(runID, ctrl)
		return RunHandle{}, Cursor{}, err
	}
	if !changed {
		m.removeLocalControl(runID, ctrl)
		return RunHandle{}, Cursor{}, nil
	}
	abortRequested, abortOwner := ctrl.establishClaimForAbort()
	if abortRequested {
		if abortOwner {
			abortErr := m.abortClaimedAdmission(context.WithoutCancel(ctx), ctrl)
			if abortErr != nil {
				return RunHandle{}, Cursor{}, abortErr
			}
		}
		return RunHandle{}, Cursor{}, context.Canceled
	}
	if err := m.publishRuntimeDelta(context.WithoutCancel(ctx), claimedSnapshot, runID, RuntimeDelta{CurrentRunView: claimedSnapshot.CurrentRunView}); err != nil {
		m.logger.Warn("publish admitting runtime checkpoint failed; subscribers will reconcile from snapshot", slog.Any("error", err), slog.String("run_id", runID))
	}
	if expiredRef.RunID != "" && m.distributed != nil {
		_, _ = m.distributed.DeleteRunRef(context.WithoutCancel(ctx), expiredRef)
	}
	if m.distributed != nil {
		localRenewalStarted := time.Now()
		confirmCtx, confirmCancel := context.WithDeadline(context.WithoutCancel(ctx), ctrl.leaseDeadline())
		renewedAt, renewErr := m.backend.Now(confirmCtx)
		if renewErr == nil {
			renewErr = m.distributed.RenewLease(confirmCtx, key, runID, m.ownerID, runGeneration, renewedAt, renewedAt.Add(m.ownerLeaseTTL))
		}
		confirmCancel()
		if renewErr != nil {
			ctrl.markReady()
			if m.isClosed() {
				m.removeLocalControl(runID, ctrl)
				return RunHandle{}, Cursor{}, ErrManagerClosed
			}
			if errors.Is(renewErr, ErrRunOwnershipLost) || !ctrl.leaseIsValidAt(time.Now()) {
				m.removeLocalControl(runID, ctrl)
				renewErr = ErrRunOwnershipLost
			} else {
				_ = m.FinishRun(context.WithoutCancel(ctx), handle, RunStatusErrored, renewErr.Error())
			}
			return RunHandle{}, Cursor{}, fmt.Errorf("confirm runtime owner lease: %w", renewErr)
		}
		deadline := localRenewalStarted.Add(m.ownerLeaseTTL)
		if !time.Now().Before(deadline) {
			ctrl.markReady()
			m.removeLocalControl(runID, ctrl)
			if m.isClosed() {
				return RunHandle{}, Cursor{}, ErrManagerClosed
			}
			return RunHandle{}, Cursor{}, fmt.Errorf("confirm runtime owner lease: %w", ErrRunOwnershipLost)
		}
		ctrl.setLeaseValidUntil(deadline)
		m.startLeaseRenewal(context.WithoutCancel(ctx), ctrl)
	}
	admission, err := builder(ctx, handle)
	if err != nil {
		ctrl.markReady()
		status := RunStatusErrored
		message := err.Error()
		if errors.Is(err, context.Canceled) {
			status = RunStatusAborted
			message = ""
		}
		_ = m.FinishRun(context.WithoutCancel(ctx), handle, status, message)
		return RunHandle{}, Cursor{}, err
	}
	if m.isClosed() {
		return RunHandle{}, Cursor{}, ErrManagerClosed
	}
	if m.localControlForHandle(handle) != ctrl {
		return RunHandle{}, Cursor{}, ErrRunOwnershipLost
	}
	admission, err = normalizeRunAdmission(admission)
	if err != nil {
		ctrl.markReady()
		_ = m.FinishRun(context.WithoutCancel(ctx), handle, RunStatusErrored, err.Error())
		return RunHandle{}, Cursor{}, err
	}
	activated, changed, err := m.updateActiveAndPublish(context.WithoutCancel(ctx), handle, func(snapshot Snapshot, now time.Time) (Snapshot, bool, error) {
		if m.isClosed() {
			return snapshot, false, ErrManagerClosed
		}
		if m.localControlForHandle(handle) != ctrl {
			return snapshot, false, ErrRunOwnershipLost
		}
		run := snapshot.CurrentRunView
		if run.RunID == runID && m.runOwnerMatches(run) && strings.EqualFold(run.Status, RunStatusAborting) {
			return snapshot, false, context.Canceled
		}
		if run.RunID != runID || !m.runOwnerMatches(run) || !strings.EqualFold(run.Status, RunStatusAdmitting) {
			return snapshot, false, errors.New("reserved runtime run is no longer owned by this manager")
		}
		snapshot.Seq++
		snapshot.UpdatedAt = now
		run.Status = RunStatusRunning
		run.RequestUserTurn = admission.RequestUserTurn
		run.Operation = admission.Operation
		run.UpdatedAt = now
		return snapshot, true, nil
	}, func(snapshot Snapshot) RuntimeDelta {
		return RuntimeDelta{CurrentRunView: snapshot.CurrentRunView}
	})
	if err != nil || !changed {
		ctrl.markReady()
		if errors.Is(err, ErrManagerClosed) || errors.Is(err, ErrRunOwnershipLost) {
			return RunHandle{}, Cursor{}, err
		}
		if err == nil {
			err = errors.New("reserved runtime run could not be activated")
		}
		status := RunStatusErrored
		message := err.Error()
		if errors.Is(err, context.Canceled) {
			status = RunStatusAborted
			message = ""
		}
		_ = m.FinishRun(context.WithoutCancel(ctx), handle, status, message)
		return RunHandle{}, Cursor{}, err
	}
	if !ctrl.completeAdmissionForAbort() {
		return RunHandle{}, Cursor{}, context.Canceled
	}
	ctrl.markReady()
	return handle, activated.cursor(), nil
}

func (m *Manager) reconcileCanceledRunClaim(ctx context.Context, ctrl *runControl) error {
	if ctrl == nil {
		return nil
	}
	snapshot, ok, err := m.backend.Load(ctx, Key{BotID: ctrl.botID, SessionID: ctrl.sessionID})
	if err != nil {
		return fmt.Errorf("reconcile canceled runtime claim: %w", err)
	}
	if !ok || !runMatchesHandle(snapshot.CurrentRunView, ctrl.handle()) {
		m.removeLocalControl(ctrl.runID, ctrl)
		return nil
	}
	if !isActiveRunStatus(snapshot.CurrentRunView.Status) {
		m.cleanupFinishedRun(ctx, ctrl.handle())
		return nil
	}
	if ctrl.claimVisibleAndTakeAbort() {
		return m.abortClaimedAdmission(ctx, ctrl)
	}
	return nil
}

func (m *Manager) FinishRun(ctx context.Context, handle RunHandle, status, message string) error {
	return m.finishRun(ctx, handle, status, "", message)
}

// FinishRunWithErrorCode records a stable public failure code without treating
// that code as a display message or persisting private provider diagnostics.
func (m *Manager) FinishRunWithErrorCode(ctx context.Context, handle RunHandle, status, errorCode string) error {
	return m.finishRun(ctx, handle, status, errorCode, "")
}

func (m *Manager) finishRun(ctx context.Context, handle RunHandle, status, errorCode, message string) error {
	if m == nil || m.backend == nil {
		return nil
	}
	handle = handle.normalized()
	if !handle.valid() {
		return ErrRunOwnershipLost
	}
	ctrl := m.localControlForHandle(handle)
	if ctrl != nil && handle.FencingToken <= 0 {
		handle.FencingToken = ctrl.fencingToken
	}
	if strings.TrimSpace(status) == "" && strings.TrimSpace(errorCode) == "" && strings.TrimSpace(message) == "" {
		snapshot, ok, err := m.backend.Load(ctx, handle.key())
		if err != nil {
			return err
		}
		if ok && runMatchesHandle(snapshot.CurrentRunView, handle) &&
			strings.EqualFold(snapshot.CurrentRunView.Status, RunStatusWaitingDecision) {
			// The native stream ends after emitting a deferred decision. That is
			// a parked execution, not a terminal run: retain ownership and the
			// command executor so the response can resume this same run.
			if ctrl != nil {
				ctrl.markDecisionReady()
			}
			return nil
		}
	}
	if ctrl != nil {
		ctrl.stopCommands()
		if ctrl.ownershipWasLost() {
			// Ownership was revoked with cause while the run was executing, so the
			// runner's return describes a cancelled execution, not an outcome. It
			// arrives as a clean stop and would otherwise be stamped `completed`.
			//
			// Failing closed here is what keeps the run reapable. The fencing token
			// is still nominally valid, so a terminal write would land, and once the
			// row is terminal the reaper's transition is a no-op it reports as
			// "nothing to decide" — the run would be durably successful precisely
			// because the process that could not finish it said so last.
			m.forgetLocalControlForHandle(context.WithoutCancel(ctx), handle)
			return ErrRunOwnershipLost
		}
	}
	errorCode = strings.TrimSpace(errorCode)
	finishMessage := strings.TrimSpace(message)
	status = m.resolveTerminalStatus(ctx, handle, status, errorCode, finishMessage)
	if errorCode == "" && strings.EqualFold(status, RunStatusErrored) {
		if snapshot, ok, loadErr := m.backend.Load(ctx, handle.key()); loadErr == nil && ok && runMatchesHandle(snapshot.CurrentRunView, handle) {
			errorCode = strings.TrimSpace(snapshot.CurrentRunView.ErrorCode)
		}
	}
	terminal, err := m.finalizeLedgerRun(ctx, handle, status, errorCode, finishMessage)
	if terminal.RunID != "" {
		defer m.observeTerminalRun(ctx, terminal)
	}
	if err != nil {
		if errors.Is(err, ErrRunOwnershipLost) && terminal.RunID != "" {
			// A newer fence already made this run terminal. This owner cannot
			// release shared state, but it must stop its stale local control and
			// lease renewal before the authoritative outcome is observed.
			m.forgetLocalControlForHandle(context.WithoutCancel(ctx), handle)
		}
		// The lease is deliberately left alone: it is the only pointer the
		// reaper has to this run, and the durable row still says the run is
		// active. Renewal has already stopped, so expiry brings the reaper. The
		// terminal newer-fence case above is the exception: no reaping remains.
		return err
	}
	changed, err := m.finishRunState(ctx, handle, status, errorCode, finishMessage)
	if err == nil || changed {
		m.cleanupFinishedRun(context.WithoutCancel(ctx), handle)
		return err
	}
	if errors.Is(err, ErrRunOwnershipLost) {
		snapshot, ok, loadErr := m.backend.Load(context.WithoutCancel(ctx), handle.key())
		if loadErr == nil && ok && runMatchesHandle(snapshot.CurrentRunView, handle) && !isActiveRunStatus(snapshot.CurrentRunView.Status) {
			m.cleanupFinishedRun(context.WithoutCancel(ctx), handle)
			return nil
		}
		m.forgetLocalControlForHandle(context.WithoutCancel(ctx), handle)
		return err
	}
	if ctrl != nil && m.localControlForHandle(handle) == ctrl {
		retryCtx := context.WithoutCancel(ctx)
		ctrl.finishRetryOnce.Do(func() {
			go m.retryFinishRun(retryCtx, ctrl, status, errorCode, finishMessage)
		})
	}
	return err
}

// resolveTerminalStatus decides once, for both terminal writes, how a run ended.
//
// An owner is allowed to finish a run without naming an outcome, and that is the
// normal case for a run stopped by something the owner did not do: an abort
// routed in from another server cancels the execution context, so all the owner
// ever sees is a cancellation, and reporting that as this run's own failure
// would contradict the intent already recorded (SR-CTL-001).
//
// Resolving here rather than inside each write is what keeps the two agreeing.
// The durable and live paths derive an unnamed outcome by different rules — an
// empty status with no message reads as `completed` to the ledger, while the
// live release reads `aborting` off the projection — so leaving both to derive
// it independently is how a run ends up durably `completed` and live `aborted`.
func (m *Manager) resolveTerminalStatus(ctx context.Context, handle RunHandle, status, errorCode, message string) string {
	if status = strings.TrimSpace(status); status != "" {
		return status
	}
	snapshot, ok, err := m.backend.Load(ctx, handle.key())
	if err != nil || !ok || !runMatchesHandle(snapshot.CurrentRunView, handle) {
		// Without the projection there is nothing to derive from, so fall back to
		// the rule the ledger would have applied on its own.
		if strings.TrimSpace(errorCode) != "" || strings.TrimSpace(message) != "" {
			return RunStatusErrored
		}
		return RunStatusCompleted
	}
	run := snapshot.CurrentRunView
	switch {
	case strings.EqualFold(run.Status, RunStatusAborting), strings.EqualFold(run.Status, RunStatusAborted):
		return RunStatusAborted
	case strings.TrimSpace(run.ErrorCode) != "", strings.TrimSpace(run.Error) != "", strings.TrimSpace(errorCode) != "", strings.TrimSpace(message) != "":
		return RunStatusErrored
	default:
		return RunStatusCompleted
	}
}

const steerRunFinishedError = "runtime run finished before steer was applied"

func rejectPendingSteerOnRunFinish(run *CurrentRunView, now time.Time) {
	if run == nil || run.Steer == nil || !isPendingSteerStatus(run.Steer.Status) {
		return
	}
	run.Steer.Status = SteerStatusRejected
	run.Steer.Error = steerRunFinishedError
	run.Steer.UpdatedAt = now
}

func (m *Manager) finishRunState(ctx context.Context, handle RunHandle, status, errorCode, finishMessage string) (bool, error) {
	admissionTerminal := false
	_, changed, err := m.releaseActiveAndPublish(ctx, handle, func(snapshot Snapshot, now time.Time) (Snapshot, bool, error) {
		run := snapshot.CurrentRunView
		if !isActiveRunStatus(run.Status) {
			return snapshot, false, nil
		}
		if !m.runOwnerMatches(run) {
			return snapshot, false, ErrRunOwnershipLost
		}
		admissionTerminal = strings.EqualFold(run.Status, RunStatusAdmitting)
		finalStatus := status
		if finalStatus == "" {
			finalStatus = RunStatusCompleted
			if strings.EqualFold(snapshot.CurrentRunView.Status, RunStatusAborting) {
				finalStatus = RunStatusAborted
			} else if strings.TrimSpace(snapshot.CurrentRunView.ErrorCode) != "" || strings.TrimSpace(snapshot.CurrentRunView.Error) != "" {
				finalStatus = RunStatusErrored
			}
		}
		snapshot.Seq++
		snapshot.UpdatedAt = now
		snapshot.CurrentRunView.Status = finalStatus
		snapshot.CurrentRunView.UpdatedAt = now
		if errorCode != "" {
			snapshot.CurrentRunView.ErrorCode = errorCode
		}
		switch {
		case finishMessage != "":
			snapshot.CurrentRunView.Error = finishMessage
		case finalStatus == RunStatusCompleted || finalStatus == RunStatusAborted:
			snapshot.CurrentRunView.ErrorCode = ""
			snapshot.CurrentRunView.Error = ""
		}
		snapshot.CurrentRunView.OwnerLeaseExpiresAt = nil
		rejectPendingSteerOnRunFinish(snapshot.CurrentRunView, now)
		return snapshot, true, nil
	}, func(snapshot Snapshot) RuntimeDelta {
		if admissionTerminal {
			return RuntimeDelta{CurrentRunView: snapshot.CurrentRunView}
		}
		return runtimeRunPatch(snapshot, true, true, true, m.distributed != nil)
	})
	return changed, err
}

func (m *Manager) retryFinishRun(ctx context.Context, ctrl *runControl, status, errorCode, message string) {
	if ctrl == nil {
		return
	}
	delay := 100 * time.Millisecond
	for {
		select {
		case <-m.closeCh:
			return
		case <-time.After(delay):
		}
		if m.localControlForHandle(ctrl.handle()) != ctrl {
			return
		}
		changed, err := m.finishRunState(ctx, ctrl.handle(), status, errorCode, message)
		if err == nil || changed {
			if err != nil {
				m.logger.Warn("publish runtime finish failed after state commit", slog.Any("error", err), slog.String("run_id", ctrl.runID))
			}
			m.cleanupFinishedRun(ctx, ctrl.handle())
			return
		}
		if errors.Is(err, ErrRunOwnershipLost) {
			m.forgetLocalControlForHandle(ctx, ctrl.handle())
			return
		}
		m.logger.Warn("retry runtime finish failed", slog.Any("error", err), slog.String("run_id", ctrl.runID))
		if delay < time.Second {
			delay *= 2
			if delay > time.Second {
				delay = time.Second
			}
		}
	}
}

func (m *Manager) cleanupFinishedRun(ctx context.Context, handle RunHandle) {
	handle = handle.normalized()
	ctrl := m.localControlForHandle(handle)
	ref := m.runRefForControl(ctrl)
	m.forgetLocalControlForHandle(ctx, handle)
	if m.distributed == nil {
		return
	}
	if ref.RunID == "" {
		ref = RunRef{BotID: handle.BotID, SessionID: handle.SessionID, RunID: handle.RunID, OwnerID: m.ownerID, Generation: handle.Generation}
	}
	if _, err := m.distributed.DeleteRunRef(context.WithoutCancel(ctx), ref); err != nil {
		m.logger.Warn("delete finished runtime stream reference failed", slog.Any("error", err), slog.String("run_id", handle.RunID))
	}
}

func (m *Manager) HandleAgentEvent(ctx context.Context, handle RunHandle, event native.StreamEvent) ([]chatview.UIMessage, error) {
	return m.handleAgentEvent(ctx, handle, event, nil)
}

// HandleAgentEventWithStatus applies an agent event and returns the run status
// from that same serialized mutation. Terminal callers use this instead of a
// second snapshot read so a routed control and terminal event have one winner
// even when a later backend read would fail.
func (m *Manager) HandleAgentEventWithStatus(ctx context.Context, handle RunHandle, event native.StreamEvent) ([]chatview.UIMessage, string, error) {
	status := ""
	messages, err := m.handleAgentEvent(ctx, handle, event, &status)
	return messages, status, err
}

func (m *Manager) handleAgentEvent(ctx context.Context, handle RunHandle, event native.StreamEvent, committedStatus *string) ([]chatview.UIMessage, error) {
	if m == nil || m.backend == nil {
		return nil, nil
	}
	handle = handle.normalized()
	if !handle.valid() {
		return nil, ErrRunOwnershipLost
	}
	ctrl := m.localControlForHandle(handle)
	if ctrl == nil {
		return nil, ErrRunOwnershipLost
	}
	if handle.FencingToken <= 0 {
		handle.FencingToken = ctrl.fencingToken
	}
	if err := waitRunControlReady(ctx, ctrl); err != nil {
		return nil, err
	}
	if m.localControlForHandle(handle) != ctrl {
		return nil, ErrRunOwnershipLost
	}
	switch event.Type {
	case native.EventToolApprovalRequest, native.EventUserInputRequest:
		if pendingDecisionEvent(event) {
			ctrl.beginDecisionWait()
			if err := m.setWaitingDecision(ctx, handle); err != nil {
				return nil, err
			}
		}
	case native.EventAgentStart:
		if err := m.resumeWaitingDecision(ctx, handle); err != nil {
			return nil, err
		}
	}

	var messages []chatview.UIMessage
	switch event.Type {
	case native.EventAgentStart:
	case native.EventAgentEnd, native.EventAgentAbort:
		messages = ctrl.converter.ConvertTerminalMessages(event.Messages)
	case native.EventError:
	default:
		messages = ctrl.converter.HandleEvent(chatview.UIStreamEventFromAgentEvent(event))
	}
	delta, visibleChange := runtimeDeltaForAgentEvent(event, messages)
	if !visibleChange {
		return messages, nil
	}

	snapshot, changed, err := m.updateActiveAndPublish(ctx, handle, func(snapshot Snapshot, now time.Time) (Snapshot, bool, error) {
		run := snapshot.CurrentRunView
		if !runMatchesHandle(run, handle) || !m.runOwnerMatches(run) || !isEventAcceptingRunStatus(run.Status) {
			return snapshot, false, nil
		}
		snapshot.Seq++
		snapshot.UpdatedAt = now
		run.UpdatedAt = now
		if event.Type == native.EventRetry {
			run.Messages = []chatview.UIMessage{}
			// A retry discards the failed attempt whole, error included: the
			// native stream publishes EventError before retrying, so keeping
			// that text would park the run in errored the moment the recovered
			// attempt reaches its clean end — and nothing after a terminal
			// event can clear it. A retry that runs out of attempts publishes
			// its own final EventError, so the failure is not lost either.
			run.Error = ""
			run.ErrorCode = ""
		}
		for _, msg := range messages {
			run.Messages = upsertUIMessage(run.Messages, msg)
		}
		switch event.Type {
		case native.EventToolApprovalRequest, native.EventUserInputRequest:
			if pendingDecisionEvent(event) {
				run.Status = RunStatusWaitingDecision
			}
		case native.EventAgentStart:
			if strings.EqualFold(run.Status, RunStatusWaitingDecision) {
				run.Status = RunStatusRunning
			}
		case native.EventAgentEnd:
			if strings.EqualFold(run.Status, RunStatusWaitingDecision) {
				return snapshot, true, nil
			}
			switch {
			case strings.TrimSpace(run.ErrorCode) != "", strings.TrimSpace(run.Error) != "":
				run.Status = RunStatusErrored
			case strings.EqualFold(run.Status, RunStatusAborting):
				run.Status = RunStatusAborted
			default:
				run.Status = RunStatusCompleted
			}
			run.OwnerLeaseExpiresAt = nil
			rejectPendingSteerOnRunFinish(run, now)
		case native.EventAgentAbort:
			if strings.TrimSpace(run.ErrorCode) != "" || strings.TrimSpace(run.Error) != "" {
				run.Status = RunStatusErrored
			} else {
				run.Status = RunStatusAborted
			}
			run.OwnerLeaseExpiresAt = nil
			rejectPendingSteerOnRunFinish(run, now)
		case native.EventError:
			run.ErrorCode = strings.TrimSpace(event.Code)
			run.Error = strings.TrimSpace(event.Error)
			if run.Error == "" {
				run.Error = "stream error"
			}
		}
		return snapshot, true, nil
	}, func(snapshot Snapshot) RuntimeDelta {
		switch event.Type {
		case native.EventAgentEnd, native.EventAgentAbort:
			waiting := snapshot.CurrentRunView != nil &&
				strings.EqualFold(snapshot.CurrentRunView.Status, RunStatusWaitingDecision)
			delta.Run = runtimeRunPatch(snapshot, true, !waiting, !waiting, m.distributed != nil).Run
		case native.EventAgentStart, native.EventToolApprovalRequest, native.EventUserInputRequest:
			delta.Run = runtimeRunPatch(snapshot, true, false, false, false).Run
		case native.EventError, native.EventRetry:
			delta.Run = runtimeRunPatch(snapshot, false, true, false, false).Run
		}
		return delta
	})
	if err != nil {
		if errors.Is(err, ErrRunOwnershipLost) {
			return nil, err
		}
		return messages, err
	}
	if !changed {
		return nil, nil
	}
	if committedStatus != nil && snapshot.CurrentRunView != nil && snapshot.CurrentRunView.RunID == handle.RunID {
		*committedStatus = snapshot.CurrentRunView.Status
	}
	return messages, nil
}

func (m *Manager) setWaitingDecision(ctx context.Context, handle RunHandle) error {
	if m == nil || m.runs == nil || handle.FencingToken <= 0 {
		return nil
	}
	_, applied, err := m.runs.SetWaitingDecision(ctx, handle.RunID, handle.FencingToken)
	if err != nil {
		return fmt.Errorf("set runtime run waiting decision: %w", err)
	}
	if !applied {
		return ErrRunOwnershipLost
	}
	return nil
}

func (m *Manager) resumeWaitingDecision(ctx context.Context, handle RunHandle) error {
	if m == nil || m.runs == nil || handle.FencingToken <= 0 {
		return nil
	}
	snapshot, ok, err := m.backend.Load(ctx, handle.key())
	if err != nil {
		return fmt.Errorf("load live runtime before decision resume: %w", err)
	}
	if !ok || !runMatchesHandle(snapshot.CurrentRunView, handle) ||
		!strings.EqualFold(snapshot.CurrentRunView.Status, RunStatusWaitingDecision) {
		return nil
	}
	_, applied, err := m.runs.Resume(ctx, handle.RunID, handle.FencingToken)
	if err != nil {
		return fmt.Errorf("resume runtime run after decision: %w", err)
	}
	if !applied {
		return ErrRunOwnershipLost
	}
	return nil
}

// Snapshot returns the session's authoritative runtime view. The live backend
// answers first because it is the only place a run in flight exists; the ledger
// fallback below covers the case where it cannot answer at all.
func (m *Manager) Snapshot(ctx context.Context, botID, sessionID string) (Snapshot, error) {
	if m == nil || m.backend == nil {
		return EmptySnapshot(botID, sessionID), nil
	}
	snapshot, err := m.liveSnapshot(ctx, botID, sessionID)
	if err != nil {
		return Snapshot{}, err
	}
	return m.hydrateSnapshotFromLedger(ctx, snapshot), nil
}

func (m *Manager) liveSnapshot(ctx context.Context, botID, sessionID string) (Snapshot, error) {
	key := Key{BotID: strings.TrimSpace(botID), SessionID: strings.TrimSpace(sessionID)}
	snapshot, ok, err := m.backend.Load(ctx, key)
	if err != nil {
		return Snapshot{}, err
	}
	if !ok || strings.TrimSpace(snapshot.Epoch) == "" {
		now, nowErr := m.backend.Now(ctx)
		if nowErr != nil {
			return Snapshot{}, fmt.Errorf("load runtime backend time: %w", nowErr)
		}
		epoch := m.newEpoch()
		snapshot, _, err = m.backend.Update(ctx, key, func(snapshot Snapshot, ok bool) (Snapshot, bool, error) {
			if ok && strings.TrimSpace(snapshot.Epoch) != "" {
				return snapshot, false, nil
			}
			if !ok {
				snapshot = EmptySnapshot(key.BotID, key.SessionID)
			}
			snapshot.BotID = key.BotID
			snapshot.SessionID = key.SessionID
			snapshot.Epoch = epoch
			snapshot.UpdatedAt = now
			return snapshot, true, nil
		})
		if err != nil {
			return Snapshot{}, err
		}
	}
	if m.distributed != nil {
		now, err := m.backend.Now(ctx)
		if err != nil {
			return Snapshot{}, fmt.Errorf("load runtime backend time: %w", err)
		}
		if !m.leaseExpired(snapshot.CurrentRunView, now) {
			return snapshot, nil
		}
		lostRef := runRefForRun(snapshot.BotID, snapshot.SessionID, snapshot.CurrentRunView)
		updated, changed, err := m.updateAndPublish(ctx, key, lostRef.RunID, func(current Snapshot, ok bool) (Snapshot, bool, error) {
			if !ok {
				return current, false, nil
			}
			if !m.markLostIfExpired(&current, now) {
				return current, false, nil
			}
			return current, true, nil
		}, func(snapshot Snapshot) RuntimeDelta {
			return runtimeRunPatch(snapshot, true, true, false, true)
		})
		if err != nil {
			return Snapshot{}, err
		}
		snapshot = updated
		if changed && lostRef.RunID != "" {
			_, _ = m.distributed.DeleteRunRef(context.WithoutCancel(ctx), lostRef)
			lostHandle := RunHandle{BotID: lostRef.BotID, SessionID: lostRef.SessionID, RunID: lostRef.RunID, Generation: lostRef.Generation}
			if ctrl := m.localControlForHandle(lostHandle); ctrl != nil {
				m.cancelRunControl(ctrl)
			}
		}
	}
	return snapshot, nil
}

// hydrateSnapshotFromLedger answers SR-OBS-001 in the one case the live backend
// cannot: it no longer holds the run. A replaced backend takes the projection a
// reconnecting client would have read with it, and that is exactly when the
// client most needs the authoritative answer — a run whose owner is gone must
// report `lost`, not appear never to have existed.
//
// PostgreSQL can prove the run's identity, its turn and its durable state, and
// this returns that and nothing more. The streamed text is genuinely gone,
// which is what makes `lost` honest rather than a pretence of resumption.
//
// Nothing is written back. The live projection is derived state, and re-seeding
// it would put a run back into the structure other reads treat as live — the
// snapshot would stop being a report and start being a claim.
func (m *Manager) hydrateSnapshotFromLedger(ctx context.Context, snapshot Snapshot) Snapshot {
	if m.runs == nil || snapshot.CurrentRunView != nil {
		return snapshot
	}
	sessionID := strings.TrimSpace(snapshot.SessionID)
	if sessionID == "" {
		return snapshot
	}
	run, err := m.runs.LatestRun(ctx, sessionID)
	if err != nil {
		// A session that never ran is the ordinary case, not a failure.
		if !errors.Is(err, ledger.ErrRunNotFound) {
			m.logger.Warn("hydrate runtime snapshot from ledger failed",
				slog.Any("error", err),
				slog.String("session_id", sessionID))
		}
		return snapshot
	}
	snapshot.CurrentRunView = &CurrentRunView{
		RunID:        run.RunID,
		TurnID:       run.TurnID,
		InvocationID: run.InvocationID,
		Generation:   run.LiveGeneration,
		Status:       liveRunStatus(run.State),
		OwnerID:      run.OwnerID,
		StartedAt:    run.CreatedAt,
		UpdatedAt:    run.UpdatedAt,
		Error:        strings.TrimSpace(run.ErrorMessage),
		ErrorCode:    strings.TrimSpace(run.ErrorCode),
	}
	return snapshot
}

// liveRunStatus maps a durable state back to the live vocabulary. It is the
// inverse of terminalLedgerState over the states that survive the round trip;
// `admitting` and `aborting` do not, because they are transitions an owner
// passes through and the ledger never records them.
func liveRunStatus(state ledger.State) string {
	switch state {
	case ledger.StateAccepted:
		return RunStatusAdmitting
	case ledger.StateRunning:
		return RunStatusRunning
	case ledger.StateWaitingDecision:
		return RunStatusWaitingDecision
	case ledger.StateAborted:
		return RunStatusAborted
	case ledger.StateFailed:
		return RunStatusErrored
	case ledger.StateLost:
		return RunStatusLost
	default:
		return RunStatusCompleted
	}
}

func (m *Manager) Subscribe(ctx context.Context, botID, sessionID string) (Subscription, error) {
	if m == nil || m.backend == nil {
		ch := make(chan Event)
		close(ch)
		return Subscription{C: ch, Close: func() {}}, nil
	}
	key := Key{BotID: strings.TrimSpace(botID), SessionID: strings.TrimSpace(sessionID)}
	subCtx, cancel := context.WithCancel(ctx)
	backendSub, err := m.backend.Subscribe(subCtx, key)
	if err != nil {
		cancel()
		return Subscription{}, err
	}
	baseline, err := m.Snapshot(subCtx, key.BotID, key.SessionID)
	if err != nil {
		backendSub.Close()
		cancel()
		return Subscription{}, err
	}

	out := make(chan Event, 64)
	out <- Event{
		Type:      EventRuntimeSnapshot,
		BotID:     key.BotID,
		SessionID: key.SessionID,
		Epoch:     baseline.Epoch,
		Seq:       baseline.Seq,
		Snapshot:  &baseline,
	}
	m.subscriptionsMu.Lock()
	if m.subscriptionsClosed || m.isClosed() {
		m.subscriptionsMu.Unlock()
		backendSub.Close()
		cancel()
		return Subscription{}, ErrManagerClosed
	}
	m.subscriptionsWG.Add(1)
	m.subscriptionsMu.Unlock()
	done := make(chan struct{})
	go func() {
		defer m.subscriptionsWG.Done()
		defer close(done)
		defer close(out)
		defer backendSub.Close()
		send := func(event Event) bool {
			select {
			case out <- event:
				return true
			case <-m.closeCh:
				return false
			case <-subCtx.Done():
				return false
			}
		}
		reconcileInterval := 2 * time.Second
		if m.distributed != nil {
			reconcileInterval = runtimeReconcileInterval(m.ownerLeaseTTL)
		}
		ticker := time.NewTicker(reconcileInterval)
		defer ticker.Stop()
		lastEpoch := baseline.Epoch
		lastSeq := baseline.Seq
		terminalDrop := func(message string) {
			_ = send(Event{
				Type:      EventRuntimeDropped,
				BotID:     key.BotID,
				SessionID: key.SessionID,
				Epoch:     lastEpoch,
				Seq:       lastSeq,
				Message:   message,
			})
		}
		reconcile := func(observedEpoch string, observedSeq int64, reason string) bool {
			snapshot, err := m.Snapshot(subCtx, key.BotID, key.SessionID)
			if err != nil {
				if subCtx.Err() == nil {
					m.logger.Warn("reconcile runtime subscription failed", slog.Any("error", err), slog.String("session_id", key.SessionID), slog.String("reason", reason))
					terminalDrop(reason + ": snapshot unavailable")
				}
				return false
			}
			snapshotEpoch := strings.TrimSpace(snapshot.Epoch)
			observedEpoch = strings.TrimSpace(observedEpoch)
			if snapshotEpoch == "" {
				terminalDrop(reason + ": snapshot is missing epoch")
				return false
			}
			if observedEpoch != "" && snapshotEpoch == observedEpoch && snapshot.Seq < observedSeq {
				terminalDrop(reason + ": snapshot is behind observed event")
				return false
			}
			if snapshotEpoch == lastEpoch && snapshot.Seq < lastSeq {
				terminalDrop(reason + ": snapshot sequence regressed")
				return false
			}
			if snapshotEpoch == lastEpoch && snapshot.Seq == lastSeq {
				return true
			}
			lastEpoch = snapshotEpoch
			lastSeq = snapshot.Seq
			return send(Event{
				Type:      EventRuntimeSnapshot,
				BotID:     key.BotID,
				SessionID: key.SessionID,
				Epoch:     snapshotEpoch,
				Seq:       snapshot.Seq,
				Snapshot:  &snapshot,
			})
		}
		events := backendSub.C
		for {
			select {
			case <-m.closeCh:
				return
			case <-subCtx.Done():
				return
			case event, ok := <-events:
				if !ok {
					if subCtx.Err() == nil {
						terminalDrop("runtime backend subscription closed")
					}
					return
				}
				if event.Type == EventRuntimeDropped {
					if !reconcile(runtimeEventEpoch(event), event.Seq, strings.TrimSpace(event.Message)) {
						return
					}
					continue
				}
				if event.Type != EventRuntimeDelta || event.Delta == nil {
					if !reconcile(runtimeEventEpoch(event), event.Seq, "invalid runtime backend event") {
						return
					}
					continue
				}
				eventEpoch := runtimeEventEpoch(event)
				if lastEpoch != "" && eventEpoch == "" {
					if !reconcile(lastEpoch, event.Seq, "runtime event is missing epoch") {
						return
					}
					continue
				}
				if lastEpoch != "" && eventEpoch != "" && eventEpoch != lastEpoch {
					if !reconcile(eventEpoch, event.Seq, "runtime epoch changed") {
						return
					}
					continue
				}
				if eventEpoch == lastEpoch && event.Seq <= lastSeq {
					continue
				}
				if eventEpoch == lastEpoch && event.Seq != lastSeq+1 {
					if !reconcile(eventEpoch, event.Seq, "runtime delta sequence gap") {
						return
					}
					continue
				}
				if eventEpoch != "" {
					lastEpoch = eventEpoch
				}
				if event.Seq > 0 {
					lastSeq = event.Seq
				}
				if !send(event) {
					return
				}
			case <-ticker.C:
				if !reconcile("", 0, "periodic runtime reconciliation") {
					return
				}
			}
		}
	}()
	var closeOnce sync.Once
	return Subscription{
		C: out,
		Close: func() {
			closeOnce.Do(func() {
				cancel()
				<-done
			})
		},
	}, nil
}

func (m *Manager) updateAndPublish(ctx context.Context, key Key, runID string, update SnapshotUpdate, buildDelta func(Snapshot) RuntimeDelta) (Snapshot, bool, error) {
	snapshot, changed, err := m.backend.Update(ctx, key, update)
	if err != nil || !changed {
		return snapshot, changed, err
	}
	delta := RuntimeDelta{}
	if buildDelta != nil {
		delta = buildDelta(snapshot)
	}
	if err := m.publishRuntimeDelta(ctx, snapshot, runID, delta); err != nil {
		m.logger.Warn("publish runtime delta failed; subscribers will reconcile from snapshot", slog.Any("error", err), slog.String("run_id", runID))
	}
	return snapshot, true, nil
}

func (m *Manager) updateActiveAndPublish(ctx context.Context, handle RunHandle, update ActiveRunUpdate, buildDelta func(Snapshot) RuntimeDelta) (Snapshot, bool, error) {
	handle = handle.normalized()
	if !handle.valid() {
		return Snapshot{}, false, ErrRunOwnershipLost
	}
	key := handle.key()
	runID := handle.RunID
	var snapshot Snapshot
	var changed bool
	var err error
	if m.distributed != nil {
		snapshot, changed, err = m.distributed.UpdateActiveRun(ctx, key, runID, handle.Generation, update)
	} else {
		snapshot, changed, err = m.backend.Update(ctx, key, func(snapshot Snapshot, ok bool) (Snapshot, bool, error) {
			if !ok || !runMatchesHandle(snapshot.CurrentRunView, handle) || !isActiveRunStatus(snapshot.CurrentRunView.Status) {
				return snapshot, false, ErrRunOwnershipLost
			}
			now, nowErr := m.backend.Now(ctx)
			if nowErr != nil {
				return snapshot, false, nowErr
			}
			return update(snapshot, now)
		})
	}
	if err != nil || !changed {
		return snapshot, changed, err
	}
	delta := RuntimeDelta{}
	if buildDelta != nil {
		delta = buildDelta(snapshot)
	}
	if err := m.publishRuntimeDelta(ctx, snapshot, runID, delta); err != nil {
		m.logger.Warn("publish runtime delta failed; subscribers will reconcile from snapshot", slog.Any("error", err), slog.String("run_id", runID))
	}
	return snapshot, true, nil
}

func (m *Manager) releaseActiveAndPublish(ctx context.Context, handle RunHandle, update ActiveRunUpdate, buildDelta func(Snapshot) RuntimeDelta) (Snapshot, bool, error) {
	handle = handle.normalized()
	if m.distributed == nil {
		return m.updateActiveAndPublish(ctx, handle, update, buildDelta)
	}
	if !handle.valid() {
		return Snapshot{}, false, ErrRunOwnershipLost
	}
	ref := RunRef{
		BotID: handle.BotID, SessionID: handle.SessionID, RunID: handle.RunID,
		OwnerID: m.ownerID, Generation: handle.Generation,
	}
	snapshot, changed, err := m.distributed.ReleaseRun(ctx, handle.key(), ref, update)
	if err != nil || !changed {
		return snapshot, changed, err
	}
	delta := RuntimeDelta{}
	if buildDelta != nil {
		delta = buildDelta(snapshot)
	}
	if err := m.publishRuntimeDelta(ctx, snapshot, handle.RunID, delta); err != nil {
		m.logger.Warn("publish runtime release delta failed; subscribers will reconcile from snapshot", slog.Any("error", err), slog.String("run_id", handle.RunID))
	}
	return snapshot, true, nil
}

func (m *Manager) runOwnerMatches(run *CurrentRunView) bool {
	if run == nil {
		return false
	}
	if m.distributed == nil {
		return strings.TrimSpace(run.OwnerID) == "" && run.OwnerLeaseExpiresAt == nil
	}
	return strings.TrimSpace(run.OwnerID) == m.ownerID
}

func (m *Manager) publishRuntimeDelta(ctx context.Context, snapshot Snapshot, runID string, delta RuntimeDelta) error {
	eventRunID := strings.TrimSpace(runID)
	if eventRunID == "" && snapshot.CurrentRunView != nil {
		eventRunID = snapshot.CurrentRunView.RunID
	}
	// No defensive clone here: both backends isolate on publish (memory clones
	// the event once for its subscribers, redis marshals it immediately).
	updatedAt := snapshot.UpdatedAt
	return m.backend.Publish(ctx, Event{
		Type:      EventRuntimeDelta,
		BotID:     snapshot.BotID,
		SessionID: snapshot.SessionID,
		Epoch:     snapshot.Epoch,
		RunID:     eventRunID,
		Seq:       snapshot.Seq,
		UpdatedAt: &updatedAt,
		Delta:     &delta,
	})
}
