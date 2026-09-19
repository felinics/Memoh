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
		s.toolStatus = channel.NewToolCallStatusTracker(s.newToolStatusMessage)
	}
	listed := s.toolStatus.Update(ctx, eventType, tc)
	if !channel.UsesToolCallStatusMessage(tc) {
		s.finishToolStatus(ctx)
		if eventType == channel.StreamEventToolCallEnd {
			return s.sendToolCallMessage(tc, channel.BuildToolCallEnd(tc))
		}
		return s.pushToolCallStart(tc)
	}
	if listed {
		return nil
	}
	if !s.toolStatus.Active() {
		if err := s.flushBufferedText(); err != nil {
			return err
		}
	}
	s.toolStatus.Join(ctx, eventType, tc)
	return nil
}

// finishToolStatus ends the current tool batch before anything else is posted,
// so the status message keeps its place above what follows. A failure only
// costs the status update; the caller still delivers its own message.
func (s *discordOutboundStream) finishToolStatus(ctx context.Context) {
	if err := s.toolStatus.Finish(ctx); err != nil && s.adapter != nil && s.adapter.logger != nil {
		s.adapter.logger.Warn("discord: finish tool status failed",
			slog.String("config_id", s.cfg.ID),
			slog.Any("error", err),
		)
	}
}

func (s *discordOutboundStream) newToolStatusMessage() *channel.ToolCallStatusMessage {
	editor := &discordToolStatusEditor{stream: s}
	opts := channel.ToolCallStatusOptions{
		Publish:     editor.publish,
		MinInterval: s.toolStatusInterval,
	}
	if s.adapter != nil {
		opts.Logger = s.adapter.logger
	}
	return channel.NewToolCallStatusMessage(opts)
}

// discordToolStatusEditor owns the status message of one tool batch: it sends
// the message on the first publish and edits it afterwards. The status message
// never publishes concurrently, so no locking is needed.
type discordToolStatusEditor struct {
	stream    *discordOutboundStream
	messageID string
	lastText  string
}

func (e *discordToolStatusEditor) publish(ctx context.Context, snapshot channel.ToolCallStatusSnapshot) error {
	text := truncateDiscordText(strings.TrimSpace(snapshot.RenderMarkdown(discordToolStatusMaxRunes)))
	if text == "" || (e.messageID != "" && text == e.lastText) {
		return nil
	}
	s := e.stream
	if e.messageID != "" {
		edit := discordgo.NewMessageEdit(s.target, e.messageID)
		edit.SetContent(text)
		edit.AllowedMentions = discordAllowedMentionsNone()
		_, err := s.session.ChannelMessageEditComplex(edit, discordgo.WithContext(ctx))
		if err == nil {
			e.lastText = text
			return nil
		}
		if !isDiscordUnknownMessage(err) {
			return err
		}
		// The status message was deleted. Carry the batch on in a new message
		// instead of losing its state.
		e.messageID = ""
	}
	messageSend := &discordgo.MessageSend{
		Content:         text,
		AllowedMentions: discordAllowedMentionsNone(),
	}
	if s.reply != nil && s.reply.MessageID != "" {
		messageSend.Reference = &discordgo.MessageReference{
			ChannelID: s.target,
			MessageID: s.reply.MessageID,
		}
	}
	msg, err := s.session.ChannelMessageSendComplex(s.target, messageSend, discordgo.WithContext(ctx))
	if err != nil {
		return err
	}
	if msg != nil {
		e.messageID = msg.ID
	}
	e.lastText = text
	return nil
}

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
