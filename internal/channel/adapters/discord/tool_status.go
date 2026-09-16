package discord

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/felinics/memoh/internal/channel"
)

const (
	// discordToolStatusEditInterval spaces status edits so a burst of tool
	// events does not run into Discord's per-channel rate limit.
	discordToolStatusEditInterval = time.Second
	// discordToolStatusMaxRunes keeps the status inside Discord's message limit.
	discordToolStatusMaxRunes = discordMaxLength - 100
)

// pushToolCallReusingMessage routes a tool event when the bot reuses one
// message per tool batch. Ordinary calls join the batch status message;
// approval and user-input prompts end the batch and keep their own message.
func (s *discordOutboundStream) pushToolCallReusingMessage(ctx context.Context, eventType channel.StreamEventType, tc *channel.StreamToolCall) error {
	if s.toolStatus == nil {
		s.toolStatus = channel.NewToolCallStatusTracker(s.toolStatusLogger(), s.newToolStatusMessage)
	}
	return s.toolStatus.Route(ctx, channel.ToolCallStatusHooks{
		FlushText: func(context.Context) error { return s.flushBufferedText() },
		PushCall: func(_ context.Context, eventType channel.StreamEventType, tc *channel.StreamToolCall) error {
			if eventType == channel.StreamEventToolCallEnd {
				return s.sendToolCallMessage(tc, channel.BuildToolCallEnd(tc))
			}
			return s.pushToolCallStart(tc)
		},
	}, eventType, tc)
}

// newToolStatusMessage sends the status message of a batch on its first publish
// and edits it afterwards.
func (s *discordOutboundStream) newToolStatusMessage() *channel.ToolCallStatusMessage {
	editor := &channel.StatusMessageEditor{
		Send: func(ctx context.Context, text string) (string, error) {
			send := &discordgo.MessageSend{Content: text, AllowedMentions: discordAllowedMentionsNone()}
			if s.reply != nil && s.reply.MessageID != "" {
				send.Reference = &discordgo.MessageReference{ChannelID: s.target, MessageID: s.reply.MessageID}
			}
			msg, err := s.session.ChannelMessageSendComplex(s.target, send, discordgo.WithContext(ctx))
			if err != nil || msg == nil {
				return "", err
			}
			return msg.ID, nil
		},
		Edit: func(ctx context.Context, id, text string, _ bool) error {
			edit := discordgo.NewMessageEdit(s.target, id)
			edit.SetContent(text)
			edit.AllowedMentions = discordAllowedMentionsNone()
			_, err := s.session.ChannelMessageEditComplex(edit, discordgo.WithContext(ctx))
			return err
		},
		Gone: isDiscordUnknownMessage,
	}
	return channel.NewToolCallStatusMessage(channel.ToolCallStatusOptions{
		Publish: func(ctx context.Context, snapshot channel.ToolCallStatusSnapshot) error {
			text := truncateDiscordText(strings.TrimSpace(snapshot.RenderMarkdown(discordToolStatusMaxRunes)))
			return editor.Publish(ctx, text, snapshot.Final)
		},
		MinInterval: s.toolStatusInterval,
		Logger:      s.toolStatusLogger(),
	})
}

func (s *discordOutboundStream) toolStatusLogger() *slog.Logger {
	if s.adapter == nil {
		return nil
	}
	return s.adapter.logger
}

// isDiscordUnknownMessage reports that the status message was deleted, so the
// batch carries on in a new message instead of losing its state.
func isDiscordUnknownMessage(err error) bool {
	var restErr *discordgo.RESTError
	if !errors.As(err, &restErr) {
		return false
	}
	if restErr.Message != nil && restErr.Message.Code == discordgo.ErrCodeUnknownMessage {
		return true
	}
	return restErr.Response != nil && restErr.Response.StatusCode == http.StatusNotFound
}
