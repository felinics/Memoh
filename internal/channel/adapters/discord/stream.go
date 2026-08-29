package discord

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/redact"
)

type discordOutboundStream struct {
	adapter      *DiscordAdapter
	cfg          channel.ChannelConfig
	target       string
	reply        *channel.ReplyRef
	session      *discordgo.Session
	closed       atomic.Bool
	mu           sync.Mutex
	msgID        string
	buffer       strings.Builder
	lastUpdate   time.Time
	toolMessages map[string]string
}

func (s *discordOutboundStream) Push(ctx context.Context, event channel.PreparedStreamEvent) error {
	if s == nil || s.adapter == nil {
		return errors.New("discord stream not configured")
	}
	if s.closed.Load() {
		return errors.New("discord stream is closed")
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	switch event.Type {
	case channel.StreamEventStatus:
		if event.Status == channel.StreamStatusStarted {
			return s.ensureMessage("Thinking...")
		}
		return nil

	case channel.StreamEventDelta:
		if event.Delta == "" || event.Phase == channel.StreamPhaseReasoning {
			return nil
		}
		s.mu.Lock()
		s.buffer.WriteString(event.Delta)
		s.mu.Unlock()

		// Discord has strict rate limits, only update periodically
		if time.Since(s.lastUpdate) > 2*time.Second {
			return s.updateMessage()
		}
		return nil

	case channel.StreamEventFinal:
		s.mu.Lock()
		bufText := strings.TrimSpace(s.buffer.String())
		s.mu.Unlock()
		finalText := bufText
		var finalMessage *channel.Message
		if event.Final != nil && !event.Final.Message.Message.IsEmpty() {
			finalText = renderDiscordStreamFinalText(event.Final.Message.Message, bufText)
			finalMessage = &event.Final.Message.Message
		}
		if finalText != "" {
			actions := []channel.Action(nil)
			if event.Final != nil {
				actions = event.Final.Message.Message.Actions
			}
			return s.finalizeMessage(finalText, actions, finalMessage)
		}
		return nil

	case channel.StreamEventError:
		errText := redact.Text(strings.TrimSpace(event.Error))
		if errText == "" {
			return nil
		}
		return s.finalizeMessage("Error: "+errText, nil, nil)

	case channel.StreamEventAttachment:
		if len(event.Attachments) == 0 {
			return nil
		}
		// Finalize current text message before sending attachments
		s.mu.Lock()
		finalText := strings.TrimSpace(s.buffer.String())
		s.mu.Unlock()
		if finalText != "" {
			if err := s.finalizeMessage(finalText, nil, nil); err != nil {
				return err
			}
		}
		// Send attachments
		for _, att := range event.Attachments {
			if err := s.sendAttachment(ctx, att); err != nil {
				return err
			}
		}
		return nil

	case channel.StreamEventToolCallStart:
		s.mu.Lock()
		bufText := strings.TrimSpace(s.buffer.String())
		s.mu.Unlock()
		if bufText != "" {
			if err := s.finalizeMessage(bufText, nil, nil); err != nil {
				return err
			}
		}
		s.resetStreamState()
		return s.sendToolCallMessage(event.ToolCall, channel.BuildToolCallStart(event.ToolCall))
	case channel.StreamEventToolCallEnd:
		return s.sendToolCallMessage(event.ToolCall, channel.BuildToolCallEnd(event.ToolCall))

	case channel.StreamEventAgentStart, channel.StreamEventAgentEnd, channel.StreamEventPhaseStart, channel.StreamEventPhaseEnd, channel.StreamEventProcessingStarted, channel.StreamEventProcessingCompleted, channel.StreamEventProcessingFailed:
		// Status events - no action needed for Discord
		return nil

	default:
		return fmt.Errorf("unsupported stream event type: %s", event.Type)
	}
}

func (s *discordOutboundStream) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	s.closed.Store(true)
	return nil
}

func renderDiscordStreamFinalText(msg channel.Message, buffered string) string {
	if rich := renderDiscordMessagePartsContent(msg); rich != "" {
		return rich
	}
	if authoritative := strings.TrimSpace(msg.PlainText()); authoritative != "" {
		return authoritative
	}
	return strings.TrimSpace(buffered)
}

func (s *discordOutboundStream) ensureMessage(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.msgID != "" {
		return nil
	}

	content := truncateDiscordText(text)
	messageSend := &discordgo.MessageSend{
		Content:         content,
		AllowedMentions: discordAllowedMentionsNone(),
	}
	if s.reply != nil && s.reply.MessageID != "" {
		messageSend.Reference = &discordgo.MessageReference{
			ChannelID: s.target,
			MessageID: s.reply.MessageID,
		}
	}

	var msg *discordgo.Message
	var err error
	msg, err = s.session.ChannelMessageSendComplex(s.target, messageSend)
	if err != nil {
		return err
	}

	s.msgID = msg.ID
	s.lastUpdate = time.Now()
	return nil
}

func (s *discordOutboundStream) updateMessage() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.msgID == "" {
		return nil
	}

	content := s.buffer.String()
	if content == "" {
		return nil
	}

	content = truncateDiscordText(content)

	edit := discordgo.NewMessageEdit(s.target, s.msgID)
	edit.SetContent(content)
	edit.AllowedMentions = discordAllowedMentionsNone()
	_, err := s.session.ChannelMessageEditComplex(edit)
	if err != nil {
		return err
	}

	s.lastUpdate = time.Now()
	return nil
}

func (s *discordOutboundStream) finalizeMessage(text string, actions []channel.Action, source *channel.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	text = truncateDiscordText(text)
	components, err := discordURLActionComponents(actions)
	if err != nil {
		return err
	}
	allowedMentions := discordAllowedMentionsNone()
	if source != nil {
		allowedMentions = discordAllowedMentionsForMessage(*source)
	}

	if s.msgID == "" {
		var msg *discordgo.Message
		messageSend := &discordgo.MessageSend{
			Content:         text,
			Components:      components,
			AllowedMentions: allowedMentions,
		}
		if s.reply != nil && s.reply.MessageID != "" {
			messageSend.Reference = &discordgo.MessageReference{
				ChannelID: s.target,
				MessageID: s.reply.MessageID,
			}
		}
		msg, err = s.session.ChannelMessageSendComplex(s.target, messageSend)
		if err != nil {
			return err
		}
		s.msgID = msg.ID
		s.lastUpdate = time.Now()
		return nil
	}

	if len(components) > 0 {
		edit := discordgo.NewMessageEdit(s.target, s.msgID)
		edit.SetContent(text)
		edit.Components = &components
		edit.AllowedMentions = allowedMentions
		_, err := s.session.ChannelMessageEditComplex(edit)
		return err
	}
	edit := discordgo.NewMessageEdit(s.target, s.msgID)
	edit.SetContent(text)
	edit.AllowedMentions = allowedMentions
	_, err = s.session.ChannelMessageEditComplex(edit)
	return err
}

// sendToolCallMessage posts a Discord message on tool_call_start and edits it
// on tool_call_end so the running → completed/failed transition is contained
// in one visible post. Falls back to a new message if the edit fails.
func (s *discordOutboundStream) sendToolCallMessage(tc *channel.StreamToolCall, p channel.ToolCallPresentation) error {
	text := truncateDiscordText(strings.TrimSpace(channel.RenderToolCallMessageMarkdown(p)))
	if text == "" {
		return nil
	}
	callID := ""
	if tc != nil {
		callID = strings.TrimSpace(tc.CallID)
	}
	if p.Status != channel.ToolCallStatusRunning && callID != "" {
		if msgID, ok := s.lookupToolCallMessage(callID); ok {
			edit := discordgo.NewMessageEdit(s.target, msgID)
			edit.SetContent(text)
			edit.AllowedMentions = discordAllowedMentionsNone()
			if _, err := s.session.ChannelMessageEditComplex(edit); err == nil {
				s.forgetToolCallMessage(callID)
				return nil
			}
			s.forgetToolCallMessage(callID)
		}
	}
	var msg *discordgo.Message
	var err error
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
	msg, err = s.session.ChannelMessageSendComplex(s.target, messageSend)
	if err != nil {
		return err
	}
	if p.Status == channel.ToolCallStatusRunning && callID != "" && msg != nil && msg.ID != "" {
		s.storeToolCallMessage(callID, msg.ID)
	}
	return nil
}

func (s *discordOutboundStream) lookupToolCallMessage(callID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.toolMessages == nil {
		return "", false
	}
	v, ok := s.toolMessages[callID]
	return v, ok
}

func (s *discordOutboundStream) storeToolCallMessage(callID, msgID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.toolMessages == nil {
		s.toolMessages = make(map[string]string)
	}
	s.toolMessages[callID] = msgID
}

func (s *discordOutboundStream) forgetToolCallMessage(callID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.toolMessages == nil {
		return
	}
	delete(s.toolMessages, callID)
}

func (s *discordOutboundStream) resetStreamState() {
	s.mu.Lock()
	s.msgID = ""
	s.buffer.Reset()
	s.lastUpdate = time.Time{}
	s.mu.Unlock()
}

func (s *discordOutboundStream) sendAttachment(ctx context.Context, att channel.PreparedAttachment) error {
	file, err := discordPreparedAttachmentToFile(ctx, att)
	if err != nil {
		return err
	}

	messageSend := &discordgo.MessageSend{
		Files:           []*discordgo.File{file},
		AllowedMentions: discordAllowedMentionsNone(),
	}

	// Add reply reference if this is the first message and we have a reply target
	if s.reply != nil && s.reply.MessageID != "" {
		messageSend.Reference = &discordgo.MessageReference{
			ChannelID: s.target,
			MessageID: s.reply.MessageID,
		}
	}

	_, err = s.session.ChannelMessageSendComplex(s.target, messageSend)
	return err
}
