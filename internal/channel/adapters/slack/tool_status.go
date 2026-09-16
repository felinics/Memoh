package slack

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	slackapi "github.com/slack-go/slack"

	"github.com/felinics/memoh/internal/channel"
)

// slackToolStatusMaxRunes keeps the status message short enough to read at a
// glance while leaving room for long batches.
const slackToolStatusMaxRunes = 3000

// pushToolCallReusingMessage routes a tool event when the bot reuses one
// message per tool batch. Ordinary calls join the batch status message;
// approval and user-input prompts end the batch and keep their own message.
func (s *slackOutboundStream) pushToolCallReusingMessage(ctx context.Context, eventType channel.StreamEventType, tc *channel.StreamToolCall) error {
	if s.toolStatus == nil {
		s.toolStatus = channel.NewToolCallStatusTracker(s.toolStatusLogger(), s.newToolStatusMessage)
	}
	return s.toolStatus.Route(ctx, channel.ToolCallStatusHooks{
		FlushText: s.flushBufferedText,
		PushCall: func(ctx context.Context, eventType channel.StreamEventType, tc *channel.StreamToolCall) error {
			if eventType == channel.StreamEventToolCallEnd {
				return s.sendToolCallMessage(ctx, tc, channel.BuildToolCallEnd(tc))
			}
			return s.pushToolCallStart(ctx, tc)
		},
	}, eventType, tc)
}

// newToolStatusMessage posts the status message of a batch on its first publish
// and updates it afterwards.
func (s *slackOutboundStream) newToolStatusMessage() *channel.ToolCallStatusMessage {
	editor := &channel.StatusMessageEditor{
		Send: func(ctx context.Context, text string) (string, error) {
			return s.postMessageWithRetry(ctx, slackDefaultBody(text), nil)
		},
		Edit: func(ctx context.Context, ts, text string, final bool) error {
			if final {
				return s.updateMessageTextWithRetry(ctx, ts, slackDefaultBody(text), nil)
			}
			return s.updateMessageText(ctx, ts, slackDefaultBody(text), nil)
		},
		Gone: isSlackMessageGone,
	}
	return channel.NewToolCallStatusMessage(channel.ToolCallStatusOptions{
		Publish: func(ctx context.Context, snapshot channel.ToolCallStatusSnapshot) error {
			text := truncateSlackText(strings.TrimSpace(snapshot.RenderMarkdown(slackToolStatusMaxRunes)))
			return editor.Publish(ctx, text, snapshot.Final)
		},
		MinInterval: s.toolStatusInterval,
		Logger:      s.toolStatusLogger(),
	})
}

func (s *slackOutboundStream) toolStatusLogger() *slog.Logger {
	if s.adapter == nil {
		return nil
	}
	return s.adapter.logger
}

// isSlackMessageGone reports whether chat.update failed because the message no
// longer exists or can no longer be edited, so the batch carries on in a new
// message instead of losing its state.
func isSlackMessageGone(err error) bool {
	var apiErr slackapi.SlackErrorResponse
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Err {
	case "message_not_found", "cant_update_message", "edit_window_closed":
		return true
	default:
		return false
	}
}
