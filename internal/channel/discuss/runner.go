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
	)
	defer func() {
		logDiscussTurn(ctx, log, outcome, discussTurnCause(outcome, turnErr, streamErr), streamErrors, start)
	}()

	handle, err := service.StartTurn(ctx, command)
	if err != nil {
		turnErr = fmt.Errorf("start turn: %w", err)
		return discussRunOutcome{}, false
	}

	events, errsCh := handle.Events(), handle.Errs()
	for events != nil || errsCh != nil {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
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

// discussTurnCause is the error that decides how the turn ended. A turn error
// wins; a stream error event the runtime recovered from before a clean end
// is not a failure of the turn.
func discussTurnCause(outcome discussRunOutcome, turnErr, streamErr error) error {
	switch {
	case turnErr != nil:
		return turnErr
	case outcome.cancelled:
		return nil
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
