package discuss

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	agentevent "github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/errlog"
)

const sessionRuntimeACPAgent = sessionpkg.RuntimeACPAgent

type discussTurnRunner struct {
	projector *discussEventProjector
}

type discussRunOutcome struct {
	runtimeType string
	streamed    bool
	terminal    bool
	failed      bool
	// endedClean marks a run the runtime closed with AgentEnd. A recovered
	// mid-stream retry emits an Error event first and still replies, so a
	// clean end outranks the sticky failed flag for cursor commits.
	endedClean bool
	skipped    bool
	cancelled  bool
	// recomposeRequested reports that the runtime compacted the thread
	// synchronously instead of running the model; the worker must reload
	// artifacts, rebuild the plan, and resubmit without advancing the cursor.
	recomposeRequested bool
}

// Run starts one Agent turn and reduces its ordered event stream to the
// cursor-commit facts needed by the worker. It writes the one result line of
// the turn.
func (r discussTurnRunner) Run(ctx context.Context, service turn.Service, command turn.StartTurnCommand, log *slog.Logger) (discussRunOutcome, bool) {
	start := time.Now()
	var (
		outcome      discussRunOutcome
		turnErr      error
		streamErr    error
		streamErrors int
		terminal     *turn.RunTerminal
	)
	defer func() {
		cause := discussTurnCause(outcome, turnErr, runFailure(terminal, turnErr), streamErr)
		logDiscussTurn(ctx, log, outcome, cause, streamErrors, start)
	}()

	handle, err := service.StartTurn(ctx, command)
	if err != nil {
		turnErr = fmt.Errorf("start turn: %w", err)
		if isStartFailure(err) {
			r.projector.BroadcastFailure(command.BotID, apperror.CodeOf(err), apperror.ArgsOf(err))
		}
		return discussRunOutcome{}, false
	}

	// A handle that ends its runs with run_terminal is answered from it: error
	// events are held back and the run's failure is broadcast once, with the
	// copy for its code.
	reportsTerminal := turn.ReportsRunTerminal(handle)
	var held []agentevent.StreamEvent
	defer func() {
		if outcome.cancelled {
			return
		}
		switch failure := runFailure(terminal, turnErr); {
		case failure != nil:
			r.projector.BroadcastFailure(command.BotID, apperror.CodeOf(failure), apperror.ArgsOf(failure))
		case terminal != nil && turnErr != nil:
			r.projector.BroadcastFailure(command.BotID, apperror.CodeOf(turnErr), apperror.ArgsOf(turnErr))
		case terminal == nil:
			for _, event := range held {
				r.projector.Broadcast(command.BotID, event)
			}
		}
	}()

	events, errsCh := handle.Events(), handle.Errs()
	for events != nil || errsCh != nil {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if runTerminal, ok := turn.RunTerminalFrom(event); ok {
				if reportsTerminal {
					terminal = &runTerminal
				}
				continue
			}
			switch event.Kind {
			case turn.DiscussEventRunResolved:
				var payload turn.DiscussRunResolvedPayload
				if json.Unmarshal(event.Payload, &payload) == nil {
					outcome.runtimeType = normalizedRuntimeType(payload.RuntimeType)
				}
			case turn.DiscussEventSkipped:
				outcome.skipped = true
			case turn.DiscussEventRecompose:
				outcome.recomposeRequested = true
			default:
				var streamEvent agentevent.StreamEvent
				if decodeErr := json.Unmarshal(event.Payload, &streamEvent); decodeErr != nil {
					log.WarnContext(ctx, "discuss: decode stream event failed", slog.Any("error", decodeErr))
					outcome.failed = true
					if streamErr == nil {
						streamErr = fmt.Errorf("decode stream event: %w", decodeErr)
					}
					continue
				}
				outcome.streamed = true
				if streamEvent.Type == agentevent.Error {
					outcome.failed = true
					streamErrors++
					if streamErr == nil {
						streamErr = errors.New(streamEvent.Error)
					}
				}
				if streamEvent.Type == agentevent.AgentEnd || streamEvent.Type == agentevent.AgentAbort {
					outcome.terminal = true
				}
				if streamEvent.Type == agentevent.AgentEnd {
					outcome.endedClean = true
				}
				if reportsTerminal && streamEvent.Type == agentevent.Error {
					held = append(held, streamEvent)
					continue
				}
				r.projector.Broadcast(command.BotID, streamEvent)
			}
		case err, ok := <-errsCh:
			if !ok {
				errsCh = nil
				continue
			}
			if err != nil {
				outcome.failed = true
				if turnErr == nil {
					turnErr = err
				}
			}
		case <-ctx.Done():
			outcome.cancelled = true
			return outcome, true
		}
	}
	return outcome, true
}

// isStartFailure reports whether a StartTurn error is a failure to report. A
// busy thread, a duplicate, or a deferred turn is an admission answer: the
// worker retries on the next trigger.
func isStartFailure(err error) bool {
	return !errors.Is(err, turn.ErrSessionBusy) &&
		!errors.Is(err, turn.ErrDuplicateTurn) &&
		!errors.Is(err, turn.ErrTurnDeferred) &&
		!errors.Is(err, context.Canceled)
}

// runFailure is the error a run that ended with a failed run_terminal event
// failed with: the turn error when it names the run's code, so its args and
// origin are kept, and otherwise the run's code. It is nil without the event
// or for a run that did not fail.
func runFailure(terminal *turn.RunTerminal, turnErr error) error {
	if terminal == nil || !terminal.Failed() {
		return nil
	}
	code := apperror.Code(terminal.ErrorCode)
	if code == "" {
		code = apperror.CodeRuntimeRunFailed
	}
	if apperror.CodeOf(turnErr) == code {
		return turnErr
	}
	return apperror.New(code, nil)
}

// discussTurnCause is the error that decides how the turn ended. A turn error
// wins, then the failure the run's terminal event names, then the first
// stream error event. A stream error the runtime recovered from before a
// clean end is not a failure of the turn.
func discussTurnCause(outcome discussRunOutcome, turnErr, runErr, streamErr error) error {
	switch {
	case turnErr != nil:
		return turnErr
	case outcome.cancelled:
		return nil
	case runErr != nil:
		return runErr
	case streamErr != nil && !outcome.endedClean:
		return streamErr
	default:
		return nil
	}
}

func logDiscussTurn(ctx context.Context, log *slog.Logger, outcome discussRunOutcome, cause error, streamErrors int, start time.Time) {
	if outcome.cancelled && cause == nil {
		cause = context.Cause(ctx)
	}
	// No caller waits for a discuss turn, so a client fault is this
	// process's fault.
	result := errlog.Finish(ctx, "discuss.turn", cause, errlog.Options{Async: true})
	attrs := []slog.Attr{
		slog.String("runtime_type", outcome.runtimeType),
		slog.Duration("latency", time.Since(start)),
	}
	if streamErrors > 0 {
		attrs = append(attrs, slog.Int("stream_errors", streamErrors))
	}
	log.LogAttrs(ctx, result.Level, "discuss turn", append(attrs, result.Attrs()...)...)
}
