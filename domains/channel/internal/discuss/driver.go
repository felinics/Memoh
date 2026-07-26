package discuss

import (
	"context"
	"log/slog"
	"sync"

	agentdomain "github.com/memohai/memoh/domains/agent"
	messagepkg "github.com/memohai/memoh/domains/agent/chat/message"
	"github.com/memohai/memoh/domains/agent/chat/timeline"
	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/channel/inbound"
)

type DiscussCursorStore interface {
	GetDiscussCursor(ctx context.Context, sessionID, scopeKey string) (timeline.DiscussCursorPosition, error)
	UpsertDiscussCursor(ctx context.Context, botID, sessionID, scopeKey, routeID, source string, position timeline.DiscussCursorPosition) error
}

// DiscussArtifactProvider projects the session's active compaction artifacts
// for timeline composition. Implemented agent-side and injected at assembly.
type DiscussArtifactProvider interface {
	ActiveCompactionArtifacts(ctx context.Context, botID, sessionID string) ([]timeline.CompactionArtifact, error)
}

// DiscussStreamBroadcaster publishes stream events to local UI subscribers.
// Implemented by local.RouteHub.
type DiscussStreamBroadcaster interface {
	PublishEvent(routeKey string, event gateway.StreamEvent)
}

// DiscussDriverDeps holds dependencies injected into the DiscussDriver.
type DiscussDriverDeps struct {
	Turn           agentdomain.Service
	MessageService messagepkg.Service
	CursorStore    DiscussCursorStore
	Artifacts      DiscussArtifactProvider
	Broadcaster    DiscussStreamBroadcaster
	Logger         *slog.Logger
}

// DiscussDriver owns worker lifecycle only. Trigger construction, history,
// cursor persistence, turn execution, and stream projection live in dedicated
// collaborators.
type DiscussDriver struct {
	mu        sync.Mutex
	turn      agentdomain.Service
	sessions  map[string]*discussSession
	workers   map[*discussSession]struct{}
	stopped   bool
	history   discussHistoryReader
	cursor    discussCursorTracker
	trigger   discussTriggerBuilder
	runner    discussTurnRunner
	artifacts DiscussArtifactProvider
	logger    *slog.Logger
}

type discussSession struct {
	config        inbound.DiscussSessionConfig
	rcCh          chan timeline.RenderedContext
	stopCh        chan struct{}
	done          chan struct{}
	cancel        context.CancelFunc
	stopOnce      sync.Once
	lastProcessed timeline.DiscussCursorPosition
}

func (s *discussSession) stop() {
	s.cancel()
	s.stopOnce.Do(func() { close(s.stopCh) })
}

var _ inbound.DiscussDriver = (*DiscussDriver)(nil)

// NewDiscussDriver creates a new DiscussDriver.
func NewDiscussDriver(deps DiscussDriverDeps) *DiscussDriver {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With(slog.String("service", "channel/discuss"))
	projector := newDiscussEventProjector(deps.Broadcaster)
	return &DiscussDriver{
		turn:      deps.Turn,
		sessions:  make(map[string]*discussSession),
		workers:   make(map[*discussSession]struct{}),
		history:   discussHistoryReader{messages: deps.MessageService, logger: logger},
		cursor:    discussCursorTracker{store: deps.CursorStore},
		runner:    discussTurnRunner{projector: projector},
		artifacts: deps.Artifacts,
		logger:    logger,
	}
}

// NotifyRC pushes a new timeline.RenderedContext to the discuss thread worker.
// If the worker goroutine is not running, it starts one.
func (d *DiscussDriver) NotifyRC(_ context.Context, sessionID string, rc timeline.RenderedContext, config inbound.DiscussSessionConfig) {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return
	}
	sess, ok := d.sessions[sessionID]
	if !ok {
		sessCtx, cancel := context.WithCancel(context.Background()) //nolint:gosec // G118: cancel is stored in sess.cancel
		sess = &discussSession{
			config: config,
			rcCh:   make(chan timeline.RenderedContext, 16),
			stopCh: make(chan struct{}),
			done:   make(chan struct{}),
			cancel: cancel,
		}
		d.sessions[sessionID] = sess
		d.workers[sess] = struct{}{}
		go d.runSession(sessCtx, sess) //nolint:contextcheck // long-lived goroutine; must outlive the inbound HTTP request
	} else {
		sess.config = config
	}
	d.mu.Unlock()

	select {
	case sess.rcCh <- rc:
	default:
		select {
		case <-sess.rcCh:
		default:
		}
		select {
		case sess.rcCh <- rc:
		default:
		}
	}
}

// StopSession stops a discuss thread worker.
func (d *DiscussDriver) StopSession(sessionID string) {
	d.mu.Lock()
	sess, ok := d.sessions[sessionID]
	if ok {
		delete(d.sessions, sessionID)
		sess.stop()
	}
	d.mu.Unlock()
}

// Shutdown stops all discuss session goroutines and waits for their active
// turns to release runtime dependencies.
func (d *DiscussDriver) Shutdown(ctx context.Context) error {
	d.mu.Lock()
	d.stopped = true
	workers := make([]*discussSession, 0, len(d.workers))
	for sess := range d.workers {
		sess.stop()
		workers = append(workers, sess)
	}
	d.mu.Unlock()

	for _, sess := range workers {
		if sess.done == nil {
			continue
		}
		select {
		case <-sess.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// HasSession returns true if a discuss thread worker is running.
func (d *DiscussDriver) HasSession(sessionID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.sessions[sessionID]
	return ok
}

func (d *DiscussDriver) sessionConfigSnapshot(sess *discussSession) inbound.DiscussSessionConfig {
	d.mu.Lock()
	defer d.mu.Unlock()
	return sess.config
}
