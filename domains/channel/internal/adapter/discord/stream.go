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

	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/internal/redact"
)

type discordOutboundStream struct {
	adapter      *DiscordAdapter
	cfg          gateway.ChannelConfig
	target       string
	reply        *gateway.ReplyRef
	session      *discordgo.Session
	closed       atomic.Bool
	mu           sync.Mutex
	msgID        string
	buffer       strings.Builder
	lastUpdate   time.Time
	toolMessages map[string]string
}

func (s *discordOutboundStream) Push(ctx context.Context, event gateway.PreparedStreamEvent) error {
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
	case gateway.StreamEventStatus:
		if event.Status == gateway.StreamStatusStarted {
			return s.ensureMessage("Thinking...")
		}
		return nil

	case gateway.StreamEventDelta:
		if event.Delta == "" || event.Phase == gateway.StreamPhaseReasoning {
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

	case gateway.StreamEventFinal:
		s.mu.Lock()
		bufText := strings.TrimSpace(s.buffer.String())
		s.mu.Unlock()
		finalText := bufText
		var finalMessage *gateway.Message
		if event.Final != nil && !event.Final.Message.Message.IsEmpty() {
			finalText = renderDiscordStreamFinalText(event.Final.Message.Message, bufText)
			finalMessage = &event.Final.Message.Message
		}
		if finalText != "" {
			actions := []gateway.Action(nil)
			if event.Final != nil {
				actions = event.Final.Message.Message.Actions
			}
			return s.finalizeMessage(finalText, actions, finalMessage)
		}
		return nil

	case gateway.StreamEventError:
		errText := redact.Text(strings.TrimSpace(event.Error))
		if errText == "" {
			return nil
		}
		return s.finalizeMessage("Error: "+errText, nil, nil)

	case gateway.StreamEventAttachment:
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

	case gateway.StreamEventToolCallStart:
		s.mu.Lock()
		bufText := strings.TrimSpace(s.buffer.String())
		s.mu.Unlock()
		if bufText != "" {
			if err := s.finalizeMessage(bufText, nil, nil); err != nil {
				return err
			}
		}
		s.resetStreamState()
		return s.sendToolCallMessage(event.ToolCall, gateway.BuildToolCallStart(event.ToolCall))
	case gateway.StreamEventToolCallEnd:
		return s.sendToolCallMessage(event.ToolCall, gateway.BuildToolCallEnd(event.ToolCall))

	case gateway.StreamEventAgentStart, gateway.StreamEventAgentEnd, gateway.StreamEventPhaseStart, gateway.StreamEventPhaseEnd, gateway.StreamEventProcessingStarted, gateway.StreamEventProcessingCompleted, gateway.StreamEventProcessingFailed:
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

func renderDiscordStreamFinalText(msg gateway.Message, buffered string) string {
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

func (s *discordOutboundStream) finalizeMessage(text string, actions []gateway.Action, source *gateway.Message) error {
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
func (s *discordOutboundStream) sendToolCallMessage(tc *gateway.StreamToolCall, p gateway.ToolCallPresentation) error {
	text := truncateDiscordText(strings.TrimSpace(gateway.RenderToolCallMessageMarkdown(p)))
	if text == "" {
		return nil
	}
	callID := ""
	if tc != nil {
		callID = strings.TrimSpace(tc.CallID)
	}
	if p.Status != gateway.ToolCallStatusRunning && callID != "" {
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
	if p.Status == gateway.ToolCallStatusRunning && callID != "" && msg != nil && msg.ID != "" {
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

func (s *discordOutboundStream) sendAttachment(ctx context.Context, att gateway.PreparedAttachment) error {
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
