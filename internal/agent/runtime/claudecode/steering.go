package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/apperror"
)

const steerInputEvent event.StreamEventType = "claude_steer_input"

type pendingSteer struct {
	input    external.SteerInput
	step     int
	observed bool
	terminal bool
	settled  chan error
}

func (t *turnRunner) startSteering(ctx context.Context) func() {
	source := t.input.Steering
	if source == nil || t.input.Command != "" {
		return func() {}
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = source.Close(ctx) }()
		select {
		case <-t.ready:
		case <-t.done:
			return
		case <-workerCtx.Done():
			return
		}
		t.mu.Lock()
		supported := t.steerSupported
		t.mu.Unlock()
		if !supported {
			return
		}
		if err := source.Enable(workerCtx); err != nil {
			return
		}
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-t.done:
				return
			default:
			}
			input, ok, err := source.Next(workerCtx)
			if err == nil && ok {
				err = t.submitSteer(workerCtx, input)
			}
			if err != nil {
				if ctx.Err() == nil && (ok || workerCtx.Err() == nil) {
					t.logger.WarnContext(ctx, "claude steer was not delivered", slog.Any("error", err))
					public, _ := apperror.PublicFrom(apperror.New(apperror.CodeRuntimeControlSteerFailed, nil), "")
					t.emit(event.StreamEvent{Type: event.RuntimeNotice, Code: string(public.Code), Delta: public.Detail})
				}
				return
			}
			if ok {
				continue
			}
			select {
			case <-workerCtx.Done():
				return
			case <-t.done:
				return
			case <-source.Wake():
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}

func (t *turnRunner) submitSteer(ctx context.Context, input external.SteerInput) error {
	// Serializes result completion with writes: no input can be sent after the
	// result handler has closed this run's admission window.
	t.submissionMu.Lock()
	t.mu.Lock()
	if t.ending || t.closed {
		t.mu.Unlock()
		t.submissionMu.Unlock()
		return errors.New("claude turn ended")
	}
	pending := &pendingSteer{input: input, step: len(t.steers), settled: make(chan error, 1)}
	t.steers[input.ID] = pending
	sessionID := t.sessionID
	t.mu.Unlock()
	line, err := json.Marshal(map[string]any{"type": "user", "uuid": input.ID, "session_id": sessionID, "priority": "now", "parent_tool_use_id": nil, "message": map[string]any{"role": "user", "content": input.Text}})
	if err == nil {
		err = t.writeLine(line)
	}
	t.submissionMu.Unlock()
	if err != nil {
		t.mu.Lock()
		t.protocolErr = err
		t.mu.Unlock()
		t.finish()
		return err
	}
	select {
	case err := <-pending.settled:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-t.done:
		select {
		case err := <-pending.settled:
			return err
		default:
			return errors.New("claude ended before consuming steer")
		}
	}
}

func (t *turnRunner) handleCommandLifecycle(msg *inboundMessage) {
	t.mu.Lock()
	pending := t.steers[msg.CommandUUID]
	if pending == nil {
		t.mu.Unlock()
		return
	}
	started := msg.State == "started" && !pending.observed && !pending.terminal
	if started {
		pending.observed = true
		t.awaitingResult = true
	}
	terminal := msg.State == "completed" || msg.State == "cancelled" || msg.State == "discarded" || msg.State == "refused"
	if terminal {
		pending.terminal = true
	}
	observed := pending.observed
	t.mu.Unlock()
	if started {
		t.emit(event.StreamEvent{Type: event.TextEnd})
		t.emit(event.StreamEvent{Type: event.ReasoningEnd})
		t.emit(event.StreamEvent{Type: event.StepEnd, StepNumber: pending.step})
		t.mu.Lock()
		t.events = append(t.events, event.StreamEvent{Type: steerInputEvent, ToolCallID: pending.input.ID, Delta: pending.input.Text})
		t.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.ctx), 5*time.Second)
		err := t.input.Steering.Accepted(ctx, pending.input.ID, pending.step)
		cancel()
		pending.settled <- err
	}
	if terminal && !observed {
		select {
		case pending.settled <- errors.New("claude did not consume queued input"):
		default:
		}
	}
	t.maybeFinishResult()
}

func (t *turnRunner) maybeFinishResult() {
	t.submissionMu.Lock()
	defer t.submissionMu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.result == nil || t.awaitingResult || t.result.QueuedTurnCount > 0 {
		return
	}
	for _, pending := range t.steers {
		if !pending.terminal {
			return
		}
	}
	t.ending = true
	t.finish()
}
