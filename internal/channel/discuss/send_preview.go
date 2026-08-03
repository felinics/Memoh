package discuss

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	agentevent "github.com/memohai/memoh/internal/agent/event"
	"github.com/memohai/memoh/internal/channel"
	"github.com/memohai/memoh/internal/delivery"
	"github.com/memohai/memoh/internal/messaging"
)

type discussSendPreview struct {
	cfg         DiscussSessionConfig
	coordinator *channel.SendToolStreamCoordinator
	logger      *slog.Logger
	calls       map[string]*discussSendPreviewCall
}

type discussSendPreviewCall struct {
	raw      strings.Builder
	emitted  int
	opened   bool
	disabled bool
}

func newDiscussSendPreview(
	cfg DiscussSessionConfig,
	coordinator *channel.SendToolStreamCoordinator,
	logger *slog.Logger,
) *discussSendPreview {
	return &discussSendPreview{
		cfg: cfg, coordinator: coordinator, logger: logger,
		calls: make(map[string]*discussSendPreviewCall),
	}
}

func (p *discussSendPreview) Handle(ctx context.Context, event agentevent.StreamEvent) {
	if p == nil || p.coordinator == nil || p.cfg.ReplySender == nil ||
		!strings.EqualFold(strings.TrimSpace(p.cfg.CurrentPlatform), string(channel.ChannelTypeTelegram)) ||
		strings.TrimSpace(p.cfg.ReplyTarget) == "" {
		return
	}
	callID := strings.TrimSpace(event.ToolCallID)
	switch event.Type {
	case agentevent.ToolCallInputStart:
		if strings.TrimSpace(event.ToolName) == "send" && callID != "" {
			p.calls[callID] = &discussSendPreviewCall{}
		}
	case agentevent.ToolCallInputDelta:
		call := p.calls[callID]
		if call == nil || call.disabled || event.Delta == "" {
			return
		}
		// Route fields may arrive after text in streamed JSON. Buffer the input
		// until ToolCallStart provides the complete arguments; opening a current
		// chat preview earlier could leak a cross-conversation send.
		call.raw.WriteString(event.Delta)
	case agentevent.ToolCallStart:
		if strings.TrimSpace(event.ToolName) != "send" || callID == "" {
			return
		}
		call := p.calls[callID]
		if call == nil {
			call = &discussSendPreviewCall{}
			p.calls[callID] = call
		}
		args, complete := completeSendArguments(event.Input, call.raw.String())
		if !complete {
			call.disabled = true
			return
		}
		platform, _ := args["platform"].(string)
		target, _ := args["target"].(string)
		if !delivery.IsSameConversation(
			p.cfg.CurrentPlatform, p.cfg.ReplyTarget, platform, target,
		) {
			call.disabled = true
			return
		}
		replyTo, replyErr := messaging.ReplyMessageIDFromArgs(args)
		if replyErr != nil {
			// The actual send path will return the schema/validation error. Do not
			// open a preview whose Telegram reply relationship could be wrong.
			call.disabled = true
			return
		}
		text, _ := args["text"].(string)
		if text == "" {
			text, _ = partialTopLevelJSONString(call.raw.String(), "text")
		}
		p.pushFinalArgumentTail(ctx, callID, call, text, replyTo)
	case agentevent.ToolCallEnd:
		call := p.calls[callID]
		if call == nil {
			return
		}
		if call.opened {
			p.coordinator.Abort(context.WithoutCancel(ctx), p.key(callID))
		}
		delete(p.calls, callID)
	case agentevent.AgentEnd, agentevent.AgentAbort, agentevent.Error:
		p.Abort(context.WithoutCancel(ctx))
	}
}

func completeSendArguments(input any, raw string) (map[string]any, bool) {
	inputArgs, _ := input.(map[string]any)
	args := make(map[string]any, len(inputArgs))
	for key, value := range inputArgs {
		args[key] = value
	}
	if strings.TrimSpace(raw) == "" {
		return args, true
	}
	var streamed map[string]any
	if json.Unmarshal([]byte(raw), &streamed) != nil {
		return args, false
	}
	for _, key := range []string{"platform", "target"} {
		inputValue, inputPresent, inputValid := sendRouteValue(inputArgs, key)
		streamedValue, streamedPresent, streamedValid := sendRouteValue(streamed, key)
		if !inputValid || !streamedValid {
			return args, false
		}
		if inputValue != "" && streamedValue != "" {
			matches := inputValue == streamedValue
			if key == "platform" {
				matches = strings.EqualFold(inputValue, streamedValue)
			}
			if !matches {
				return args, false
			}
		}
		// A non-empty route is more restrictive than an omitted/defaulted one.
		// Preserve it regardless of which event representation carried it.
		if streamedPresent && streamedValue != "" && (!inputPresent || inputValue == "") {
			args[key] = streamedValue
		}
	}
	for key, value := range streamed {
		if _, exists := args[key]; !exists {
			args[key] = value
		}
	}
	return args, true
}

func sendRouteValue(args map[string]any, key string) (string, bool, bool) {
	value, present := args[key]
	if !present {
		return "", false, true
	}
	text, ok := value.(string)
	if !ok {
		return "", true, false
	}
	return strings.TrimSpace(text), true, true
}

func (p *discussSendPreview) pushFinalArgumentTail(
	ctx context.Context,
	callID string,
	call *discussSendPreviewCall,
	text string,
	replyTo string,
) {
	if call.disabled || len(text) <= call.emitted || strings.TrimSpace(text) == "" {
		return
	}
	if !call.opened && !p.open(ctx, callID, call, replyTo) {
		return
	}
	if err := p.coordinator.PushDelta(ctx, p.key(callID), text[call.emitted:]); err != nil {
		p.fail(ctx, callID, call, "complete send argument preview", err)
		return
	}
	call.emitted = len(text)
}

func (p *discussSendPreview) open(
	ctx context.Context,
	callID string,
	call *discussSendPreviewCall,
	replyTo string,
) bool {
	target := strings.TrimSpace(p.cfg.ReplyTarget)
	sourceMessageID := strings.TrimSpace(p.cfg.SourceMessageID)
	var reply *channel.ReplyRef
	if replyMessageID := strings.TrimSpace(replyTo); replyMessageID != "" {
		reply = &channel.ReplyRef{Target: target, MessageID: replyMessageID}
	}
	stream, err := p.cfg.ReplySender.OpenStream(ctx, target, channel.StreamOptions{
		Reply:           reply,
		SourceMessageID: sourceMessageID,
		Metadata: map[string]any{
			"route_id":          strings.TrimSpace(p.cfg.RouteID),
			"conversation_type": strings.TrimSpace(p.cfg.ConversationType),
			"tool_call_id":      callID,
		},
	})
	if err != nil {
		p.fail(ctx, callID, call, "open send argument preview", err)
		return false
	}
	if err := stream.Push(ctx, channel.StreamEvent{Type: channel.StreamEventStatus, Status: channel.StreamStatusStarted}); err != nil {
		_ = stream.Close(context.WithoutCancel(ctx))
		p.fail(ctx, callID, call, "start send argument preview", err)
		return false
	}
	if !p.coordinator.Attach(p.key(callID), stream) {
		_ = stream.Close(context.WithoutCancel(ctx))
		call.disabled = true
		return false
	}
	call.opened = true
	return true
}

func (p *discussSendPreview) Abort(ctx context.Context) {
	if p == nil || p.coordinator == nil {
		return
	}
	for callID, call := range p.calls {
		if call.opened {
			p.coordinator.Abort(ctx, p.key(callID))
		}
		delete(p.calls, callID)
	}
}

func (p *discussSendPreview) key(callID string) channel.SendToolStreamKey {
	return channel.SendToolStreamKey{
		BotID: p.cfg.BotID, Platform: channel.ChannelTypeTelegram,
		Target: p.cfg.ReplyTarget, ToolCallID: callID,
	}
}

func (p *discussSendPreview) fail(ctx context.Context, callID string, call *discussSendPreviewCall, operation string, err error) {
	call.disabled = true
	if call.opened {
		p.coordinator.Abort(context.WithoutCancel(ctx), p.key(callID))
	}
	if p.logger != nil {
		p.logger.Warn("discuss Telegram send preview failed",
			slog.String("operation", operation), slog.Any("error", err))
	}
}

// partialTopLevelJSONString decodes the currently available prefix of a
// top-level JSON string field. Incomplete escapes are withheld until the next
// delta so Telegram never receives malformed UTF-8 or half of a \u escape.
func partialTopLevelJSONString(raw, field string) (string, bool) {
	depth := 0
	for i := 0; i < len(raw); {
		switch raw[i] {
		case '{':
			depth++
			i++
		case '}':
			depth--
			i++
		case '"':
			value, end, complete := decodeJSONStringPrefix(raw, i)
			if !complete {
				return "", false
			}
			if depth == 1 && value == field {
				j := end
				for j < len(raw) && (raw[j] == ' ' || raw[j] == '\n' || raw[j] == '\r' || raw[j] == '\t') {
					j++
				}
				if j >= len(raw) || raw[j] != ':' {
					i = end
					continue
				}
				j++
				for j < len(raw) && (raw[j] == ' ' || raw[j] == '\n' || raw[j] == '\r' || raw[j] == '\t') {
					j++
				}
				if j >= len(raw) || raw[j] != '"' {
					return "", false
				}
				text, _, _ := decodeJSONStringPrefix(raw, j)
				return text, true
			}
			i = end
		default:
			i++
		}
	}
	return "", false
}

func decodeJSONStringPrefix(raw string, quote int) (string, int, bool) {
	if quote >= len(raw) || raw[quote] != '"' {
		return "", quote, false
	}
	var out strings.Builder
	for i := quote + 1; i < len(raw); {
		if raw[i] == '"' {
			return out.String(), i + 1, true
		}
		if raw[i] != '\\' {
			_, size := utf8.DecodeRuneInString(raw[i:])
			if size == 0 || i+size > len(raw) {
				return out.String(), len(raw), false
			}
			out.WriteString(raw[i : i+size])
			i += size
			continue
		}
		if i+1 >= len(raw) {
			return out.String(), len(raw), false
		}
		switch raw[i+1] {
		case '"', '\\', '/':
			out.WriteByte(raw[i+1])
			i += 2
		case 'b':
			out.WriteByte('\b')
			i += 2
		case 'f':
			out.WriteByte('\f')
			i += 2
		case 'n':
			out.WriteByte('\n')
			i += 2
		case 'r':
			out.WriteByte('\r')
			i += 2
		case 't':
			out.WriteByte('\t')
			i += 2
		case 'u':
			if i+6 > len(raw) {
				return out.String(), len(raw), false
			}
			decoded, err := hex.DecodeString(raw[i+2 : i+6])
			if err != nil {
				return out.String(), i + 6, false
			}
			r := rune(decoded[0])<<8 | rune(decoded[1])
			i += 6
			if utf16.IsSurrogate(r) {
				if i+6 > len(raw) || raw[i:i+2] != `\u` {
					return out.String(), len(raw), false
				}
				lowBytes, lowErr := hex.DecodeString(raw[i+2 : i+6])
				if lowErr != nil {
					return out.String(), i + 6, false
				}
				low := rune(lowBytes[0])<<8 | rune(lowBytes[1])
				r = utf16.DecodeRune(r, low)
				i += 6
			}
			out.WriteRune(r)
		default:
			return out.String(), i + 2, false
		}
	}
	return out.String(), len(raw), false
}
