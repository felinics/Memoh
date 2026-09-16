package matrix

import (
	"context"
	"log/slog"
	"strings"

	"github.com/felinics/memoh/internal/channel"
)

// matrixToolStatusMaxRunes keeps the status message readable and far below the
// Matrix event size limit.
const matrixToolStatusMaxRunes = 8000

// pushToolCallReusingMessage routes a tool event when the bot reuses one
// message per tool batch. Ordinary calls join the batch status message;
// approval and user-input prompts end the batch and keep their own message.
func (s *matrixOutboundStream) pushToolCallReusingMessage(ctx context.Context, eventType channel.StreamEventType, tc *channel.StreamToolCall) error {
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
func (s *matrixOutboundStream) finishToolStatus(ctx context.Context) {
	if err := s.toolStatus.Finish(ctx); err != nil && s.adapter != nil && s.adapter.logger != nil {
		s.adapter.logger.Warn("matrix: finish tool status failed",
			slog.String("target", s.target),
			slog.Any("error", err),
		)
	}
}

func (s *matrixOutboundStream) newToolStatusMessage() *channel.ToolCallStatusMessage {
	editor := &matrixToolStatusEditor{stream: s}
	opts := channel.ToolCallStatusOptions{
		Publish:     editor.publish,
		MinInterval: s.toolStatusInterval,
	}
	if s.adapter != nil {
		opts.Logger = s.adapter.logger
	}
	return channel.NewToolCallStatusMessage(opts)
}

// matrixToolStatusEditor owns the status message of one tool batch: it sends
// the event on the first publish and replaces it with m.replace edits after.
// The status message never publishes concurrently, so no locking is needed.
type matrixToolStatusEditor struct {
	stream   *matrixOutboundStream
	roomID   string
	eventID  string
	lastText string
}

func (e *matrixToolStatusEditor) publish(ctx context.Context, snapshot channel.ToolCallStatusSnapshot) error {
	text := strings.TrimSpace(snapshot.RenderMarkdown(matrixToolStatusMaxRunes))
	if text == "" || (e.eventID != "" && text == e.lastText) {
		return nil
	}
	s := e.stream
	if e.roomID == "" {
		roomID, err := s.adapter.resolveRoomTarget(ctx, s.cfg, s.target)
		if err != nil {
			return err
		}
		e.roomID = roomID
	}
	msg := channel.Message{Text: text, Format: channel.MessageFormatMarkdown}
	if e.eventID != "" {
		if _, err := s.adapter.sendTextEvent(ctx, s.cfg, e.roomID, buildMatrixMessageContent(msg, true, e.eventID)); err != nil {
			return err
		}
		e.lastText = text
		return nil
	}
	msg.Reply = s.reply
	eventID, err := s.adapter.sendTextEvent(ctx, s.cfg, e.roomID, buildMatrixMessageContent(msg, false, ""))
	if err != nil {
		return err
	}
	e.eventID, e.lastText = eventID, text
	return nil
}
