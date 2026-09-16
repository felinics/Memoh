package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	tele "gopkg.in/telebot.v4"

	"github.com/felinics/memoh/internal/channel"
)

const (
	// telegramToolStatusDraftID keeps the tool status draft apart from the
	// streamed-text draft, so switching between them replaces the preview
	// instead of animating one text into the other.
	telegramToolStatusDraftID = 2
	// telegramToolStatusDraftKeepAlive refreshes the status draft well inside
	// Telegram's 30-second draft lifetime while a long tool runs.
	telegramToolStatusDraftKeepAlive = 15 * time.Second
	// telegramToolStatusEditInterval keeps status edits inside the group budget
	// of about 20 messages a minute.
	telegramToolStatusEditInterval = 3 * time.Second
	// telegramToolStatusMaxRunes budgets the Markdown source. The converted HTML
	// must fit the message limit as well, see renderTelegramToolStatus.
	telegramToolStatusMaxRunes = 3500
)

// pushToolCallReusingMessage routes a tool event when the bot reuses one
// message per tool batch. Ordinary calls join the batch status message.
// Approval and user-input prompts end the batch and keep their own card, and
// the result of a call that owns a card lands on that card as well.
func (s *telegramOutboundStream) pushToolCallReusingMessage(ctx context.Context, eventType channel.StreamEventType, tc *channel.StreamToolCall) error {
	if s.toolStatus == nil {
		s.toolStatus = channel.NewToolCallStatusTracker(s.newToolStatusMessage)
	}
	listed := s.toolStatus.Update(ctx, eventType, tc)
	switch {
	case !channel.UsesToolCallStatusMessage(tc):
		s.finishToolStatus(ctx)
		if eventType == channel.StreamEventToolCallEnd {
			return s.pushToolCallEnd(ctx, tc)
		}
		return s.pushToolCallStart(ctx, tc)
	case eventType == channel.StreamEventToolCallEnd && s.hasToolCallCard(tc):
		return s.pushToolCallEnd(ctx, tc)
	case listed:
		return nil
	}
	if !s.toolStatus.Active() {
		s.flushBufferedText(ctx)
	}
	s.toolStatus.Join(ctx, eventType, tc)
	return nil
}

func (s *telegramOutboundStream) hasToolCallCard(tc *channel.StreamToolCall) bool {
	if tc == nil || strings.TrimSpace(tc.CallID) == "" {
		return false
	}
	_, ok := s.lookupToolCallMessage(strings.TrimSpace(tc.CallID))
	return ok
}

// finishToolStatus ends the current tool batch before anything else is posted,
// so the status message keeps its place above what follows. A failure only
// costs the status update; the caller still delivers its own message.
func (s *telegramOutboundStream) finishToolStatus(ctx context.Context) {
	if err := s.toolStatus.Finish(ctx); err != nil && s.adapter != nil && s.adapter.logger != nil {
		s.adapter.logger.Warn("telegram: finish tool status failed",
			slog.String("config_id", s.cfg.ID),
			slog.Any("error", err),
		)
	}
}

// newToolStatusMessage starts the status message of a new tool batch. Private
// chats show it as a draft preview that the next draft or message replaces, so
// tool progress never lingers in the chat; groups cannot receive drafts and get
// one message that is edited in place.
func (s *telegramOutboundStream) newToolStatusMessage() *channel.ToolCallStatusMessage {
	opts := channel.ToolCallStatusOptions{}
	if s.adapter != nil {
		opts.Logger = s.adapter.logger
	}
	if s.isPrivateChat {
		s.mu.Lock()
		chatID := s.streamChatID
		s.mu.Unlock()
		opts.Publish = func(ctx context.Context, snapshot channel.ToolCallStatusSnapshot) error {
			return s.publishToolStatusDraft(ctx, chatID, snapshot)
		}
		opts.MinInterval = telegramDraftThrottle
		opts.KeepAlive = s.toolStatusKeepAlive
		opts.Ephemeral = true
	} else {
		editor := &telegramToolStatusEditor{stream: s}
		opts.Publish = editor.publish
		opts.MinInterval = s.toolStatusInterval
	}
	return channel.NewToolCallStatusMessage(opts)
}

func (s *telegramOutboundStream) publishToolStatusDraft(ctx context.Context, chatID int64, snapshot channel.ToolCallStatusSnapshot) error {
	text, parseMode := renderTelegramToolStatus(snapshot)
	if text == "" {
		return nil
	}
	if err := s.adapter.waitStreamLimit(ctx); err != nil {
		return err
	}
	bot, err := s.getBot(ctx)
	if err != nil {
		return err
	}
	return telegramRetryAfter(sendTelegramDraft(bot, chatID, telegramToolStatusDraftID, text, parseMode))
}

// telegramToolStatusEditor owns the group-chat status message of one tool
// batch: it sends the message on the first publish and edits it afterwards.
// The status message never publishes concurrently, so no locking is needed.
type telegramToolStatusEditor struct {
	stream        *telegramOutboundStream
	chatID        int64
	messageID     int
	lastText      string
	lastParseMode string
}

func (e *telegramToolStatusEditor) publish(ctx context.Context, snapshot channel.ToolCallStatusSnapshot) error {
	text, parseMode := renderTelegramToolStatus(snapshot)
	if text == "" || (e.messageID != 0 && text == e.lastText && parseMode == e.lastParseMode) {
		return nil
	}
	bot, replyTo, err := e.stream.getBotAndReply(ctx)
	if err != nil {
		return err
	}
	if e.messageID != 0 {
		err := e.edit(ctx, bot, text, parseMode, snapshot.Final)
		if err == nil {
			e.lastText, e.lastParseMode = text, parseMode
			return nil
		}
		if !isTelegramEditUnrecoverable(err) {
			return err
		}
		// The status message was deleted or aged out of editing. Carry the
		// batch on in a new message instead of losing its state.
		e.messageID = 0
	}
	if err := e.stream.adapter.waitStreamLimit(ctx); err != nil {
		return err
	}
	chatID, messageID, err := sendTelegramTextReturnMessage(bot, e.stream.target, text, replyTo, parseMode)
	if err != nil {
		return telegramRetryAfter(err)
	}
	e.chatID, e.messageID = chatID, messageID
	e.lastText, e.lastParseMode = text, parseMode
	return nil
}

// edit replaces the status text. An intermediate update gives up on a flood
// error and lets the status message retry after the server's backoff; a final
// update waits the backoff out so a batch never ends on a stale state.
func (e *telegramToolStatusEditor) edit(ctx context.Context, bot *tele.Bot, text, parseMode string, final bool) error {
	attempts := 1
	if final {
		attempts = telegramFinalEditMaxRetries
	}
	var lastErr error
	for attempt := range attempts {
		if err := e.stream.adapter.waitStreamLimit(ctx); err != nil {
			return err
		}
		err := rawEditTelegramMessageText(bot, e.chatID, e.messageID, text, parseMode)
		if err == nil || isTelegramMessageNotModified(err) {
			return nil
		}
		if !isTelegramTooManyRequests(err) {
			return err
		}
		if !final {
			return telegramRetryAfter(err)
		}
		lastErr = err
		delay := getTelegramRetryAfter(err)
		if delay <= 0 {
			delay = time.Duration(attempt+1) * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return fmt.Errorf("telegram: final tool status edit failed after %d attempts: %w", attempts, lastErr)
}

// telegramRetryAfter tags a flood error with the server's backoff so the status
// message waits before its next update.
func telegramRetryAfter(err error) error {
	if err == nil || !isTelegramTooManyRequests(err) {
		return err
	}
	return &channel.RetryAfterError{Err: err, Delay: getTelegramRetryAfter(err)}
}

// renderTelegramToolStatus renders a snapshot as Telegram HTML. Outbound text
// is cut at the message limit, which could split a tag, so the Markdown budget
// shrinks until the converted markup fits whole.
func renderTelegramToolStatus(snapshot channel.ToolCallStatusSnapshot) (string, string) {
	budget := telegramToolStatusMaxRunes
	for range 4 {
		text, parseMode := formatTelegramOutput(snapshot.RenderMarkdown(budget), channel.MessageFormatMarkdown)
		if parseMode == "" {
			text = snapshot.RenderPlain(budget)
		}
		text = strings.TrimSpace(text)
		if runeLenTelegramText(text) <= telegramMaxMessageLength {
			return text, parseMode
		}
		budget = budget * 3 / 4
	}
	return strings.TrimSpace(snapshot.RenderPlain(telegramMaxMessageLength)), ""
}
