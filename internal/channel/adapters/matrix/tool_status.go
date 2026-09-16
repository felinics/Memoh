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

// newToolStatusMessage sends the status message of a batch on its first publish
// and replaces it with m.replace edits afterwards.
func (s *matrixOutboundStream) newToolStatusMessage() *channel.ToolCallStatusMessage {
	var roomID string
	room := func(ctx context.Context) (string, error) {
		if roomID == "" {
			resolved, err := s.adapter.resolveRoomTarget(ctx, s.cfg, s.target)
			if err != nil {
				return "", err
			}
			roomID = resolved
		}
		return roomID, nil
	}
	editor := &channel.StatusMessageEditor{
		Send: func(ctx context.Context, text string) (string, error) {
			id, err := room(ctx)
			if err != nil {
				return "", err
			}
			msg := channel.Message{Text: text, Format: channel.MessageFormatMarkdown, Reply: s.reply}
			return s.adapter.sendTextEvent(ctx, s.cfg, id, buildMatrixMessageContent(msg, false, ""))
		},
		Edit: func(ctx context.Context, eventID, text string, _ bool) error {
			id, err := room(ctx)
			if err != nil {
				return err
			}
			msg := channel.Message{Text: text, Format: channel.MessageFormatMarkdown}
			_, err = s.adapter.sendTextEvent(ctx, s.cfg, id, buildMatrixMessageContent(msg, true, eventID))
			return err
		},
	}
	return channel.NewToolCallStatusMessage(channel.ToolCallStatusOptions{
		Publish: func(ctx context.Context, snapshot channel.ToolCallStatusSnapshot) error {
			return editor.Publish(ctx, strings.TrimSpace(snapshot.RenderMarkdown(matrixToolStatusMaxRunes)), snapshot.Final)
		},
		MinInterval: s.toolStatusInterval,
		Logger:      s.toolStatusLogger(),
	})
}

func (s *matrixOutboundStream) toolStatusLogger() *slog.Logger {
	if s.adapter == nil {
		return nil
	}
	return s.adapter.logger
}
