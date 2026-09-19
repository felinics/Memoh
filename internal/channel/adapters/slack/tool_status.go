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
		s.toolStatus = channel.NewToolCallStatusTracker(s.newToolStatusMessage)
	}
	listed := s.toolStatus.Update(ctx, eventType, tc)
	if !channel.UsesToolCallStatusMessage(tc) {
		s.finishToolStatus(ctx)
		if eventType == channel.StreamEventToolCallEnd {
			return s.sendToolCallMessage(ctx, tc, channel.BuildToolCallEnd(tc))
		}
		return s.pushToolCallStart(ctx, tc)
	}
	if listed {
		return nil
	}
	if !s.toolStatus.Active() {
		if err := s.flushBufferedText(ctx); err != nil {
			return err
		}
	}
	s.toolStatus.Join(ctx, eventType, tc)
	return nil
}

// finishToolStatus ends the current tool batch before anything else is posted,
// so the status message keeps its place above what follows. A failure only
// costs the status update; the caller still delivers its own message.
func (s *slackOutboundStream) finishToolStatus(ctx context.Context) {
	if err := s.toolStatus.Finish(ctx); err != nil && s.adapter != nil && s.adapter.logger != nil {
		s.adapter.logger.Warn("slack: finish tool status failed",
			slog.String("config_id", s.cfg.ID),
			slog.String("target", s.target),
			slog.Any("error", err),
		)
	}
}

func (s *slackOutboundStream) newToolStatusMessage() *channel.ToolCallStatusMessage {
	editor := &slackToolStatusEditor{stream: s}
	opts := channel.ToolCallStatusOptions{
		Publish:     editor.publish,
		MinInterval: s.toolStatusInterval,
	}
	if s.adapter != nil {
		opts.Logger = s.adapter.logger
	}
	return channel.NewToolCallStatusMessage(opts)
}

// slackToolStatusEditor owns the status message of one tool batch: it posts
// the message on the first publish and updates it afterwards. The status
// message never publishes concurrently, so no locking is needed.
type slackToolStatusEditor struct {
	stream   *slackOutboundStream
	ts       string
	lastText string
}

func (e *slackToolStatusEditor) publish(ctx context.Context, snapshot channel.ToolCallStatusSnapshot) error {
	text := truncateSlackText(strings.TrimSpace(snapshot.RenderMarkdown(slackToolStatusMaxRunes)))
	if text == "" || (e.ts != "" && text == e.lastText) {
		return nil
	}
	s := e.stream
	body := slackDefaultBody(text)
	if e.ts != "" {
		var err error
		if snapshot.Final {
			err = s.updateMessageTextWithRetry(ctx, e.ts, body, nil)
		} else {
			err = s.updateMessageText(ctx, e.ts, body, nil)
		}
		if err == nil {
			e.lastText = text
			return nil
		}
		if delay, ok := slackRetryDelay(err); ok {
			return &channel.RetryAfterError{Err: err, Delay: delay}
		}
		if !isSlackMessageGone(err) {
			return err
		}
		// The status message was deleted or can no longer be edited. Carry the
		// batch on in a new message instead of losing its state.
		e.ts = ""
	}
	ts, err := s.postMessageWithRetry(ctx, body, nil)
	if err != nil {
		return err
	}
	e.ts, e.lastText = ts, text
	return nil
}

// isSlackMessageGone reports whether chat.update failed because the message no
// longer exists or can no longer be edited.
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
