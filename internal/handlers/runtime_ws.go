package handlers

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	chatview "github.com/felinics/memoh/internal/agent/view"
)

// Each half is reached by asserting the one injected session runtime, and a
// failed assertion degrades silently — a subscription that never delivers, an
// abort that never routes. These assertions turn that into a build failure, so
// renaming or resignaturing a manager method cannot quietly unwire the handler.
var (
	_ runtimeSubscriptionSource = (*sessionruntime.Manager)(nil)
	_ runtimeRunEventPublisher  = (*sessionruntime.Manager)(nil)
	_ runtimeControlRouter      = (*sessionruntime.Manager)(nil)
)

// runtimeSubscriptionSource is the observation half of the session runtime.
// It is declared as the single method this file calls so a subscription can
// never reach admission, ownership, or command routing: SR-OBS-003 requires
// that subscribing neither creates, claims, replays, nor releases a run, and
// the narrow interface is what makes that structural rather than a convention.
//
// *sessionruntime.Manager satisfies it. The manager is a process-wide
// singleton, so every connection on this server observes the same projection
// instead of building a second one per socket.
type runtimeSubscriptionSource interface {
	Subscribe(ctx context.Context, botID, sessionID string) (sessionruntime.Subscription, error)
}

// sessionRuntimeObserver narrows the injected session runtime to its
// observation half. Admission and observation are the same *Manager — one
// process-wide singleton — so this reads the existing dependency rather than
// adding a second injection point that could be wired to a different manager
// and split the projection in two.
func (h *LocalChannelHandler) sessionRuntimeObserver() runtimeSubscriptionSource {
	if h == nil || h.sessionRuntime == nil {
		return nil
	}
	source, ok := h.sessionRuntime.(runtimeSubscriptionSource)
	if !ok {
		return nil
	}
	return source
}

// runtimeRunEventPublisher is the publication half of the session runtime: it
// folds one agent event into the run's authoritative state so every subscriber
// of the session sees it, including subscribers on another server. It is
// declared as the single method this handler calls for the same reason as
// runtimeSubscriptionSource — publishing must not become a route to admission
// or ownership.
type runtimeRunEventPublisher interface {
	HandleAgentEvent(ctx context.Context, handle sessionruntime.RunHandle, event native.StreamEvent) ([]chatview.UIMessage, error)
}

// runtimeControlRouter routes a client's control request to whichever process
// owns the run. The connection that receives an abort is frequently not the one
// executing the run — a reconnect lands anywhere, and in a cluster that is
// routinely a different server — so a control that only reached local state
// would silently do nothing (SR-CTL-001).
type runtimeControlRouter interface {
	AbortControl(ctx context.Context, botID, sessionID, runID, controlID string) (bool, error)
	RouteDecisionResponse(ctx context.Context, response sessionruntime.DecisionResponse) (sessionruntime.DecisionResponseResult, error)
}

// sessionRuntimePublisher and sessionRuntimeController narrow the one injected
// session runtime to the half each caller needs, for the reason given on
// sessionRuntimeObserver: admission, observation, publication, and control are
// all the same process-wide *Manager, and reading the existing dependency is
// what keeps them from being wired to different ones.
func (h *LocalChannelHandler) sessionRuntimePublisher() runtimeRunEventPublisher {
	if h == nil || h.sessionRuntime == nil {
		return nil
	}
	publisher, ok := h.sessionRuntime.(runtimeRunEventPublisher)
	if !ok {
		return nil
	}
	return publisher
}

func (h *LocalChannelHandler) sessionRuntimeController() runtimeControlRouter {
	if h == nil || h.sessionRuntime == nil {
		return nil
	}
	controller, ok := h.sessionRuntime.(runtimeControlRouter)
	if !ok {
		return nil
	}
	return controller
}

// wsMessageAdmission owns the one prepared attachment set shared by the
// runtime projection and the eventual history write. Normal uploads are
// ingested only after admission wins; skill activations pass an already
// prepared set.
type wsMessageAdmission struct {
	handler             *LocalChannelHandler
	botID               string
	request             chatview.UITurn
	attachments         []turn.Attachment
	attachmentsPrepared bool
}

func (a *wsMessageAdmission) build(ctx context.Context, handle sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
	if !a.attachmentsPrepared {
		a.attachments = a.handler.ingestWSInboundAttachments(ctx, a.botID, a.attachments)
		a.attachmentsPrepared = true
	}
	request := a.request
	request.TurnID = handle.TurnID
	request.Attachments = chatview.UIAttachmentsFromTurnAttachments(a.botID, a.attachments)
	return sessionruntime.RunAdmissionView{RequestUserTurn: &request}, nil
}

func (a *wsMessageAdmission) preparedAttachments() []turn.Attachment {
	return a.attachments
}

// wsReplacementAdmission publishes a validated retry/edit boundary as part of
// the run's first authoritative view. Other subscribers can therefore remove
// the replaced history tail before any model output arrives.
type wsReplacementAdmission struct {
	handler             *LocalChannelHandler
	botID               string
	kind                string
	prepareAnchor       func(context.Context) (string, error)
	replacementUserTurn *chatview.UITurn
	attachments         []turn.Attachment
	attachmentsPrepared bool
}

func (a *wsReplacementAdmission) build(ctx context.Context, handle sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
	if a.prepareAnchor == nil {
		return sessionruntime.RunAdmissionView{}, errors.New("replacement operation validator is not configured")
	}
	replaceFromMessageID, err := a.prepareAnchor(ctx)
	if err != nil {
		return sessionruntime.RunAdmissionView{}, err
	}
	operation := &sessionruntime.RunOperationView{
		Kind:                 a.kind,
		ReplaceFromMessageID: replaceFromMessageID,
	}
	if a.replacementUserTurn != nil {
		if !a.attachmentsPrepared {
			a.attachments = a.handler.ingestWSInboundAttachments(ctx, a.botID, a.attachments)
			a.attachmentsPrepared = true
		}
		replacement := *a.replacementUserTurn
		replacement.TurnID = handle.TurnID
		replacement.Attachments = chatview.UIAttachmentsFromTurnAttachments(a.botID, a.attachments)
		operation.ReplacementUserTurn = &replacement
	}
	return sessionruntime.RunAdmissionView{Operation: operation}, nil
}

func (a *wsReplacementAdmission) preparedAttachments() []turn.Attachment {
	return a.attachments
}

// runtimeSubscribeAuthorizer re-checks this connection's read access to one
// session. It runs on every subscribe rather than once at connect: a socket
// opened while the user could read a session must not keep observing it after
// access is revoked, and session_id alone is not an authorization (SR-OBS-003,
// last bullet).
type runtimeSubscribeAuthorizer func(ctx context.Context, sessionID string) error

// runtimeClientMessage is the inbound half of the subscription protocol.
// Cursor is accepted and reported, but it does not select a replay position:
// see runtimeSubscriptions.subscribe.
type runtimeClientMessage struct {
	Type      string         `json:"type"`
	SessionID string         `json:"session_id,omitempty"`
	Cursor    *runtimeCursor `json:"cursor,omitempty"`
}

type runtimeCursor struct {
	Epoch string `json:"epoch,omitempty"`
	Seq   int64  `json:"seq,omitempty"`
}

// runtimeOutboundEvent is the public subscription frame. Every field a client
// dedupes or recovers on — session, epoch, seq — is top level, so a client can
// route and order a frame without decoding the payload it carries.
type runtimeOutboundEvent struct {
	Type      string                       `json:"type"`
	SessionID string                       `json:"session_id"`
	Epoch     string                       `json:"epoch,omitempty"`
	Seq       int64                        `json:"seq"`
	Snapshot  *sessionruntime.Snapshot     `json:"snapshot,omitempty"`
	Delta     *sessionruntime.RuntimeDelta `json:"delta,omitempty"`
	Message   string                       `json:"message,omitempty"`
}

const (
	runtimeSubscribeMessageType   = "runtime_subscribe"
	runtimeUnsubscribeMessageType = "runtime_unsubscribe"
)

// runtimeSubscriptions is one connection's set of live session subscriptions.
// A connection may observe many sessions at once, so the set is keyed by
// session id; a run belongs to a session and never to a socket, which is why
// closing one subscription never touches the run behind it.
type runtimeSubscriptions struct {
	source runtimeSubscriptionSource
	writer *wsWriter
	logger *slog.Logger

	mu     sync.Mutex
	closed bool
	active map[string]*runtimeSubscription
}

// runtimeSubscription is one session's forwarding goroutine. done is closed by
// that goroutine, so close() can wait for it and guarantee no forwarder
// outlives the connection.
type runtimeSubscription struct {
	sessionID string
	cancel    context.CancelFunc
	stop      func()
	done      chan struct{}
	closeOnce sync.Once
}

func (s *runtimeSubscription) close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.cancel()
		if s.stop != nil {
			s.stop()
		}
	})
	<-s.done
}

func newRuntimeSubscriptions(source runtimeSubscriptionSource, writer *wsWriter, logger *slog.Logger) *runtimeSubscriptions {
	if logger == nil {
		logger = slog.Default()
	}
	return &runtimeSubscriptions{
		source: source,
		writer: writer,
		logger: logger,
		active: make(map[string]*runtimeSubscription),
	}
}

// handle dispatches the two subscription messages and reports whether it owned
// the message, so the connection loop can fall through to the turn-starting
// message types it still handles itself.
func (r *runtimeSubscriptions) handle(ctx context.Context, botID string, msg runtimeClientMessage, authorize runtimeSubscribeAuthorizer) bool {
	switch strings.TrimSpace(msg.Type) {
	case runtimeSubscribeMessageType:
		r.subscribe(ctx, botID, msg, authorize)
		return true
	case runtimeUnsubscribeMessageType:
		r.unsubscribe(msg.SessionID)
		return true
	default:
		return false
	}
}

// subscribe authorizes, then replaces any existing subscription for the same
// session atomically, so a client that re-subscribes after a gap never ends up
// with two forwarders racing to write the same session's frames in the wrong
// order.
//
// The cursor is deliberately not used to resume. There is no durable event log
// behind the live projection, so the only honest answer to any cursor is the
// current authoritative snapshot followed by real deltas; synthesising the
// increments between a client's position and now would be fabricated history.
// A client that finds the snapshot ahead of its cursor discards what it had,
// which is exactly the recovery SR-OBS-002 asks for.
func (r *runtimeSubscriptions) subscribe(ctx context.Context, botID string, msg runtimeClientMessage, authorize runtimeSubscribeAuthorizer) {
	sessionID := strings.TrimSpace(msg.SessionID)
	if sessionID == "" {
		sendWSError(r.writer, wsTurn("", ""), "session_id is required")
		return
	}
	if r.source == nil {
		sendWSError(r.writer, wsTurn("", sessionID), "session runtime is not configured")
		return
	}
	if authorize != nil {
		if err := authorize(ctx, sessionID); err != nil {
			sendWSError(r.writer, wsTurn("", sessionID), wsErrorMessage(err))
			return
		}
	}

	subCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	subscription, err := r.source.Subscribe(subCtx, botID, sessionID)
	if err != nil {
		cancel()
		sendWSError(r.writer, wsTurn("", sessionID), wsErrorMessage(err))
		return
	}

	entry := &runtimeSubscription{
		sessionID: sessionID,
		cancel:    cancel,
		stop:      subscription.Close,
		done:      make(chan struct{}),
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		cancel()
		subscription.Close()
		close(entry.done)
		return
	}
	previous := r.active[sessionID]
	r.active[sessionID] = entry
	r.mu.Unlock()

	// Closing the old forwarder outside the lock keeps a slow or blocked
	// subscriber from stalling every other session on this connection.
	if previous != nil {
		previous.close()
	}

	go r.forward(entry, subscription)
}

func (r *runtimeSubscriptions) unsubscribe(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sendWSError(r.writer, wsTurn("", ""), "session_id is required")
		return
	}
	r.mu.Lock()
	entry := r.active[sessionID]
	delete(r.active, sessionID)
	r.mu.Unlock()
	entry.close()
}

// close tears down every subscription this connection holds. It is the
// disconnect path: the runs themselves keep executing, because a run is owned
// by the session runtime and not by the socket that started it.
func (r *runtimeSubscriptions) close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	entries := make([]*runtimeSubscription, 0, len(r.active))
	for sessionID, entry := range r.active {
		entries = append(entries, entry)
		delete(r.active, sessionID)
	}
	r.mu.Unlock()
	for _, entry := range entries {
		entry.close()
	}
}

// forward drains one subscription until it ends. The manager's own subscriber
// buffer drops rather than blocks on overflow and reports the gap as a dropped
// event, so a slow client costs itself a re-subscribe and never applies back
// pressure to the run or to another subscriber.
func (r *runtimeSubscriptions) forward(entry *runtimeSubscription, subscription sessionruntime.Subscription) {
	defer close(entry.done)
	for event := range subscription.C {
		outbound, ok := runtimeEventFrame(entry.sessionID, event)
		if !ok {
			continue
		}
		r.writer.SendJSON(outbound)
	}
	// A subscription that ends on its own — manager shutdown, backend loss —
	// is a gap from the client's point of view, so it is reported as one
	// unless this connection is the side that closed it.
	r.mu.Lock()
	current := r.active[entry.sessionID]
	closed := r.closed
	r.mu.Unlock()
	if closed || current != entry {
		return
	}
	r.writer.SendJSON(runtimeOutboundEvent{
		Type:      sessionruntime.EventRuntimeDropped,
		SessionID: entry.sessionID,
		Message:   "runtime subscription closed",
	})
}

// runtimeEventFrame projects a manager event onto the public frame. The
// session id comes from the subscription rather than the event so a client can
// always route a frame to the session it asked about, even for an event the
// backend emitted without one.
func runtimeEventFrame(sessionID string, event sessionruntime.Event) (runtimeOutboundEvent, bool) {
	frame := runtimeOutboundEvent{
		SessionID: sessionID,
		Epoch:     strings.TrimSpace(event.Epoch),
		Seq:       event.Seq,
	}
	switch event.Type {
	case sessionruntime.EventRuntimeSnapshot:
		if event.Snapshot == nil {
			return runtimeOutboundEvent{}, false
		}
		frame.Type = sessionruntime.EventRuntimeSnapshot
		frame.Snapshot = event.Snapshot
		return frame, true
	case sessionruntime.EventRuntimeDelta:
		if event.Delta == nil {
			return runtimeOutboundEvent{}, false
		}
		frame.Type = sessionruntime.EventRuntimeDelta
		frame.Delta = event.Delta
		return frame, true
	case sessionruntime.EventRuntimeDropped:
		frame.Type = sessionruntime.EventRuntimeDropped
		frame.Message = strings.TrimSpace(event.Message)
		if frame.Message == "" {
			frame.Message = "runtime subscription gap"
		}
		return frame, true
	default:
		return runtimeOutboundEvent{}, false
	}
}
