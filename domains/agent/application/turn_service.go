package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"

	agentdomain "github.com/memohai/memoh/domains/agent"
)

var _ agentdomain.Service = (*Service)(nil)

// SetAllowedTeam restricts the service to a single team. The in-process
// runtime's database pool is session-bound to one team GUC, so commands
// for any other team must fail closed (agentdomain.ErrTeamNotServed) instead of
// silently operating on the bound team's data. The composition root
// injects the self-hosted singleton team; a hosted multi-team runtime
// replaces this with request-scoped team binding.
func (s *Service) SetAllowedTeam(teamID string) {
	s.allowedTeam = teamID
}

// newRunID mints a run identifier.
func newRunID() string { return uuid.NewString() }

// StartTurn validates the command, starts the underlying stream, and
// returns a handle whose Events/Errs mirror the application stream.
func (s *Service) StartTurn(ctx context.Context, cmd agentdomain.StartTurnCommand) (agentdomain.RunHandle, error) {
	if cmd.TeamID == "" {
		return nil, errors.New("turn: TeamID is required")
	}
	if s.allowedTeam != "" && cmd.TeamID != s.allowedTeam {
		return nil, fmt.Errorf("%w: %s", agentdomain.ErrTeamNotServed, cmd.TeamID)
	}
	if cmd.Mode == agentdomain.ModeDiscuss && !s.discussRuntimeConfigured() {
		return nil, errors.New("turn: discuss runtime not configured")
	}
	s.turnIdempotencyOnce.Do(func() {
		if s.turnIdempotency == nil {
			s.turnIdempotency = newIdempotencyRegistry(idempotencyCapacity)
		}
	})
	var releaseClaim func()
	if cmd.IdempotencyKey != "" {
		if !s.turnIdempotency.claim(cmd.TeamID, cmd.IdempotencyKey) {
			return nil, fmt.Errorf("%w: %s", agentdomain.ErrDuplicateTurn, cmd.IdempotencyKey)
		}
		teamID, key := cmd.TeamID, cmd.IdempotencyKey
		releaseClaim = func() { s.turnIdempotency.release(teamID, key) }
	}
	if cmd.Mode == agentdomain.ModeDiscuss {
		return s.startDiscussTurn(ctx, cmd, releaseClaim)
	}
	runCtx, cancel := context.WithCancel(ctx)

	injectCh := make(chan agentdomain.InjectMessage, 16)
	var (
		assetMu sync.Mutex
		assets  []agentdomain.OutboundAssetRef
	)

	req := chatRequestFromCommand(cmd)
	req.InjectCh = injectCh
	req.OutboundAssetCollector = func() []agentdomain.OutboundAssetRef {
		assetMu.Lock()
		defer assetMu.Unlock()
		out := make([]agentdomain.OutboundAssetRef, len(assets))
		copy(out, assets)
		return out
	}

	chunkCh, errCh := s.streamTurnChat(runCtx, req)

	h := &runHandle{
		id:     newRunID(),
		events: make(chan agentdomain.Event, 16),
		errs:   make(chan error, 1),
		ctx:    runCtx,
		cancel: cancel,
		inject: injectCh,
		addAssets: func(refs []agentdomain.OutboundAssetRef) {
			assetMu.Lock()
			defer assetMu.Unlock()
			assets = append(assets, refs...)
		},
		releaseClaim: releaseClaim,
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
	events    chan agentdomain.Event
	errs      chan error
	ctx       context.Context
	cancel    context.CancelFunc
	inject    chan agentdomain.InjectMessage
	addAssets func([]agentdomain.OutboundAssetRef)

	// injectMu guards inject against send-after-close: the pump closes the
	// channel when the run ends so the application's forwarding goroutine
	// (which ranges over it) can exit instead of leaking per turn.
	injectMu     sync.Mutex
	injectClosed bool

	// failed records that the run ended in an error or cancellation; finish
	// then releases the idempotency claim so a redelivery can retry.
	failed       atomic.Bool
	releaseClaim func()
}

func (h *runHandle) RunID() string                    { return h.id }
func (h *runHandle) Events() <-chan agentdomain.Event { return h.events }
func (h *runHandle) Errs() <-chan error               { return h.errs }
func (h *runHandle) Cancel()                          { h.cancel() }

func (h *runHandle) Inject(ctx context.Context, msg agentdomain.InjectMessage) error {
	h.injectMu.Lock()
	defer h.injectMu.Unlock()
	if h.injectClosed {
		if err := h.ctx.Err(); err != nil {
			return err
		}
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

// closeInject closes the inject channel exactly once. Safe against a
// concurrent Inject: the pump cancels the run context before closing, so
// any in-flight Inject unblocks via ctx.Done and drops the mutex first.
func (h *runHandle) closeInject() {
	h.injectMu.Lock()
	defer h.injectMu.Unlock()
	if h.injectClosed {
		return
	}
	h.injectClosed = true
	close(h.inject)
}

// finish releases the idempotency claim for runs that did not complete
// cleanly. Runs the last thing in the pump's defer stack.
func (h *runHandle) finish() {
	if h.failed.Load() && h.releaseClaim != nil {
		h.releaseClaim()
	}
}

// RespondToolApproval resumes a turn deferred on tool approval.
func (s *Service) RespondToolApproval(ctx context.Context, input agentdomain.ToolApprovalResponse, eventCh chan<- json.RawMessage) error {
	return s.respondToolApproval(ctx, toolApprovalInputFromResponse(input), eventCh)
}

// RespondUserInput resumes a turn deferred on ask_user.
func (s *Service) RespondUserInput(ctx context.Context, input agentdomain.UserInputResponse, eventCh chan<- json.RawMessage) error {
	return s.respondUserInput(ctx, userInputInputFromResponse(input), eventCh)
}

func (h *runHandle) AddOutboundAssets(refs []agentdomain.OutboundAssetRef) {
	h.addAssets(refs)
}

// pump forwards the application's chunk/error pair into the handle's channels,
// wrapping each chunk as a agentdomain.Event with a monotonically increasing Seq.
func (h *runHandle) pump(cmd agentdomain.StartTurnCommand, chunkCh <-chan StreamChunk, errCh <-chan error) {
	// Deferred order (LIFO): detect external cancellation, cancel, close
	// inject, release a failed claim, then close the channel pair — so by
	// the time a consumer observes the closed channels, the idempotency
	// claim of a failed/canceled run has already been released.
	defer close(h.events)
	defer close(h.errs)
	defer h.finish()
	defer h.closeInject()
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
	for chunkCh != nil || errCh != nil {
		select {
		case chunk, ok := <-chunkCh:
			if !ok {
				chunkCh = nil
				continue
			}
			seq++
			select {
			case h.events <- agentdomain.Event{
				RunID:    h.id,
				TeamID:   cmd.TeamID,
				ThreadID: cmd.ThreadID,
				Seq:      seq,
				Kind:     parseKind(chunk),
				Payload:  chunk,
			}:
			case <-h.ctx.Done():
				h.failed.Store(true)
				return
			}
		case err, ok := <-errCh:
			if !ok {
				errCh = nil
				continue
			}
			if err != nil {
				h.failed.Store(true)
				select {
				case h.errs <- err:
				case <-h.ctx.Done():
					return
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
