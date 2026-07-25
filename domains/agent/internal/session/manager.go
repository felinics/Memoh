package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	agentdomain "github.com/memohai/memoh/domains/agent"
	chatview "github.com/memohai/memoh/domains/agent/view"
)

type Manager struct {
	backend        Backend
	distributed    DistributedBackend
	ownerID        string
	stateTTL       time.Duration
	ownerLeaseTTL  time.Duration
	commandAckTTL  time.Duration
	commandWorkers int
	logger         *slog.Logger
	newEpoch       func() string
	newGeneration  func() string

	mu                     sync.Mutex
	controls               map[runControlKey]*runControl
	commandHandler         func(context.Context, Command) error
	commandReconciler      func(context.Context, Command) (bool, error)
	pendingCommands        map[string]map[*commandWaiter]struct{}
	inflightCommandTargets map[string]struct{}
	commandExecutions      map[string]chan struct{}
	admittedCommands       map[string]struct{}

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

type runControl struct {
	botID             string
	sessionID         string
	streamID          string
	generation        string
	abortCh           chan<- struct{}
	cancel            context.CancelFunc
	admissionCancel   context.CancelFunc
	lifecycleCtx      context.Context
	lifecycleCancel   context.CancelFunc
	injectCh          chan<- agentdomain.InjectMessage
	injectMu          sync.Mutex
	injectClosed      bool
	converter         *chatview.UIMessageStreamConverter
	leaseStop         func()
	leaseDone         chan struct{}
	leaseLifecycleMu  sync.Mutex
	leaseMu           sync.RWMutex
	leaseValidUntil   time.Time
	leaseChanged      chan struct{}
	ready             chan struct{}
	readyOnce         sync.Once
	abortStateMu      sync.Mutex
	claimEstablished  bool
	admissionComplete bool
	abortRequested    bool
	abortFinalizing   bool
	finishRetryOnce   sync.Once
	ownershipCancel   context.CancelCauseFunc
	ownershipOnce     sync.Once
}

type runControlKey struct {
	botID     string
	sessionID string
	streamID  string
}

func scopedRunControlKey(botID, sessionID, streamID string) runControlKey {
	return runControlKey{
		botID:     strings.TrimSpace(botID),
		sessionID: strings.TrimSpace(sessionID),
		streamID:  strings.TrimSpace(streamID),
	}
}

func (c *runControl) key() runControlKey {
	if c == nil {
		return runControlKey{}
	}
	return scopedRunControlKey(c.botID, c.sessionID, c.streamID)
}

func (c *runControl) handle() RunHandle {
	if c == nil {
		return RunHandle{}
	}
	return RunHandle{BotID: c.botID, SessionID: c.sessionID, StreamID: c.streamID, Generation: c.generation}
}

type Options struct {
	OwnerID                string
	StateTTL               time.Duration
	OwnerLeaseTTL          time.Duration
	CommandAckTTL          time.Duration
	CommandWorkerLimit     int
	Logger                 *slog.Logger
	EpochGenerator         func() string
	RunGenerationGenerator func() string
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
	newEpoch := opts.EpochGenerator
	if newEpoch == nil {
		newEpoch = uuid.NewString
	}
	newGeneration := opts.RunGenerationGenerator
	if newGeneration == nil {
		newGeneration = uuid.NewString
	}
	return &Manager{
		backend:                backend,
		distributed:            distributed,
		ownerID:                ownerID,
		stateTTL:               stateTTL,
		ownerLeaseTTL:          leaseTTL,
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

func (m *Manager) Start(ctx context.Context) error {
	if m == nil || m.distributed == nil {
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
	m.commandCancel = nil
	m.commandDone = nil
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
	m.subscriptionsMu.Lock()
	m.subscriptionsClosed = true
	m.subscriptionsMu.Unlock()
	var backendErr error
	if m.backend != nil {
		backendErr = m.backend.Close()
	}
	m.subscriptionsWG.Wait()
	return errors.Join(releaseErr, controlErr, backendErr)
}

func (m *Manager) StartRun(ctx context.Context, botID, sessionID, streamID string, abortCh chan<- struct{}, cancel context.CancelFunc, injectCh chan<- agentdomain.InjectMessage) error {
	_, err := m.StartRunHandle(ctx, botID, sessionID, streamID, abortCh, cancel, injectCh)
	return err
}

func (m *Manager) StartRunHandle(ctx context.Context, botID, sessionID, streamID string, abortCh chan<- struct{}, cancel context.CancelFunc, injectCh chan<- agentdomain.InjectMessage) (RunHandle, error) {
	return m.StartRunWithAdmissionHandle(ctx, botID, sessionID, streamID, RunAdmissionView{}, abortCh, cancel, injectCh)
}

func (m *Manager) StartRunWithOperation(ctx context.Context, botID, sessionID, streamID string, operation *RunOperationView, abortCh chan<- struct{}, cancel context.CancelFunc, injectCh chan<- agentdomain.InjectMessage) error {
	_, err := m.StartRunWithOperationHandle(ctx, botID, sessionID, streamID, operation, abortCh, cancel, injectCh)
	return err
}

func (m *Manager) StartRunWithOperationHandle(ctx context.Context, botID, sessionID, streamID string, operation *RunOperationView, abortCh chan<- struct{}, cancel context.CancelFunc, injectCh chan<- agentdomain.InjectMessage) (RunHandle, error) {
	return m.StartRunWithAdmissionHandle(ctx, botID, sessionID, streamID, RunAdmissionView{Operation: operation}, abortCh, cancel, injectCh)
}

func (m *Manager) StartRunWithAdmission(ctx context.Context, botID, sessionID, streamID string, admission RunAdmissionView, abortCh chan<- struct{}, cancel context.CancelFunc, injectCh chan<- agentdomain.InjectMessage) error {
	_, err := m.StartRunWithAdmissionHandle(ctx, botID, sessionID, streamID, admission, abortCh, cancel, injectCh)
	return err
}

func (m *Manager) StartRunWithAdmissionHandle(ctx context.Context, botID, sessionID, streamID string, admission RunAdmissionView, abortCh chan<- struct{}, cancel context.CancelFunc, injectCh chan<- agentdomain.InjectMessage) (RunHandle, error) {
	return m.StartRunWithAdmissionBuilderHandle(ctx, botID, sessionID, streamID, func(context.Context, RunHandle) (RunAdmissionView, error) {
		return admission, nil
	}, abortCh, cancel, injectCh)
}

func (m *Manager) StartRunWithOperationBuilder(ctx context.Context, botID, sessionID, streamID string, builder func(context.Context) (*RunOperationView, error), abortCh chan<- struct{}, cancel context.CancelFunc, injectCh chan<- agentdomain.InjectMessage) error {
	if builder == nil {
		return errors.New("runtime operation builder is required")
	}
	_, err := m.StartRunWithOperationBuilderHandle(ctx, botID, sessionID, streamID, func(ctx context.Context, _ RunHandle) (*RunOperationView, error) {
		return builder(ctx)
	}, abortCh, cancel, injectCh)
	return err
}

func (m *Manager) StartRunWithOperationBuilderHandle(ctx context.Context, botID, sessionID, streamID string, builder func(context.Context, RunHandle) (*RunOperationView, error), abortCh chan<- struct{}, cancel context.CancelFunc, injectCh chan<- agentdomain.InjectMessage) (RunHandle, error) {
	if builder == nil {
		return RunHandle{}, errors.New("runtime operation builder is required")
	}
	return m.StartRunWithAdmissionBuilderHandle(ctx, botID, sessionID, streamID, func(ctx context.Context, handle RunHandle) (RunAdmissionView, error) {
		operation, err := builder(ctx, handle)
		return RunAdmissionView{Operation: operation}, err
	}, abortCh, cancel, injectCh)
}

// StartRunWithAdmissionBuilder reserves the cross-server run before executing
// builder, then publishes the running view only after the canonical request
// turn and optional replacement operation are ready.
func (m *Manager) StartRunWithAdmissionBuilder(ctx context.Context, botID, sessionID, streamID string, builder func(context.Context) (RunAdmissionView, error), abortCh chan<- struct{}, cancel context.CancelFunc, injectCh chan<- agentdomain.InjectMessage) error {
	if builder == nil {
		return errors.New("runtime admission builder is required")
	}
	_, err := m.StartRunWithAdmissionBuilderHandle(ctx, botID, sessionID, streamID, func(ctx context.Context, _ RunHandle) (RunAdmissionView, error) {
		return builder(ctx)
	}, abortCh, cancel, injectCh)
	return err
}

func (m *Manager) StartRunWithAdmissionBuilderAndOwnership(ctx context.Context, botID, sessionID, streamID string, builder func(context.Context) (RunAdmissionView, error), ownershipCancel context.CancelCauseFunc, abortCh chan<- struct{}, cancel context.CancelFunc, injectCh chan<- agentdomain.InjectMessage) error {
	if builder == nil {
		if ownershipCancel != nil {
			ownershipCancel(ErrRunOwnershipLost)
		}
		return errors.New("runtime admission builder is required")
	}
	_, err := m.StartRunWithAdmissionBuilderAndOwnershipHandle(ctx, botID, sessionID, streamID, func(ctx context.Context, _ RunHandle) (RunAdmissionView, error) {
		return builder(ctx)
	}, ownershipCancel, abortCh, cancel, injectCh)
	return err
}

func (m *Manager) StartRunWithAdmissionBuilderHandle(ctx context.Context, botID, sessionID, streamID string, builder func(context.Context, RunHandle) (RunAdmissionView, error), abortCh chan<- struct{}, cancel context.CancelFunc, injectCh chan<- agentdomain.InjectMessage) (RunHandle, error) {
	return m.startRunWithAdmissionBuilder(ctx, botID, sessionID, streamID, builder, nil, abortCh, cancel, injectCh)
}

func (m *Manager) StartRunWithAdmissionBuilderAndOwnershipHandle(ctx context.Context, botID, sessionID, streamID string, builder func(context.Context, RunHandle) (RunAdmissionView, error), ownershipCancel context.CancelCauseFunc, abortCh chan<- struct{}, cancel context.CancelFunc, injectCh chan<- agentdomain.InjectMessage) (RunHandle, error) {
	return m.startRunWithAdmissionBuilder(ctx, botID, sessionID, streamID, builder, ownershipCancel, abortCh, cancel, injectCh)
}

func (m *Manager) startRunWithAdmissionBuilder(ctx context.Context, botID, sessionID, streamID string, builder func(context.Context, RunHandle) (RunAdmissionView, error), ownershipCancel context.CancelCauseFunc, abortCh chan<- struct{}, cancel context.CancelFunc, injectCh chan<- agentdomain.InjectMessage) (RunHandle, error) {
	if m == nil || m.backend == nil {
		if ownershipCancel != nil {
			ownershipCancel(ErrRunOwnershipLost)
		}
		return RunHandle{}, nil
	}
	botID = strings.TrimSpace(botID)
	sessionID = strings.TrimSpace(sessionID)
	streamID = strings.TrimSpace(streamID)
	if botID == "" || sessionID == "" || streamID == "" {
		if ownershipCancel != nil {
			ownershipCancel(ErrRunOwnershipLost)
		}
		return RunHandle{}, errors.New("bot_id, session_id, and stream_id are required")
	}
	if builder == nil {
		if ownershipCancel != nil {
			ownershipCancel(ErrRunOwnershipLost)
		}
		return RunHandle{}, errors.New("runtime admission builder is required")
	}
	if err := ctx.Err(); err != nil {
		if ownershipCancel != nil {
			ownershipCancel(ErrRunOwnershipLost)
		}
		return RunHandle{}, err
	}

	admissionCtx, admissionCancel := context.WithCancel(ctx)
	defer admissionCancel()
	ctx = admissionCtx

	runGeneration := m.newGeneration()
	handle := RunHandle{BotID: botID, SessionID: sessionID, StreamID: streamID, Generation: runGeneration}
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.WithoutCancel(ctx))
	ctrl := &runControl{
		botID:           botID,
		sessionID:       sessionID,
		streamID:        streamID,
		generation:      runGeneration,
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
		return RunHandle{}, err
	}
	select {
	case <-m.closeCh:
		m.mu.Unlock()
		ctrl.revokeOwnership(ErrRunOwnershipLost)
		return RunHandle{}, ErrManagerClosed
	default:
	}
	controlKey := scopedRunControlKey(botID, sessionID, streamID)
	if _, exists := m.controls[controlKey]; exists {
		m.mu.Unlock()
		ctrl.revokeOwnership(ErrRunOwnershipLost)
		return RunHandle{}, fmt.Errorf("stream_id %q is already owned by this runtime manager", streamID)
	}
	m.controls[controlKey] = ctrl
	m.mu.Unlock()

	localLeaseStarted := time.Now()
	now, err := m.backend.Now(ctx)
	if err != nil {
		m.removeLocalControl(streamID, ctrl)
		return RunHandle{}, fmt.Errorf("load runtime backend time: %w", err)
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
	var expiredRef StreamRef
	claim := func(snapshot Snapshot, _ bool) (Snapshot, bool, error) {
		if snapshot.CurrentRunView != nil && isActiveRunStatus(snapshot.CurrentRunView.Status) && snapshot.CurrentRunView.StreamID != streamID {
			if m.distributed == nil || !m.markLostIfExpired(&snapshot, now) {
				return snapshot, false, fmt.Errorf("session %q already has an active runtime run", sessionID)
			}
			expiredRef = streamRefForRun(snapshot.BotID, snapshot.SessionID, snapshot.CurrentRunView)
		}
		snapshot.BotID = botID
		snapshot.SessionID = sessionID
		if strings.TrimSpace(snapshot.Epoch) == "" {
			snapshot.Epoch = epoch
		}
		snapshot.Seq++
		snapshot.Queue = nonNilQueue(snapshot.Queue)
		snapshot.UpdatedAt = now
		snapshot.CurrentRunView = &CurrentRunView{
			StreamID:            streamID,
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
		claimedSnapshot, changed, err = m.distributed.StartRun(ctx, key, StreamRef{
			BotID: botID, SessionID: sessionID, StreamID: streamID, OwnerID: m.ownerID, Generation: runGeneration,
		}, claim)
	} else {
		claimedSnapshot, changed, err = m.backend.Update(ctx, key, claim)
	}
	if err != nil {
		if ctrl.abortWasRequested() {
			reconcileErr := m.reconcileCanceledRunClaim(context.WithoutCancel(ctx), ctrl)
			if reconcileErr == nil {
				return RunHandle{}, context.Canceled
			}
			m.removeLocalControl(streamID, ctrl)
			return RunHandle{}, errors.Join(err, reconcileErr)
		}
		m.removeLocalControl(streamID, ctrl)
		return RunHandle{}, err
	}
	if !changed {
		m.removeLocalControl(streamID, ctrl)
		return RunHandle{}, nil
	}
	abortRequested, abortOwner := ctrl.establishClaimForAbort()
	if abortRequested {
		if abortOwner {
			abortErr := m.abortClaimedAdmission(context.WithoutCancel(ctx), ctrl)
			if abortErr != nil {
				return RunHandle{}, abortErr
			}
		}
		return RunHandle{}, context.Canceled
	}
	if err := m.publishRuntimeDelta(context.WithoutCancel(ctx), claimedSnapshot, streamID, RuntimeDelta{CurrentRunView: claimedSnapshot.CurrentRunView}); err != nil {
		m.logger.WarnContext(ctx, "publish admitting runtime checkpoint failed; subscribers will reconcile from snapshot", slog.Any("error", err), slog.String("stream_id", streamID))
	}
	if expiredRef.StreamID != "" && m.distributed != nil {
		_, _ = m.distributed.DeleteStreamRef(context.WithoutCancel(ctx), expiredRef)
	}
	if m.distributed != nil {
		localRenewalStarted := time.Now()
		confirmCtx, confirmCancel := context.WithDeadline(context.WithoutCancel(ctx), ctrl.leaseDeadline())
		renewedAt, renewErr := m.backend.Now(confirmCtx)
		if renewErr == nil {
			renewErr = m.distributed.RenewLease(confirmCtx, key, streamID, m.ownerID, runGeneration, renewedAt, renewedAt.Add(m.ownerLeaseTTL))
		}
		confirmCancel()
		if renewErr != nil {
			ctrl.markReady()
			if m.isClosed() {
				m.removeLocalControl(streamID, ctrl)
				return RunHandle{}, ErrManagerClosed
			}
			if errors.Is(renewErr, ErrRunOwnershipLost) || !ctrl.leaseIsValidAt(time.Now()) {
				m.removeLocalControl(streamID, ctrl)
				renewErr = ErrRunOwnershipLost
			} else {
				_ = m.FinishRun(context.WithoutCancel(ctx), handle, RunStatusErrored, renewErr.Error())
			}
			return RunHandle{}, fmt.Errorf("confirm runtime owner lease: %w", renewErr)
		}
		deadline := localRenewalStarted.Add(m.ownerLeaseTTL)
		if !time.Now().Before(deadline) {
			ctrl.markReady()
			m.removeLocalControl(streamID, ctrl)
			if m.isClosed() {
				return RunHandle{}, ErrManagerClosed
			}
			return RunHandle{}, fmt.Errorf("confirm runtime owner lease: %w", ErrRunOwnershipLost)
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
		return RunHandle{}, err
	}
	if m.isClosed() {
		return RunHandle{}, ErrManagerClosed
	}
	if m.localControlForHandle(handle) != ctrl {
		return RunHandle{}, ErrRunOwnershipLost
	}
	admission, err = normalizeRunAdmission(admission)
	if err != nil {
		ctrl.markReady()
		_ = m.FinishRun(context.WithoutCancel(ctx), handle, RunStatusErrored, err.Error())
		return RunHandle{}, err
	}
	_, changed, err = m.updateActiveAndPublish(context.WithoutCancel(ctx), handle, func(snapshot Snapshot, now time.Time) (Snapshot, bool, error) {
		if m.isClosed() {
			return snapshot, false, ErrManagerClosed
		}
		if m.localControlForHandle(handle) != ctrl {
			return snapshot, false, ErrRunOwnershipLost
		}
		run := snapshot.CurrentRunView
		if run.StreamID == streamID && m.runOwnerMatches(run) && strings.EqualFold(run.Status, RunStatusAborting) {
			return snapshot, false, context.Canceled
		}
		if run.StreamID != streamID || !m.runOwnerMatches(run) || !strings.EqualFold(run.Status, RunStatusAdmitting) {
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
			return RunHandle{}, err
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
		return RunHandle{}, err
	}
	if !ctrl.completeAdmissionForAbort() {
		return RunHandle{}, context.Canceled
	}
	ctrl.markReady()
	return handle, nil
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
		m.removeLocalControl(ctrl.streamID, ctrl)
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
	if m == nil || m.backend == nil {
		return nil
	}
	handle = handle.normalized()
	if !handle.valid() {
		return ErrRunOwnershipLost
	}
	ctrl := m.localControlForHandle(handle)
	if ctrl != nil {
		ctrl.stopCommands()
	}
	status = strings.TrimSpace(status)
	finishMessage := strings.TrimSpace(message)
	changed, err := m.finishRunState(ctx, handle, status, finishMessage)
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
			go m.retryFinishRun(retryCtx, ctrl, status, finishMessage)
		})
	}
	return err
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

func (m *Manager) finishRunState(ctx context.Context, handle RunHandle, status, finishMessage string) (bool, error) {
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
			} else if strings.TrimSpace(snapshot.CurrentRunView.Error) != "" {
				finalStatus = RunStatusErrored
			}
		}
		snapshot.Seq++
		snapshot.UpdatedAt = now
		snapshot.CurrentRunView.Status = finalStatus
		snapshot.CurrentRunView.UpdatedAt = now
		switch {
		case finishMessage != "":
			snapshot.CurrentRunView.Error = finishMessage
		case finalStatus == RunStatusCompleted || finalStatus == RunStatusAborted:
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

func (m *Manager) retryFinishRun(ctx context.Context, ctrl *runControl, status, message string) {
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
		changed, err := m.finishRunState(ctx, ctrl.handle(), status, message)
		if err == nil || changed {
			if err != nil {
				m.logger.WarnContext(ctx, "publish runtime finish failed after state commit", slog.Any("error", err), slog.String("stream_id", ctrl.streamID))
			}
			m.cleanupFinishedRun(ctx, ctrl.handle())
			return
		}
		if errors.Is(err, ErrRunOwnershipLost) {
			m.forgetLocalControlForHandle(ctx, ctrl.handle())
			return
		}
		m.logger.WarnContext(ctx, "retry runtime finish failed", slog.Any("error", err), slog.String("stream_id", ctrl.streamID))
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
	ref := m.streamRefForControl(ctrl)
	m.forgetLocalControlForHandle(ctx, handle)
	if m.distributed == nil {
		return
	}
	if ref.StreamID == "" {
		ref = StreamRef{BotID: handle.BotID, SessionID: handle.SessionID, StreamID: handle.StreamID, OwnerID: m.ownerID, Generation: handle.Generation}
	}
	if _, err := m.distributed.DeleteStreamRef(context.WithoutCancel(ctx), ref); err != nil {
		m.logger.WarnContext(ctx, "delete finished runtime stream reference failed", slog.Any("error", err), slog.String("stream_id", handle.StreamID))
	}
}

func (m *Manager) HandleAgentEvent(ctx context.Context, handle RunHandle, event agentdomain.StreamEvent) ([]chatview.UIMessage, error) {
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
	if err := waitRunControlReady(ctx, ctrl); err != nil {
		return nil, err
	}
	if m.localControlForHandle(handle) != ctrl {
		return nil, ErrRunOwnershipLost
	}

	var messages []chatview.UIMessage
	switch event.Type {
	case agentdomain.AgentStart:
	case agentdomain.AgentEnd, agentdomain.AgentAbort:
		messages = ctrl.converter.ConvertTerminalMessages(event.Messages)
	case agentdomain.Error:
	default:
		messages = ctrl.converter.HandleEvent(chatview.UIStreamEventFromAgentEvent(event))
	}
	delta, visibleChange := runtimeDeltaForAgentEvent(event, messages)
	if !visibleChange {
		return messages, nil
	}

	_, changed, err := m.updateActiveAndPublish(ctx, handle, func(snapshot Snapshot, now time.Time) (Snapshot, bool, error) {
		run := snapshot.CurrentRunView
		if !runMatchesHandle(run, handle) || !m.runOwnerMatches(run) || !isEventAcceptingRunStatus(run.Status) {
			return snapshot, false, nil
		}
		snapshot.Seq++
		snapshot.Queue = nonNilQueue(snapshot.Queue)
		snapshot.UpdatedAt = now
		run.UpdatedAt = now
		if event.Type == agentdomain.Retry {
			run.Messages = []chatview.UIMessage{}
		}
		for _, msg := range messages {
			run.Messages = upsertUIMessage(run.Messages, msg)
		}
		switch event.Type {
		case agentdomain.AgentEnd:
			switch {
			case strings.TrimSpace(run.Error) != "":
				run.Status = RunStatusErrored
			case strings.EqualFold(run.Status, RunStatusAborting):
				run.Status = RunStatusAborted
			default:
				run.Status = RunStatusCompleted
			}
			run.OwnerLeaseExpiresAt = nil
			rejectPendingSteerOnRunFinish(run, now)
		case agentdomain.AgentAbort:
			if strings.TrimSpace(run.Error) != "" {
				run.Status = RunStatusErrored
			} else {
				run.Status = RunStatusAborted
			}
			run.OwnerLeaseExpiresAt = nil
			rejectPendingSteerOnRunFinish(run, now)
		case agentdomain.Error:
			run.Error = strings.TrimSpace(event.Error)
			if run.Error == "" {
				run.Error = "stream error"
			}
		}
		return snapshot, true, nil
	}, func(snapshot Snapshot) RuntimeDelta {
		switch event.Type {
		case agentdomain.AgentEnd, agentdomain.AgentAbort:
			delta.Run = runtimeRunPatch(snapshot, true, true, true, m.distributed != nil).Run
		case agentdomain.Error:
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
	return messages, nil
}

func (m *Manager) Snapshot(ctx context.Context, botID, sessionID string) (Snapshot, error) {
	if m == nil || m.backend == nil {
		return EmptySnapshot(botID, sessionID), nil
	}
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
			snapshot.Queue = nonNilQueue(snapshot.Queue)
			return snapshot, nil
		}
		lostRef := streamRefForRun(snapshot.BotID, snapshot.SessionID, snapshot.CurrentRunView)
		updated, changed, err := m.updateAndPublish(ctx, key, lostRef.StreamID, func(current Snapshot, ok bool) (Snapshot, bool, error) {
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
		if changed && lostRef.StreamID != "" {
			_, _ = m.distributed.DeleteStreamRef(context.WithoutCancel(ctx), lostRef)
			lostHandle := RunHandle{BotID: lostRef.BotID, SessionID: lostRef.SessionID, StreamID: lostRef.StreamID, Generation: lostRef.Generation}
			if ctrl := m.localControlForHandle(lostHandle); ctrl != nil {
				m.cancelRunControl(ctrl)
			}
		}
	}
	snapshot.Queue = nonNilQueue(snapshot.Queue)
	return snapshot, nil
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
					m.logger.WarnContext(ctx, "reconcile runtime subscription failed", slog.Any("error", err), slog.String("session_id", key.SessionID), slog.String("reason", reason))
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

func (m *Manager) updateAndPublish(ctx context.Context, key Key, streamID string, update SnapshotUpdate, buildDelta func(Snapshot) RuntimeDelta) (Snapshot, bool, error) {
	snapshot, changed, err := m.backend.Update(ctx, key, update)
	if err != nil || !changed {
		return snapshot, changed, err
	}
	delta := RuntimeDelta{}
	if buildDelta != nil {
		delta = buildDelta(snapshot)
	}
	if err := m.publishRuntimeDelta(ctx, snapshot, streamID, delta); err != nil {
		m.logger.WarnContext(ctx, "publish runtime delta failed; subscribers will reconcile from snapshot", slog.Any("error", err), slog.String("stream_id", streamID))
	}
	return snapshot, true, nil
}

func (m *Manager) updateActiveAndPublish(ctx context.Context, handle RunHandle, update ActiveRunUpdate, buildDelta func(Snapshot) RuntimeDelta) (Snapshot, bool, error) {
	handle = handle.normalized()
	if !handle.valid() {
		return Snapshot{}, false, ErrRunOwnershipLost
	}
	key := handle.key()
	streamID := handle.StreamID
	var snapshot Snapshot
	var changed bool
	var err error
	if m.distributed != nil {
		snapshot, changed, err = m.distributed.UpdateActiveRun(ctx, key, streamID, handle.Generation, update)
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
	if err := m.publishRuntimeDelta(ctx, snapshot, streamID, delta); err != nil {
		m.logger.WarnContext(ctx, "publish runtime delta failed; subscribers will reconcile from snapshot", slog.Any("error", err), slog.String("stream_id", streamID))
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
	ref := StreamRef{
		BotID: handle.BotID, SessionID: handle.SessionID, StreamID: handle.StreamID,
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
	if err := m.publishRuntimeDelta(ctx, snapshot, handle.StreamID, delta); err != nil {
		m.logger.WarnContext(ctx, "publish runtime release delta failed; subscribers will reconcile from snapshot", slog.Any("error", err), slog.String("stream_id", handle.StreamID))
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

func (m *Manager) publishRuntimeDelta(ctx context.Context, snapshot Snapshot, streamID string, delta RuntimeDelta) error {
	eventStreamID := strings.TrimSpace(streamID)
	if eventStreamID == "" && snapshot.CurrentRunView != nil {
		eventStreamID = snapshot.CurrentRunView.StreamID
	}
	// No defensive clone here: both backends isolate on publish (memory clones
	// the event once for its subscribers, redis marshals it immediately).
	updatedAt := snapshot.UpdatedAt
	return m.backend.Publish(ctx, Event{
		Type:      EventRuntimeDelta,
		BotID:     snapshot.BotID,
		SessionID: snapshot.SessionID,
		Epoch:     snapshot.Epoch,
		StreamID:  eventStreamID,
		Seq:       snapshot.Seq,
		UpdatedAt: &updatedAt,
		Delta:     &delta,
	})
}
