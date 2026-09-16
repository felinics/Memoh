package feishu

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/felinics/memoh/internal/channel"
)

// feishuToolStatusMaxRunes matches the streamed-text card budget.
const feishuToolStatusMaxRunes = feishuStreamMaxRunes

// pushToolCallReusingMessage routes a tool event when the bot reuses one card
// per tool batch. Ordinary calls join the batch status card; approval and
// user-input prompts end the batch and keep their own card.
func (s *feishuOutboundStream) pushToolCallReusingMessage(ctx context.Context, eventType channel.StreamEventType, tc *channel.StreamToolCall) error {
	if s.toolStatus == nil {
		s.toolStatus = channel.NewToolCallStatusTracker(s.toolStatusLogger(), s.newToolStatusMessage)
	}
	return s.toolStatus.Route(ctx, channel.ToolCallStatusHooks{
		FlushText: func(ctx context.Context) error {
			s.flushBufferedText(ctx)
			return nil
		},
		PushCall: func(ctx context.Context, eventType channel.StreamEventType, tc *channel.StreamToolCall) error {
			if eventType == channel.StreamEventToolCallEnd {
				return s.pushToolCallEnd(ctx, tc)
			}
			return s.pushToolCallStart(ctx, tc)
		},
	}, eventType, tc)
}

// newToolStatusMessage sends the status card of a batch on its first publish
// and patches it afterwards.
func (s *feishuOutboundStream) newToolStatusMessage() *channel.ToolCallStatusMessage {
	editor := &channel.StatusMessageEditor{
		Send: func(ctx context.Context, text string) (string, error) {
			if s.client == nil {
				return "", errors.New("feishu client not configured")
			}
			return s.sendToolCallCard(ctx, text)
		},
		Edit: func(ctx context.Context, messageID, text string, _ bool) error {
			if s.client == nil {
				return errors.New("feishu client not configured")
			}
			return s.patchToolCallCard(ctx, messageID, text)
		},
	}
	return channel.NewToolCallStatusMessage(channel.ToolCallStatusOptions{
		Publish: func(ctx context.Context, snapshot channel.ToolCallStatusSnapshot) error {
			return editor.Publish(ctx, strings.TrimSpace(snapshot.RenderMarkdown(feishuToolStatusMaxRunes)), snapshot.Final)
		},
		MinInterval: s.toolStatusInterval,
		Logger:      s.toolStatusLogger(),
	})
}

func (s *feishuOutboundStream) toolStatusLogger() *slog.Logger {
	if s.adapter == nil {
		return nil
	}
	return s.adapter.logger
}
