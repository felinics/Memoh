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
		s.toolStatus = channel.NewToolCallStatusTracker(s.newToolStatusMessage)
	}
	listed := s.toolStatus.Update(ctx, eventType, tc)
	if !channel.UsesToolCallStatusMessage(tc) {
		s.finishToolStatus(ctx)
		if eventType == channel.StreamEventToolCallEnd {
			return s.pushToolCallEnd(ctx, tc)
		}
		return s.pushToolCallStart(ctx, tc)
	}
	if listed {
		return nil
	}
	if !s.toolStatus.Active() {
		s.flushBufferedText(ctx)
	}
	s.toolStatus.Join(ctx, eventType, tc)
	return nil
}

// finishToolStatus ends the current tool batch before anything else is posted,
// so the status card keeps its place above what follows. A failure only costs
// the status update; the caller still delivers its own message.
func (s *feishuOutboundStream) finishToolStatus(ctx context.Context) {
	if err := s.toolStatus.Finish(ctx); err != nil && s.adapter != nil && s.adapter.logger != nil {
		s.adapter.logger.Warn("feishu: finish tool status failed",
			slog.String("config_id", s.cfg.ID),
			slog.Any("error", err),
		)
	}
}

func (s *feishuOutboundStream) newToolStatusMessage() *channel.ToolCallStatusMessage {
	editor := &feishuToolStatusEditor{stream: s}
	opts := channel.ToolCallStatusOptions{
		Publish:     editor.publish,
		MinInterval: s.toolStatusInterval,
	}
	if s.adapter != nil {
		opts.Logger = s.adapter.logger
	}
	return channel.NewToolCallStatusMessage(opts)
}

// feishuToolStatusEditor owns the status card of one tool batch: it sends the
// card on the first publish and patches it afterwards. The status message never
// publishes concurrently, so no locking is needed.
type feishuToolStatusEditor struct {
	stream    *feishuOutboundStream
	messageID string
	lastText  string
}

func (e *feishuToolStatusEditor) publish(ctx context.Context, snapshot channel.ToolCallStatusSnapshot) error {
	text := strings.TrimSpace(snapshot.RenderMarkdown(feishuToolStatusMaxRunes))
	if text == "" || (e.messageID != "" && text == e.lastText) {
		return nil
	}
	s := e.stream
	if s.client == nil {
		return errors.New("feishu client not configured")
	}
	if e.messageID != "" {
		if err := s.patchToolCallCard(ctx, e.messageID, text); err != nil {
			return err
		}
		e.lastText = text
		return nil
	}
	messageID, err := s.sendToolCallCard(ctx, text)
	if err != nil {
		return err
	}
	e.messageID, e.lastText = messageID, text
	return nil
}
