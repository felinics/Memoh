package discuss

import (
	"context"
	"encoding/json"
	"log/slog"

	agentevent "github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/turn"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
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
	skipped     bool
	cancelled   bool
}

// Run starts one Agent turn and reduces its ordered event stream to the
// cursor-commit facts needed by the worker.
func (r discussTurnRunner) Run(ctx context.Context, service turn.Service, command turn.StartTurnCommand, log *slog.Logger) (discussRunOutcome, bool) {
	handle, err := service.StartTurn(ctx, command)
	if err != nil {
		log.Error("discuss: start turn failed", slog.Any("error", err))
		return discussRunOutcome{}, false
	}

	var outcome discussRunOutcome
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
			default:
				var streamEvent agentevent.StreamEvent
				if decodeErr := json.Unmarshal(event.Payload, &streamEvent); decodeErr != nil {
					log.Warn("discuss: decode stream event failed", slog.Any("error", decodeErr))
					outcome.failed = true
					continue
				}
				outcome.streamed = true
				if streamEvent.Type == agentevent.Error {
					outcome.failed = true
					log.Error("discuss stream error", slog.String("error", streamEvent.Error))
				}
				if streamEvent.Type == agentevent.AgentEnd || streamEvent.Type == agentevent.AgentAbort {
					outcome.terminal = true
				}
				r.projector.Broadcast(command.BotID, streamEvent)
			}
		case streamErr, ok := <-errsCh:
			if !ok {
				errsCh = nil
				continue
			}
			if streamErr != nil {
				log.Error("discuss turn failed", slog.Any("error", streamErr))
				outcome.failed = true
			}
		case <-ctx.Done():
			log.Warn("discuss turn cancelled", slog.Any("error", ctx.Err()))
			outcome.cancelled = true
			return outcome, true
		}
	}
	return outcome, true
}
