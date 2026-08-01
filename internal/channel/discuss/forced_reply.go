package discuss

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/memohai/memoh/internal/agent/turn"
	"github.com/memohai/memoh/internal/channel"
)

const discussReplyFallbackTimeout = 30 * time.Second

func (*DiscussDriver) sendReplyFallback(
	ctx context.Context,
	cfg DiscussSessionConfig,
	outcome discussRunOutcome,
	log *slog.Logger,
) {
	if !cfg.ForceReply ||
		!strings.EqualFold(strings.TrimSpace(cfg.CurrentPlatform), string(channel.ChannelTypeTelegram)) ||
		!outcome.completed || outcome.failed || outcome.currentReplySent {
		return
	}
	if cfg.ReplySender == nil || strings.TrimSpace(cfg.ReplyTarget) == "" {
		log.Warn("discuss reply fallback unavailable")
		return
	}
	message := latestReplyMessage(outcome.finalMessages, cfg.CurrentPlatform)
	if message.IsEmpty() {
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, discussReplyFallbackTimeout)
	defer cancel()
	if err := cfg.ReplySender.Send(sendCtx, channel.OutboundMessage{
		Target:  strings.TrimSpace(cfg.ReplyTarget),
		Message: message,
	}); err != nil {
		log.Error("discuss reply fallback failed", slog.Any("error", err))
		return
	}
	log.Info("sent discuss reply fallback")
}

func latestReplyMessage(messages []turn.ModelMessage, platform string) channel.Message {
	outputs := turn.ExtractAssistantOutputs(messages)
	for i := len(outputs) - 1; i >= 0; i-- {
		if message := replyMessage(outputs[i].Content, platform); !message.IsEmpty() {
			return message
		}
	}
	// Some providers put visible text and a tool call in the same assistant
	// message. ExtractAssistantOutputs intentionally skips those for ordinary
	// reply rendering, but the text remains the best deterministic fallback
	// when a discuss turn never called the messaging tool.
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "assistant" {
			continue
		}
		if message := replyMessage(messages[i].TextContent(), platform); !message.IsEmpty() {
			return message
		}
	}
	return channel.Message{}
}

func replyMessage(text, platform string) channel.Message {
	text = strings.TrimSpace(text)
	if text == "" || channel.IsSilentReplyText(text) {
		return channel.Message{}
	}
	message := channel.Message{Text: text}
	if strings.EqualFold(strings.TrimSpace(platform), string(channel.ChannelTypeTelegram)) && channel.ContainsMarkdown(text) {
		message.Format = channel.MessageFormatMarkdown
	}
	return message
}
