package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
)

var _ turn.Service = (*Service)(nil)

// SetAllowedTeam restricts the service to a single team. The in-process
// runtime's database pool is session-bound to one team GUC, so commands
// for any other team must fail closed (turn.ErrTeamNotServed) instead of
// silently operating on the bound team's data. The composition root
// injects the self-hosted singleton team; a hosted multi-team runtime
// replaces this with request-scoped team binding.
func (s *Service) SetAllowedTeam(teamID string) {
	s.allowedTeam = teamID
}

// StartTurn validates the command, admits it durably, starts the underlying
// stream, and returns a handle whose Events/Errs mirror the application stream.
func (s *Service) StartTurn(ctx context.Context, cmd turn.StartTurnCommand) (turn.RunHandle, error) {
	if cmd.TeamID == "" {
		return nil, errors.New("turn: TeamID is required")
	}
	if s.allowedTeam != "" && cmd.TeamID != s.allowedTeam {
		return nil, fmt.Errorf("%w: %s", turn.ErrTeamNotServed, cmd.TeamID)
	}
	if cmd.Mode == turn.ModeDiscuss && !s.discussRuntimeConfigured() {
		return nil, errors.New("turn: discuss runtime not configured")
	}

	// The run context is cancellable by cause so a lost owner lease can stop
	// execution with a reason, instead of a superseded owner racing the fence it
	// can no longer pass.
	runCtx, cancelCause := context.WithCancelCause(ctx)
	cancel := func() { cancelCause(context.Canceled) }

	if cmd.Mode == turn.ModeDiscuss {
		admission, err := s.admitTurnRun(runCtx, cmd, cancel, cancelCause, nil)
		if err != nil {
			cancel()
			return nil, err
		}
		runCtx = s.withAdmissionRuntimeFence(runCtx, admission)
		return s.startDiscussTurn(runCtx, cmd, cancel, admission)
	}

	injectCh := make(chan turn.InjectMessage, 16)
	admission, err := s.admitTurnRun(runCtx, cmd, cancel, cancelCause, injectCh)
	if err != nil {
		cancel()
		return nil, err
	}
	runCtx = s.withAdmissionRuntimeFence(runCtx, admission)

	var (
		assetMu sync.Mutex
		assets  []turn.OutboundAssetRef
	)

	req := chatRequestFromCommand(cmd)
	req.RunID = admission.RunID
	req.TurnID = admission.TurnID
	req.TurnPosition = &admission.TurnPosition
	req.InjectCh = injectCh
	req.OutboundAssetCollector = func() []turn.OutboundAssetRef {
		assetMu.Lock()
		defer assetMu.Unlock()
		out := make([]turn.OutboundAssetRef, len(assets))
		copy(out, assets)
		return out
	}

	chunkCh, errCh := s.streamTurnChat(runCtx, req)

	h := &runHandle{
		id:     admission.RunID,
		events: make(chan turn.Event, 16),
		errs:   make(chan error, 1),
		ctx:    runCtx,
		cancel: cancel,
		inject: injectCh,
		addAssets: func(refs []turn.OutboundAssetRef) {
			assetMu.Lock()
			defer assetMu.Unlock()
			assets = append(assets, refs...)
		},
		finishRun:         s.turnRunFinisher(runCtx, admission),
		publishAgentEvent: s.turnAgentEventPublisher(admission.Handle),
	}
	go h.pump(cmd, chunkCh, errCh)
	return h, nil
}

func (s *Service) streamTurnChat(ctx context.Context, req ChatRequest) (<-chan StreamChunk, <-chan error) {
	if s.turnHooks != nil && s.turnHooks.streamChat != nil {
		return s.turnHooks.streamChat(ctx, req)
	}
	return s.StreamChat(ctx, req)
}

type runHandle struct {
	id        string
	events    chan turn.Event
	errs      chan error
	ctx       context.Context
	cancel    context.CancelFunc
	inject    chan turn.InjectMessage
	addAssets func([]turn.OutboundAssetRef)

	// injectMu lets teardown stop direct injections before the durable finisher
	// stops the session sender and the channel can be closed safely.
	injectMu      sync.Mutex
	injectStopped bool
	injectClosed  bool

	// failed records that the run ended in an error or cancellation, and
	// streamErr keeps the error itself when there was one. Both are needed
	// because the terminal record distinguishes a run someone stopped from a run
	// that broke, and cancellation alone cannot tell them apart. Only the pump
	// goroutine writes either, and it reads them in its own defer.
	failed            atomic.Bool
	streamErr         error
	terminalPublished bool
	finishRun         func(status string, cause error)
	publishAgentEvent func(context.Context, native.StreamEvent) error
}

func (h *runHandle) RunID() string             { return h.id }
func (h *runHandle) Events() <-chan turn.Event { return h.events }
func (h *runHandle) Errs() <-chan error        { return h.errs }
func (h *runHandle) Cancel()                   { h.cancel() }

func (h *runHandle) Inject(ctx context.Context, msg turn.InjectMessage) error {
	h.injectMu.Lock()
	defer h.injectMu.Unlock()
	if err := h.ctx.Err(); err != nil {
		return err
	}
	if h.injectStopped {
		return errors.New("turn: run already finished")
	}
	select {
	case h.inject <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-h.ctx.Done():
		return h.ctx.Err()
	}
}

func (h *runHandle) stopInject() {
	h.injectMu.Lock()
	defer h.injectMu.Unlock()
	h.injectStopped = true
}

// closeInject closes the inject channel exactly once after direct and session
// senders have stopped.
func (h *runHandle) closeInject() {
	h.injectMu.Lock()
	defer h.injectMu.Unlock()
	if h.injectClosed {
		return
	}
	h.injectStopped = true
	h.injectClosed = true
	close(h.inject)
}

// finish records the run's terminal state and releases the thread's slot after
// direct injects stop but before the application closes the shared channel.
//
// Every outcome is terminal, including failure. A failed run is this invocation's
// recorded answer, so a platform redelivery of the same external message is a
// replay to drop rather than a turn to run again: nothing here can tell a run
// that failed before saying anything from one that failed halfway through a
// reply, and re-running the latter would say it twice.
func (h *runHandle) finish() {
	if h.finishRun == nil {
		return
	}
	if h.streamErr != nil {
		h.finishRun(sessionruntime.RunStatusErrored, h.streamErr)
		return
	}
	// Once the terminal event is in the runtime projection, that projection is
	// authoritative. A consumer disconnect while forwarding the same terminal
	// event must not rewrite a completed, aborted, or parked run as aborted.
	if h.terminalPublished {
		h.finishRun("", nil)
		return
	}
	// Canceled with nothing on the error channel means someone stopped this run
	// — /stop, a routed abort, or a lost owner lease — rather than it breaking.
	if h.failed.Load() {
		h.finishRun(sessionruntime.RunStatusAborted, nil)
		return
	}
	// An unnamed clean end lets the runtime preserve waiting_decision or derive
	// completed from its event projection. Naming completion here would collapse
	// a deferred decision into a terminal run.
	h.finishRun("", nil)
}

func (h *runHandle) recordStreamFailure(err error) bool {
	h.failed.Store(true)
	if err == nil {
		return false
	}
	// Native and ACP streams may report context.Canceled after the run was
	// explicitly stopped. That terminal signal still means aborted; a provider
	// cancellation while the run context is active remains an error.
	failureCause := err
	if privateCause := apperror.CauseOf(err); privateCause != nil {
		failureCause = privateCause
	}
	explicitlyCanceled := h.ctx != nil &&
		errors.Is(failureCause, context.Canceled) &&
		errors.Is(context.Cause(h.ctx), context.Canceled)
	if explicitlyCanceled {
		return false
	}
	if h.streamErr == nil {
		h.streamErr = err
	}
	return true
}

func runtimeHistoryError(err error) error {
	if err == nil || apperror.CodeOf(err) != "" {
		return err
	}
	return apperror.Wrap(apperror.CodeSessionHistoryInconsistent, err, nil)
}

func (s *Service) turnAgentEventPublisher(handle sessionruntime.RunHandle) func(context.Context, native.StreamEvent) error {
	if s == nil || s.publishTurnEvent == nil {
		return nil
	}
	return func(ctx context.Context, event native.StreamEvent) error {
		return runtimeHistoryError(s.publishTurnEvent(ctx, handle, event))
	}
}

func (h *runHandle) publishChunk(chunk StreamChunk) error {
	if h.publishAgentEvent == nil {
		return nil
	}
	var event native.StreamEvent
	if err := json.Unmarshal(chunk, &event); err != nil || event.Type == "" {
		return nil
	}
	err := h.publishAgentEvent(h.ctx, event)
	if err == nil && (event.Type == native.EventAgentEnd || event.Type == native.EventAgentAbort) {
		h.terminalPublished = true
	}
	return err
}

// RespondToolApproval resumes a turn deferred on tool approval.
func (s *Service) RespondToolApproval(ctx context.Context, input turn.ToolApprovalResponse, eventCh chan<- json.RawMessage) error {
	converted := toolApprovalInputFromResponse(input)
	if handled, err := s.routeToolApprovalResponse(ctx, converted); handled || err != nil {
		return err
	}
	return s.respondToolApproval(ctx, converted, eventCh)
}

// RespondUserInput resumes a turn deferred on ask_user.
func (s *Service) RespondUserInput(ctx context.Context, input turn.UserInputResponse, eventCh chan<- json.RawMessage) error {
	converted := userInputInputFromResponse(input)
	if handled, err := s.routeUserInputResponse(ctx, converted); handled || err != nil {
		return err
	}
	return s.respondUserInput(ctx, converted, eventCh)
}

func (h *runHandle) AddOutboundAssets(refs []turn.OutboundAssetRef) {
	h.addAssets(refs)
}

// pump forwards the application's chunk/error pair into the handle's channels,
// wrapping each chunk as a turn.Event with a monotonically increasing Seq.
func (h *runHandle) pump(cmd turn.StartTurnCommand, chunkCh <-chan StreamChunk, errCh <-chan error) {
	// Deferred order (LIFO): detect external cancellation, cancel, stop direct
	// injects, finish the durable run, close inject, then close the channel pair.
	defer close(h.events)
	defer close(h.errs)
	defer h.closeInject()
	defer h.finish()
	defer h.stopInject()
	defer func() {
		// A canceled run may look like a clean completion here: the
		// application reacts to ctx cancellation by closing both channels.
		// Check before cancel() masks the distinction.
		if h.ctx.Err() != nil {
			h.failed.Store(true)
		}
		h.cancel()
	}()

	var seq int64
	clientGone := false
	ctxDone := h.ctx.Done()
	for chunkCh != nil || errCh != nil {
		select {
		case <-ctxDone:
			h.failed.Store(true)
			clientGone = true
			ctxDone = nil
		case chunk, ok := <-chunkCh:
			if !ok {
				chunkCh = nil
				continue
			}
			if h.ctx.Err() != nil {
				h.failed.Store(true)
				clientGone = true
				ctxDone = nil
			}
			if !clientGone {
				if err := h.publishChunk(chunk); err != nil {
					if h.recordStreamFailure(err) {
						select {
						case h.errs <- err:
						case <-h.ctx.Done():
						}
					}
					h.cancel()
					clientGone = true
					ctxDone = nil
					continue
				}
			}
			seq++
			if clientGone {
				continue
			}
			select {
			case h.events <- turn.Event{
				RunID:    h.id,
				TeamID:   cmd.TeamID,
				ThreadID: cmd.ThreadID,
				Seq:      seq,
				Kind:     parseKind(chunk),
				Payload:  chunk,
			}:
			case <-h.ctx.Done():
				h.failed.Store(true)
				clientGone = true
				ctxDone = nil
			}
		case err, ok := <-errCh:
			if !ok {
				errCh = nil
				continue
			}
			if err != nil {
				if !h.recordStreamFailure(err) {
					clientGone = true
					ctxDone = nil
					continue
				}
				if !clientGone {
					select {
					case h.errs <- err:
					case <-h.ctx.Done():
						clientGone = true
						ctxDone = nil
					}
				}
			}
		}
	}
}

// parseKind extracts the "type" field from a raw chunk, best effort.
func parseKind(p json.RawMessage) string {
	var env struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(p, &env) != nil {
		return ""
	}
	return env.Type
}
