package inbound

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/channel"
)

// InjectMessage is an alias for turn.InjectMessage, re-exported so
// callers within this package do not need to import the conversation package
// directly for inject-related types.
type InjectMessage = turn.InjectMessage

// InboundMode determines how a new inbound message is handled when an agent
// stream is already active for the same route.
type InboundMode int

const (
	// ModeInject (default, command /btw) injects the message into the active
	// agent stream via the PrepareStep hook so the LLM sees it between tool
	// rounds. When no stream is active, starts one normally.
	ModeInject InboundMode = iota
	// ModeParallel (command /now) starts a new agent stream immediately,
	// running concurrently with any existing stream.
	ModeParallel
	// ModeQueue (command /next) queues the message and processes it after the
	// current agent stream completes.
	ModeQueue
)

// QueuedTask holds everything needed to start an agent stream for a queued message.
type QueuedTask struct {
	Ctx         context.Context
	Cfg         channel.ChannelConfig
	Msg         channel.InboundMessage
	Sender      channel.StreamReplySender
	Ident       InboundIdentity
	Text        string
	Attachments []turn.Attachment
}

// PersistFunc is a deferred persistence closure called after the active stream
// completes (and its storeRound has run), ensuring correct created_at ordering.
type PersistFunc func(ctx context.Context)

// routeState tracks in-flight agent activity for a single route.
type routeState struct {
	mu              sync.Mutex
	activeOwners    int
	injectCh        chan InjectMessage
	queue           []QueuedTask
	pendingPersists []PersistFunc
	lastUsed        time.Time
}

// RouteDispatcher manages per-route concurrency for inbound message processing.
// It decides whether a new message should be injected into an active stream,
// run in parallel, or be queued.
type RouteDispatcher struct {
	mu     sync.RWMutex
	routes map[string]*routeState
	logger *slog.Logger
}

// NewRouteDispatcher creates a dispatcher with background cleanup.
func NewRouteDispatcher(logger *slog.Logger) *RouteDispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &RouteDispatcher{
		routes: make(map[string]*routeState),
		logger: logger.With(slog.String("component", "route_dispatcher")),
	}
}

const injectChBuffer = 16

func (d *RouteDispatcher) getOrCreate(routeID string) *routeState {
	d.mu.RLock()
	rs, ok := d.routes[routeID]
	d.mu.RUnlock()
	if ok {
		return rs
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if rs, ok = d.routes[routeID]; ok {
		return rs
	}
	rs = &routeState{
		injectCh: make(chan InjectMessage, injectChBuffer),
		lastUsed: time.Now(),
	}
	d.routes[routeID] = rs
	return rs
}

// IsActive reports whether the given route has an active agent stream.
func (d *RouteDispatcher) IsActive(routeID string) bool {
	routeID = strings.TrimSpace(routeID)
	if routeID == "" {
		return false
	}
	rs := d.getOrCreate(routeID)
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.activeOwners > 0
}

// MarkActive acquires active ownership for a route and returns the shared
// inject channel that the agent should drain via PrepareStep.
func (d *RouteDispatcher) MarkActive(routeID string) <-chan InjectMessage {
	routeID = strings.TrimSpace(routeID)
	if routeID == "" {
		return nil
	}
	rs := d.getOrCreate(routeID)
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.activeOwners++
	rs.lastUsed = time.Now()
	return rs.injectCh
}

// MarkDoneResult holds the data returned when a route transitions from active to idle.
type MarkDoneResult struct {
	PendingPersists []PersistFunc
	QueuedTasks     []QueuedTask
}

// MarkDone releases active ownership for a route. It returns pending persist
// functions and queued tasks only when the last active owner exits.
func (d *RouteDispatcher) MarkDone(routeID string) MarkDoneResult {
	routeID = strings.TrimSpace(routeID)
	if routeID == "" {
		return MarkDoneResult{}
	}
	rs := d.getOrCreate(routeID)
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.lastUsed = time.Now()
	if rs.activeOwners > 0 {
		rs.activeOwners--
	}
	if rs.activeOwners > 0 {
		return MarkDoneResult{}
	}

	drainInjectCh(rs.injectCh)

	var persists []PersistFunc
	if len(rs.pendingPersists) > 0 {
		persists = rs.pendingPersists
		rs.pendingPersists = nil
	}

	var tasks []QueuedTask
	if len(rs.queue) > 0 {
		tasks = rs.queue
		rs.queue = nil
	}

	return MarkDoneResult{PendingPersists: persists, QueuedTasks: tasks}
}

// AddPendingPersist records a deferred persist closure to be executed after the
// active stream completes. This ensures injected messages get a created_at
// timestamp after the triggering message's round.
func (d *RouteDispatcher) AddPendingPersist(routeID string, fn PersistFunc) {
	routeID = strings.TrimSpace(routeID)
	if routeID == "" || fn == nil {
		return
	}
	rs := d.getOrCreate(routeID)
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.pendingPersists = append(rs.pendingPersists, fn)
}

// Inject sends a message to the inject channel of an active route.
// Returns true if the message was accepted (route is active and channel not full).
func (d *RouteDispatcher) Inject(routeID string, msg InjectMessage) bool {
	routeID = strings.TrimSpace(routeID)
	if routeID == "" {
		return false
	}
	rs := d.getOrCreate(routeID)
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.activeOwners == 0 {
		return false
	}
	select {
	case rs.injectCh <- msg:
		if d.logger != nil {
			d.logger.Info("message injected into active stream",
				slog.String("route_id", routeID),
			)
		}
		return true
	default:
		if d.logger != nil {
			d.logger.Warn("inject channel full, message dropped",
				slog.String("route_id", routeID),
			)
		}
		return false
	}
}

// Enqueue adds a task to the route's queue for later processing.
func (d *RouteDispatcher) Enqueue(routeID string, task QueuedTask) {
	routeID = strings.TrimSpace(routeID)
	if routeID == "" {
		return
	}
	rs := d.getOrCreate(routeID)
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.queue = append(rs.queue, task)
	rs.lastUsed = time.Now()
	if d.logger != nil {
		d.logger.Info("message queued",
			slog.String("route_id", routeID),
			slog.Int("queue_size", len(rs.queue)),
		)
	}
}

// Cleanup removes idle route states older than maxAge.
func (d *RouteDispatcher) Cleanup(maxAge time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cutoff := time.Now().Add(-maxAge)
	for id, rs := range d.routes {
		rs.mu.Lock()
		idle := rs.activeOwners == 0 && rs.lastUsed.Before(cutoff) && len(rs.queue) == 0
		rs.mu.Unlock()
		if idle {
			delete(d.routes, id)
		}
	}
}

func drainInjectCh(ch chan InjectMessage) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// DetectMode parses a message prefix to determine the inbound mode.
// Returns the mode and the text with the prefix stripped.
func DetectMode(text string) (InboundMode, string) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ModeInject, trimmed
	}

	type modePrefix struct {
		prefix string
		mode   InboundMode
	}
	prefixes := []modePrefix{
		{"/now ", ModeParallel},
		{"/next ", ModeQueue},
		{"/btw ", ModeInject},
	}
	lower := strings.ToLower(trimmed)
	for _, mp := range prefixes {
		if strings.HasPrefix(lower, mp.prefix) {
			return mp.mode, strings.TrimSpace(trimmed[len(mp.prefix):])
		}
	}
	// Exact match without trailing text (bare command)
	barePrefixes := []modePrefix{
		{"/now", ModeParallel},
		{"/next", ModeQueue},
		{"/btw", ModeInject},
	}
	for _, mp := range barePrefixes {
		if lower == mp.prefix {
			return mp.mode, ""
		}
	}
	return ModeInject, trimmed
}

// IsModeCommand reports whether the text is a mode-prefix command
// (/btw, /now, /next), so the generic command handler should skip it.
func IsModeCommand(text string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(text))
	if trimmed == "" {
		return false
	}
	for _, prefix := range []string{"/now", "/next", "/btw"} {
		if trimmed == prefix || strings.HasPrefix(trimmed, prefix+" ") || strings.HasPrefix(trimmed, prefix+"\t") {
			return true
		}
	}
	return false
}
