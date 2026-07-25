package discuss

import (
	"context"
	"encoding/json"
	"log/slog"

	agentdomain "github.com/memohai/memoh/domains/agent"
	sessionpkg "github.com/memohai/memoh/domains/agent/chat/thread"
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
func (r discussTurnRunner) Run(ctx context.Context, service agentdomain.Service, command agentdomain.StartTurnCommand, log *slog.Logger) (discussRunOutcome, bool) {
	handle, err := service.StartTurn(ctx, command)
	if err != nil {
		log.ErrorContext(ctx, "discuss: start turn failed", slog.Any("error", err))
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
			case agentdomain.DiscussEventRunResolved:
				var payload agentdomain.DiscussRunResolvedPayload
				if json.Unmarshal(event.Payload, &payload) == nil {
					outcome.runtimeType = normalizedRuntimeType(payload.RuntimeType)
				}
			case agentdomain.DiscussEventSkipped:
				outcome.skipped = true
			default:
				var streamEvent agentdomain.StreamEvent
				if decodeErr := json.Unmarshal(event.Payload, &streamEvent); decodeErr != nil {
					log.WarnContext(ctx, "discuss: decode stream event failed", slog.Any("error", decodeErr))
					outcome.failed = true
					continue
				}
				outcome.streamed = true
				if streamEvent.Type == agentdomain.Error {
					outcome.failed = true
					log.ErrorContext(ctx, "discuss stream error", slog.String("error", streamEvent.Error))
				}
				if streamEvent.Type == agentdomain.AgentEnd || streamEvent.Type == agentdomain.AgentAbort {
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
				log.ErrorContext(ctx, "discuss turn failed", slog.Any("error", streamErr))
				outcome.failed = true
			}
		case <-ctx.Done():
			log.WarnContext(ctx, "discuss turn cancelled", slog.Any("error", ctx.Err()))
			outcome.cancelled = true
			return outcome, true
		}
	}
	return outcome, true
}
