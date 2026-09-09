package qq

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/redact"
)

type qqOutboundStream struct {
	target string
	reply  *channel.ReplyRef
	send   func(context.Context, channel.PreparedOutboundMessage) error

	closed      atomic.Bool
	mu          sync.Mutex
	buffer      strings.Builder
	attachments []channel.PreparedAttachment
	sentText    bool
}

func (a *QQAdapter) OpenStream(_ context.Context, cfg channel.ChannelConfig, target string, opts channel.StreamOptions) (channel.PreparedOutboundStream, error) {
	parsed, err := parseConfig(cfg.Credentials)
	if err != nil {
		return nil, fmt.Errorf("qq open stream: %w", err)
	}
	redact.SetSecrets("qq:"+parsed.AppID, parsed.AppSecret)
	return &qqOutboundStream{
		target: target,
		reply:  opts.Reply,
		send: func(ctx context.Context, msg channel.PreparedOutboundMessage) error {
			if msg.Target == "" {
				msg.Target = target
			}
			if msg.Message.Message.Reply == nil && opts.Reply != nil {
				msg.Message.Message.Reply = opts.Reply
			}
			return a.Send(ctx, cfg, msg)
		},
	}, nil
}

func (s *qqOutboundStream) Push(ctx context.Context, event channel.PreparedStreamEvent) error {
	if s == nil || s.send == nil {
		return errors.New("qq stream not configured")
	}
	if s.closed.Load() {
		return errors.New("qq stream is closed")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	switch event.Type {
	case channel.StreamEventStatus,
		channel.StreamEventPhaseStart,
		channel.StreamEventPhaseEnd,
		channel.StreamEventToolCallStart,
		channel.StreamEventAgentStart,
		channel.StreamEventAgentEnd,
		channel.StreamEventProcessingStarted,
		channel.StreamEventProcessingCompleted,
		channel.StreamEventProcessingFailed:
		return nil
	case channel.StreamEventToolCallEnd:
		text := strings.TrimSpace(channel.RenderToolCallMessage(channel.BuildToolCallEnd(event.ToolCall)))
		if text == "" {
			return nil
		}
		return s.send(ctx, channel.PreparedOutboundMessage{
			Target: s.target,
			Message: channel.PreparedMessage{
				Message: channel.Message{Format: channel.MessageFormatPlain, Text: text, Reply: s.reply},
			},
		})
	case channel.StreamEventDelta:
		if event.Phase == channel.StreamPhaseReasoning || event.Delta == "" {
			return nil
		}
		s.mu.Lock()
		s.buffer.WriteString(event.Delta)
		s.mu.Unlock()
		return nil
	case channel.StreamEventAttachment:
		if len(event.Attachments) == 0 {
			return nil
		}
		s.mu.Lock()
		s.attachments = append(s.attachments, event.Attachments...)
		s.mu.Unlock()
		return nil
	case channel.StreamEventError:
		errText := redact.Text(strings.TrimSpace(event.Error))
		if errText == "" {
			return nil
		}
		return s.flush(ctx, channel.PreparedMessage{
			Message: channel.Message{
				Text: "Error: " + errText,
			},
		})
	case channel.StreamEventFinal:
		if event.Final == nil {
			return errors.New("qq stream final payload is required")
		}
		return s.flush(ctx, event.Final.Message)
	default:
		return nil
	}
}

func (s *qqOutboundStream) Close(ctx context.Context) error {
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

func (s *qqOutboundStream) flush(ctx context.Context, msg channel.PreparedMessage) error {
	s.mu.Lock()
	bufferedText := strings.TrimSpace(s.buffer.String())
	bufferedAttachments := append([]channel.PreparedAttachment(nil), s.attachments...)
	alreadySentText := s.sentText
	s.buffer.Reset()
	s.attachments = nil
	s.mu.Unlock()

	logicalMsg := msg.LogicalMessage()
	if bufferedText != "" {
		logicalMsg.Text = bufferedText
		logicalMsg.Parts = nil
		if logicalMsg.Format == "" {
			logicalMsg.Format = channel.MessageFormatPlain
		}
	} else if alreadySentText && len(bufferedAttachments) == 0 && len(msg.Attachments) == 0 && strings.TrimSpace(logicalMsg.PlainText()) != "" {
		return nil
	}
	preparedAttachments := append([]channel.PreparedAttachment(nil), bufferedAttachments...)
	if len(bufferedAttachments) > 0 {
		logicalMsg.Attachments = append(preparedAttachmentLogicals(bufferedAttachments), logicalMsg.Attachments...)
		preparedAttachments = append(preparedAttachments, msg.Attachments...)
	} else {
		preparedAttachments = append(preparedAttachments, msg.Attachments...)
	}
	if logicalMsg.Reply == nil && s.reply != nil {
		logicalMsg.Reply = s.reply
	}
	if logicalMsg.IsEmpty() && len(preparedAttachments) == 0 {
		return nil
	}
	if err := s.send(ctx, channel.PreparedOutboundMessage{
		Target: s.target,
		Message: channel.PreparedMessage{
			Message:     logicalMsg,
			Attachments: preparedAttachments,
		},
	}); err != nil {
		return err
	}
	if strings.TrimSpace(logicalMsg.PlainText()) != "" {
		s.mu.Lock()
		s.sentText = true
		s.mu.Unlock()
	}
	return nil
}

func preparedAttachmentLogicals(attachments []channel.PreparedAttachment) []channel.Attachment {
	if len(attachments) == 0 {
		return nil
	}
	logical := make([]channel.Attachment, 0, len(attachments))
	for _, att := range attachments {
		logical = append(logical, att.Logical)
	}
	return logical
}
