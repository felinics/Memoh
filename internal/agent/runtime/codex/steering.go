package codex

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/apperror"
)

// Private journal marker: never sent over the public event transport.
const steerInputEvent event.StreamEventType = "codex_steer_input"

type pendingSteer struct {
	input    external.SteerInput
	observed bool
	step     int
	settled  chan error
}

// A single consumer preserves queue order. It is joined before Prompt returns,
// so an old consumer cannot deliver an input to a later turn on the same thread.
func startSteering(ctx context.Context, client *conn, turn *turnState) func() {
	source := turn.input.Steering
	if source == nil || turn.input.Command != "" {
		return func() {}
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = source.Close(ctx) }()
		if err := source.Enable(workerCtx); err != nil {
			return
		}
		ticker := time.NewTicker(time.Second) // Recover a lost/coalesced cross-owner wake.
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-turn.done:
				return
			default:
			}
			input, ok, err := source.Next(workerCtx)
			if err == nil && ok {
				err = submitSteer(workerCtx, client, turn, input)
			}
			if err != nil {
				if ctx.Err() == nil && (ok || workerCtx.Err() == nil) {
					turn.logger.Warn("codex steer was not delivered", slog.Any("error", err))
					public, _ := apperror.PublicFrom(apperror.New(apperror.CodeRuntimeControlSteerFailed, nil), "")
					turn.emit(event.StreamEvent{Type: event.RuntimeNotice, Code: string(public.Code), Delta: public.Detail})
				}
				return // Never retry an uncertain RPC or silently move it to another turn.
			}
			if ok {
				continue
			}
			select {
			case <-workerCtx.Done():
				return
			case <-turn.done:
				return
			case <-source.Wake():
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}

func submitSteer(ctx context.Context, client *conn, turn *turnState, input external.SteerInput) error {
	turn.mu.Lock()
	if turn.steers == nil {
		turn.steers = make(map[string]*pendingSteer)
	}
	pending := &pendingSteer{input: input, step: len(turn.steers), settled: make(chan error, 1)}
	turn.steers[input.ID] = pending
	expected := turn.turnID
	turn.mu.Unlock()
	if expected == "" || input.ID == "" || input.Text == "" {
		return errors.New("invalid steer input or active turn")
	}
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	watcherDone := make(chan struct{})
	go func() {
		select {
		case <-turn.done:
			cancel()
		case <-requestCtx.Done():
		}
		close(watcherDone)
	}()
	var response protocol.TurnSteerResponse
	err := client.Call(requestCtx, protocol.MethodTurnSteer, protocol.TurnSteerParams{
		ThreadID: turn.threadID, ExpectedTurnID: expected, ClientUserMessageID: &input.ID,
		Input: []protocol.UserInput{{Text: &protocol.TextUserInput{Text: input.Text}}},
	}, &response)
	cancel()
	<-watcherDone
	if err == nil && response.TurnID != expected {
		err = errors.New("codex accepted steer for a different turn")
	}
	if err == nil {
		// The RPC accepts into Codex's input buffer. Only the correlated item
		// notification locates actual consumption amongst tool/text output.
		select {
		case deliveryErr := <-pending.settled:
			return deliveryErr
		case <-turn.done:
			err = errors.New("turn ended before the steer input was consumed")
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	turn.mu.Lock()
	observed := pending.observed
	turn.mu.Unlock()
	if !observed {
		return err
	}
	// A correlated userMessage is authoritative even if the RPC response was
	// lost. Drain its delivery barrier independently of a simultaneous Stop.
	select {
	case deliveryErr := <-pending.settled:
		return deliveryErr
	case <-time.After(6 * time.Second):
		return errors.New("steer projection did not settle")
	}
}

func (t *turnState) recordSteerInput(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	pending := t.steers[id]
	if pending == nil || pending.observed || t.closed {
		return
	}
	pending.observed = true
	// Appending both markers under one lock places the input precisely between
	// surrounding output. Started/completed repeats for the same client ID dedupe.
	for _, ev := range []event.StreamEvent{
		{Type: event.TextEnd},
		{Type: event.ReasoningEnd},
		{Type: event.StepEnd, StepNumber: pending.step},
		{Type: steerInputEvent, ToolCallID: id, Delta: pending.input.Text},
	} {
		t.events = append(t.events, ev)
		t.queue = append(t.queue, ev)
	}
	t.queueCond.Signal()
}

func (t *turnState) deliverSteerInput(parent context.Context, id string) {
	t.mu.Lock()
	pending := t.steers[id]
	t.mu.Unlock()
	if pending == nil || t.input.Steering == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	pending.settled <- t.input.Steering.Accepted(ctx, id, pending.step)
}
