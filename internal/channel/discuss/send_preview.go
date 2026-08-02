package discuss

import (
	"context"
	"encoding/hex"
	"log/slog"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	agentevent "github.com/memohai/memoh/internal/agent/event"
	"github.com/memohai/memoh/internal/channel"
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
		call.raw.WriteString(event.Delta)
		p.pushAvailableText(ctx, callID, call)
	case agentevent.ToolCallStart:
		if strings.TrimSpace(event.ToolName) != "send" || callID == "" {
			return
		}
		call := p.calls[callID]
		if call == nil {
			call = &discussSendPreviewCall{}
			p.calls[callID] = call
		}
		args, _ := event.Input.(map[string]any)
		platform, _ := args["platform"].(string)
		target, _ := args["target"].(string)
		if !sameDiscussConversation(p.cfg, platform, target) {
			call.disabled = true
			if call.opened {
				p.coordinator.Abort(ctx, p.key(callID))
			}
			return
		}
		text, _ := args["text"].(string)
		p.pushFinalArgumentTail(ctx, callID, call, text)
	case agentevent.ToolCallEnd:
		call := p.calls[callID]
		if call == nil {
			return
		}
		if strings.TrimSpace(event.Error) != "" && call.opened {
			p.coordinator.Abort(context.WithoutCancel(ctx), p.key(callID))
		}
		delete(p.calls, callID)
	case agentevent.AgentEnd, agentevent.AgentAbort, agentevent.Error:
		p.Abort(context.WithoutCancel(ctx))
	}
}

func (p *discussSendPreview) pushAvailableText(ctx context.Context, callID string, call *discussSendPreviewCall) {
	text, found := partialTopLevelJSONString(call.raw.String(), "text")
	if !found || len(text) <= call.emitted || strings.TrimSpace(text) == "" {
		return
	}
	if !call.opened && !p.open(ctx, callID, call) {
		return
	}
	delta := text[call.emitted:]
	if err := p.coordinator.PushDelta(ctx, p.key(callID), delta); err != nil {
		p.fail(ctx, callID, call, "push send argument preview", err)
		return
	}
	call.emitted = len(text)
}

func (p *discussSendPreview) pushFinalArgumentTail(ctx context.Context, callID string, call *discussSendPreviewCall, text string) {
	if call.disabled || len(text) <= call.emitted || strings.TrimSpace(text) == "" {
		return
	}
	if !call.opened && !p.open(ctx, callID, call) {
		return
	}
	if err := p.coordinator.PushDelta(ctx, p.key(callID), text[call.emitted:]); err != nil {
		p.fail(ctx, callID, call, "complete send argument preview", err)
		return
	}
	call.emitted = len(text)
}

func (p *discussSendPreview) open(ctx context.Context, callID string, call *discussSendPreviewCall) bool {
	target := strings.TrimSpace(p.cfg.ReplyTarget)
	messageID := strings.TrimSpace(p.cfg.SourceMessageID)
	reply := &channel.ReplyRef{Target: target, MessageID: messageID}
	stream, err := p.cfg.ReplySender.OpenStream(ctx, target, channel.StreamOptions{
		Reply:           reply,
		SourceMessageID: messageID,
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

func sameDiscussConversation(cfg DiscussSessionConfig, platform, target string) bool {
	platform = strings.TrimSpace(platform)
	target = strings.TrimSpace(target)
	if platform == "" {
		platform = strings.TrimSpace(cfg.CurrentPlatform)
	}
	if target == "" {
		target = strings.TrimSpace(cfg.ReplyTarget)
	}
	return strings.EqualFold(platform, strings.TrimSpace(cfg.CurrentPlatform)) &&
		target == strings.TrimSpace(cfg.ReplyTarget)
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
