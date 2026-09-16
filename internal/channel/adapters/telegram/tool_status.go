package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
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
	// telegramToolStatusEditInterval keeps status edits inside the group budget
	// of about 20 messages a minute.
	telegramToolStatusEditInterval = 3 * time.Second
	// telegramToolStatusDraftKeepAlive refreshes the status draft well inside
	// Telegram's 30-second draft lifetime while a long tool runs.
	telegramToolStatusDraftKeepAlive = 15 * time.Second
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
		HasCard: s.hasToolCallCard,
	}, eventType, tc)
}

func (s *telegramOutboundStream) hasToolCallCard(tc *channel.StreamToolCall) bool {
	if tc == nil || strings.TrimSpace(tc.CallID) == "" {
		return false
	}
	_, ok := s.lookupToolCallMessage(strings.TrimSpace(tc.CallID))
	return ok
}

// newToolStatusMessage starts the status message of a new tool batch. Private
// chats show it as a draft preview that the next draft or message replaces, so
// tool progress never lingers in the chat; groups cannot receive drafts and get
// one message that is edited in place.
func (s *telegramOutboundStream) newToolStatusMessage() *channel.ToolCallStatusMessage {
	opts := channel.ToolCallStatusOptions{
		MinInterval: s.toolStatusInterval,
		Logger:      s.toolStatusLogger(),
	}
	if s.isPrivateChat {
		s.mu.Lock()
		chatID := s.streamChatID
		s.mu.Unlock()
		opts.MinInterval = telegramDraftThrottle
		opts.KeepAlive = s.toolStatusKeepAlive
		opts.Ephemeral = true
		opts.Publish = func(ctx context.Context, snapshot channel.ToolCallStatusSnapshot) error {
			text, parseMode := renderTelegramToolStatus(snapshot)
			if text == "" {
				return nil
			}
			bot, err := s.toolStatusBot(ctx)
			if err != nil {
				return err
			}
			return sendTelegramDraft(bot, chatID, telegramToolStatusDraftID, text, parseMode)
		}
		return channel.NewToolCallStatusMessage(opts)
	}

	var (
		chatID    int64
		parseMode string
	)
	editor := &channel.StatusMessageEditor{
		Send: func(ctx context.Context, text string) (string, error) {
			bot, replyTo, err := s.getBotAndReply(ctx)
			if err != nil {
				return "", err
			}
			if err := s.adapter.waitStreamLimit(ctx); err != nil {
				return "", err
			}
			chat, messageID, err := sendTelegramTextReturnMessage(bot, s.target, text, replyTo, parseMode)
			if err != nil {
				return "", err
			}
			chatID = chat
			return strconv.Itoa(messageID), nil
		},
		Edit: func(ctx context.Context, id, text string, final bool) error {
			bot, err := s.toolStatusBot(ctx)
			if err != nil {
				return err
			}
			messageID, err := strconv.Atoi(id)
			if err != nil {
				return err
			}
			return s.editToolStatus(ctx, bot, chatID, messageID, text, parseMode, final)
		},
		// The status message was deleted or aged out of editing. Carry the batch
		// on in a new message instead of losing its state.
		Gone: isTelegramEditUnrecoverable,
	}
	opts.Publish = func(ctx context.Context, snapshot channel.ToolCallStatusSnapshot) error {
		text, mode := renderTelegramToolStatus(snapshot)
		parseMode = mode
		return editor.Publish(ctx, text, snapshot.Final)
	}
	return channel.NewToolCallStatusMessage(opts)
}

// editToolStatus replaces the status text. An intermediate update gives up on a
// flood error and retries with the next tool event; a final update waits the
// backoff out so a batch never ends on a stale state.
func (s *telegramOutboundStream) editToolStatus(ctx context.Context, bot *tele.Bot, chatID int64, messageID int, text, parseMode string, final bool) error {
	attempts := 1
	if final {
		attempts = telegramFinalEditMaxRetries
	}
	var lastErr error
	for attempt := range attempts {
		if err := s.adapter.waitStreamLimit(ctx); err != nil {
			return err
		}
		err := rawEditTelegramMessageText(bot, chatID, messageID, text, parseMode)
		if err == nil || isTelegramMessageNotModified(err) {
			return nil
		}
		if !final || !isTelegramTooManyRequests(err) {
			return err
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

func (s *telegramOutboundStream) toolStatusBot(ctx context.Context) (*tele.Bot, error) {
	if err := s.adapter.waitStreamLimit(ctx); err != nil {
		return nil, err
	}
	return s.getBot(ctx)
}

func (s *telegramOutboundStream) toolStatusLogger() *slog.Logger {
	if s.adapter == nil {
		return nil
	}
	return s.adapter.logger
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
