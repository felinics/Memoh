package discuss

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	agentevent "github.com/memohai/memoh/internal/agent/event"
	"github.com/memohai/memoh/internal/agent/turn"
	"github.com/memohai/memoh/internal/channel"
	sessionpkg "github.com/memohai/memoh/internal/chat/thread"
)

const sessionRuntimeACPAgent = sessionpkg.RuntimeACPAgent

type discussTurnRunner struct {
	projector       *discussEventProjector
	sendToolStreams *channel.SendToolStreamCoordinator
}

type discussRunOutcome struct {
	runtimeType      string
	streamed         bool
	terminal         bool
	completed        bool
	failed           bool
	skipped          bool
	cancelled        bool
	currentReplySent bool
	finalMessages    []turn.ModelMessage
}

// Run starts one Agent turn and reduces its ordered event stream to the
// cursor-commit facts needed by the worker.
func (r discussTurnRunner) Run(
	ctx context.Context,
	service turn.Service,
	command turn.StartTurnCommand,
	config DiscussSessionConfig,
	log *slog.Logger,
) (discussRunOutcome, bool) {
	handle, err := service.StartTurn(ctx, command)
	if err != nil {
		log.Error("discuss: start turn failed", slog.Any("error", err))
		return discussRunOutcome{}, false
	}

	var outcome discussRunOutcome
	preview := newDiscussSendPreview(config, r.sendToolStreams, log)
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
				if successfulCurrentReply(streamEvent, command) {
					outcome.currentReplySent = true
				}
				if outcome.runtimeType != sessionRuntimeACPAgent {
					preview.Handle(ctx, streamEvent)
				}
				if streamEvent.Type == agentevent.AgentEnd || streamEvent.Type == agentevent.AgentAbort {
					outcome.terminal = true
					outcome.completed = streamEvent.Type == agentevent.AgentEnd
					if len(streamEvent.Messages) > 0 {
						var messages []turn.ModelMessage
						if decodeErr := json.Unmarshal(streamEvent.Messages, &messages); decodeErr != nil {
							log.Warn("discuss: decode terminal messages failed", slog.Any("error", decodeErr))
						} else {
							outcome.finalMessages = messages
							if successfulCurrentReplyInMessages(messages, command) {
								outcome.currentReplySent = true
							}
						}
					}
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
			preview.Abort(context.WithoutCancel(ctx))
			return outcome, true
		}
	}
	return outcome, true
}

func successfulCurrentReply(event agentevent.StreamEvent, command turn.StartTurnCommand) bool {
	if event.Type != agentevent.ToolCallEnd || strings.TrimSpace(event.Error) != "" {
		return false
	}
	toolName := strings.TrimSpace(event.ToolName)
	if !isCurrentReplyTool(toolName) {
		return false
	}
	result, ok := event.Result.(map[string]any)
	if !ok {
		// A successful tool-end event is the strongest signal available for
		// runtimes that do not return the native messaging result envelope.
		return true
	}
	return successfulCurrentReplyResult(result, command)
}

func successfulCurrentReplyInMessages(messages []turn.ModelMessage, command turn.StartTurnCommand) bool {
	type toolResultPart struct {
		Type     string         `json:"type"`
		ToolName string         `json:"toolName"`
		Result   map[string]any `json:"result"`
	}
	for _, message := range messages {
		if message.Role != "tool" || len(message.Content) == 0 {
			continue
		}
		var parts []toolResultPart
		if err := json.Unmarshal(message.Content, &parts); err != nil {
			continue
		}
		for _, part := range parts {
			if part.Type != "tool-result" || !isCurrentReplyTool(part.ToolName) {
				continue
			}
			if successfulCurrentReplyResult(part.Result, command) {
				return true
			}
		}
	}
	return false
}

func isCurrentReplyTool(name string) bool {
	name = strings.TrimSpace(name)
	return name == "send" || name == "send_message"
}

func successfulCurrentReplyResult(result map[string]any, command turn.StartTurnCommand) bool {
	if delivered, present := result["ok"].(bool); present && !delivered {
		return false
	}
	if platform := resultString(result, "platform"); platform != "" &&
		strings.TrimSpace(command.CurrentChannel) != "" &&
		!strings.EqualFold(platform, strings.TrimSpace(command.CurrentChannel)) {
		return false
	}
	if target := resultString(result, "target"); target != "" &&
		strings.TrimSpace(command.ReplyTarget) != "" &&
		target != strings.TrimSpace(command.ReplyTarget) {
		return false
	}
	return true
}

func resultString(result map[string]any, key string) string {
	value, _ := result[key].(string)
	return strings.TrimSpace(value)
}
