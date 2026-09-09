package discuss

import (
	"context"
	"log/slog"
	"sync"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/channel"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/chat/timeline"
)

type DiscussCursorStore interface {
	GetDiscussCursor(ctx context.Context, sessionID, scopeKey string) (timeline.DiscussCursorPosition, error)
	UpsertDiscussCursor(ctx context.Context, sessionID, scopeKey, routeID, source string, position timeline.DiscussCursorPosition) error
}

// DiscussArtifactProvider projects the session's active compaction artifacts
// for timeline composition. Implemented agent-side and injected at assembly.
type DiscussArtifactProvider interface {
	ActiveCompactionArtifacts(ctx context.Context, botID, sessionID string) ([]timeline.CompactionArtifact, error)
}

// DiscussStreamBroadcaster publishes stream events to local UI subscribers.
// Implemented by local.RouteHub.
type DiscussStreamBroadcaster interface {
	PublishEvent(routeKey string, event channel.StreamEvent)
}

// DiscussDriverDeps holds dependencies injected into the DiscussDriver.
type DiscussDriverDeps struct {
	Turn           turn.Service
	MessageService messagepkg.Service
	CursorStore    DiscussCursorStore
	Artifacts      DiscussArtifactProvider
	Broadcaster    DiscussStreamBroadcaster
	Logger         *slog.Logger
	// AdmissionMaxTokens is the server-wide context admission cap applied
	// before composition materializes anything (CM-ADM-001). Zero selects
	// the shared default; the cap is never disabled.
	AdmissionMaxTokens int
}

// DiscussSessionConfig holds per-thread configuration for discuss mode.
type DiscussSessionConfig struct {
	TeamID            string
	BotID             string
	ThreadID          string
	RouteID           string
	ChannelIdentityID string
	ReplyTarget       string
	CurrentPlatform   string
	ConversationType  string
	ConversationName  string
	SessionToken      string //nolint:gosec // session credential material
	ChatToken         string //nolint:gosec // scoped chat routing token
	ToolHTTPURL       string
}

// DiscussDriver owns worker lifecycle only. Trigger construction, history,
// cursor persistence, turn execution, and stream projection live in dedicated
// collaborators.
type DiscussDriver struct {
	mu                 sync.Mutex
	turn               turn.Service
	sessions           map[string]*discussSession
	history            discussHistoryReader
	cursor             discussCursorTracker
	trigger            discussTriggerBuilder
	runner             discussTurnRunner
	artifacts          DiscussArtifactProvider
	admissionCapTokens int
	logger             *slog.Logger
}

// admissionMaxTokens resolves the effective admission cap; construction
// without an explicit cap still gets the shared default, never unbounded.
func (d *DiscussDriver) admissionMaxTokens() int {
	if d.admissionCapTokens > 0 {
		return d.admissionCapTokens
	}
	return turn.DefaultContextCapTokens
}

type discussSession struct {
	config        DiscussSessionConfig
	rcCh          chan timeline.RenderedContext
	stopCh        chan struct{}
	cancel        context.CancelFunc
	lastProcessed timeline.DiscussCursorPosition
}

// NewDiscussDriver creates a new DiscussDriver.
func NewDiscussDriver(deps DiscussDriverDeps) *DiscussDriver {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With(slog.String("service", "channel/discuss"))
	projector := newDiscussEventProjector(deps.Broadcaster)
	capTokens := deps.AdmissionMaxTokens
	if capTokens <= 0 {
		capTokens = turn.DefaultContextCapTokens
	}
	return &DiscussDriver{
		turn:     deps.Turn,
		sessions: make(map[string]*discussSession),
		history: discussHistoryReader{
			messages: deps.MessageService,
			maxBytes: int64(capTokens) * turn.ContextBytesPerToken,
			logger:   logger,
		},
		cursor:             discussCursorTracker{store: deps.CursorStore},
		runner:             discussTurnRunner{projector: projector},
		artifacts:          deps.Artifacts,
		admissionCapTokens: capTokens,
		logger:             logger,
	}
}

// SetTurnService sets the turn service after construction (breaks DI cycles).
func (d *DiscussDriver) SetTurnService(svc turn.Service) {
	d.mu.Lock()
	d.turn = svc
	d.mu.Unlock()
}

// SetBroadcaster sets the stream broadcaster after construction so that
// discuss-mode agent events are forwarded to the Web UI in real time.
func (d *DiscussDriver) SetBroadcaster(b DiscussStreamBroadcaster) {
	d.runner.projector.SetBroadcaster(b)
}

// NotifyRC pushes a new timeline.RenderedContext to the discuss thread worker.
// If the worker goroutine is not running, it starts one.
func (d *DiscussDriver) NotifyRC(_ context.Context, sessionID string, rc timeline.RenderedContext, config DiscussSessionConfig) {
	d.mu.Lock()
	sess, ok := d.sessions[sessionID]
	if !ok {
		sessCtx, cancel := context.WithCancel(context.Background()) //nolint:gosec // G118: cancel is stored in sess.cancel
		sess = &discussSession{
			config: config,
			rcCh:   make(chan timeline.RenderedContext, 16),
			stopCh: make(chan struct{}),
			cancel: cancel,
		}
		d.sessions[sessionID] = sess
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
		sess.cancel()
		close(sess.stopCh)
		delete(d.sessions, sessionID)
	}
	d.mu.Unlock()
}

// StopAll stops all discuss session goroutines.
func (d *DiscussDriver) StopAll() {
	d.mu.Lock()
	for id, sess := range d.sessions {
		sess.cancel()
		close(sess.stopCh)
		delete(d.sessions, id)
	}
	d.mu.Unlock()
}

// HasSession returns true if a discuss thread worker is running.
func (d *DiscussDriver) HasSession(sessionID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.sessions[sessionID]
	return ok
}

func (d *DiscussDriver) sessionConfigSnapshot(sess *discussSession) DiscussSessionConfig {
	d.mu.Lock()
	defer d.mu.Unlock()
	return sess.config
}

func (d *DiscussDriver) turnServiceSnapshot() turn.Service {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.turn
}
